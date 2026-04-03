#!/usr/bin/env bash

set -euo pipefail

SKILLS_SMOKE_REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

SKILLS_SMOKE_WORKDIR=""
SKILLS_SMOKE_ARTIFACT_DIR=""
SKILLS_SMOKE_MINIREDIS_PID=""
SKILLS_SMOKE_MINIREDIS_LOG=""
SKILLS_SMOKE_REDIS_ADDR=""
SKILLS_SMOKE_MYSQL_PID=""
SKILLS_SMOKE_MYSQL_LOG=""
SKILLS_SMOKE_APISERVER_PID=""
SKILLS_SMOKE_APISERVER_LOG=""
SKILLS_SMOKE_APISERVER_CFG=""
SKILLS_SMOKE_AGENT_PID=""
SKILLS_SMOKE_AGENT_LOG=""
SKILLS_SMOKE_DS_PID=""
SKILLS_SMOKE_DS_LOG=""
SKILLS_SMOKE_ARTIFACT_PID=""
SKILLS_SMOKE_ARTIFACT_LOG=""

skills_smoke_wait_http_ok() {
  local url="$1"
  local attempts="${2:-60}"
  local delay="${3:-1}"
  local i
  for ((i = 1; i <= attempts; i++)); do
    if [[ "$(curl -sS -o /dev/null -w '%{http_code}' "${url}")" == "200" ]]; then
      return 0
    fi
    sleep "${delay}"
  done
  return 1
}

skills_smoke_find_mysql_bin() {
  local name="$1"
  if command -v "${name}" >/dev/null 2>&1; then
    command -v "${name}"
    return 0
  fi
  if command -v brew >/dev/null 2>&1; then
    local prefix
    prefix="$(brew --prefix mariadb 2>/dev/null || true)"
    if [[ -n "${prefix}" && -x "${prefix}/bin/${name}" ]]; then
      printf '%s\n' "${prefix}/bin/${name}"
      return 0
    fi
    prefix="$(brew --prefix mysql 2>/dev/null || true)"
    if [[ -n "${prefix}" && -x "${prefix}/bin/${name}" ]]; then
      printf '%s\n' "${prefix}/bin/${name}"
      return 0
    fi
  fi
  return 1
}

skills_smoke_start_miniredis() {
  local addr="${SKILLS_SMOKE_REDIS_BIND_ADDR:-127.0.0.1:${SKILLS_SMOKE_REDIS_PORT:-26379}}"
  local password="${SKILLS_SMOKE_REDIS_PASSWORD:-}"
  SKILLS_SMOKE_MINIREDIS_LOG="${SKILLS_SMOKE_WORKDIR}/miniredis.log"
  (
    cd "${SKILLS_SMOKE_REPO_ROOT}" && \
      go run ./scripts/go/skills_smoke_miniredis.go --addr "${addr}" --password "${password}"
  ) >"${SKILLS_SMOKE_MINIREDIS_LOG}" 2>&1 &
  SKILLS_SMOKE_MINIREDIS_PID="$!"
  local deadline
  deadline="$(( $(date +%s) + 30 ))"
  while true; do
    if ! kill -0 "${SKILLS_SMOKE_MINIREDIS_PID}" >/dev/null 2>&1; then
      echo "miniredis exited early" >&2
      tail -n 120 "${SKILLS_SMOKE_MINIREDIS_LOG}" >&2 || true
      return 1
    fi
    if grep -Eq '^[0-9.]+:[0-9]+$' "${SKILLS_SMOKE_MINIREDIS_LOG}" 2>/dev/null; then
      SKILLS_SMOKE_REDIS_ADDR="$(head -n 1 "${SKILLS_SMOKE_MINIREDIS_LOG}")"
      export SKILLS_SMOKE_REDIS_ADDR
      return 0
    fi
    if (( $(date +%s) > deadline )); then
      echo "timed out waiting for miniredis address" >&2
      tail -n 120 "${SKILLS_SMOKE_MINIREDIS_LOG}" >&2 || true
      return 1
    fi
    sleep 1
  done
}

skills_smoke_mysql_install_args() {
  local install_bin="$1"
  local datadir="$2"
  local install_name
  install_name="$(basename "${install_bin}")"
  printf '%s\n' "--datadir=${datadir}"
  if [[ "${install_name}" == "mariadb-install-db" ]]; then
    printf '%s\n' "--auth-root-authentication-method=normal"
  fi
  printf '%s\n' "--skip-test-db"
}

skills_smoke_start_mysql() {
  if [[ -n "${SKILLS_SMOKE_MYSQL_ADDR:-}" ]]; then
    export SKILLS_SMOKE_MYSQL_USER="${SKILLS_SMOKE_MYSQL_USER:-root}"
    export SKILLS_SMOKE_MYSQL_PASSWORD="${SKILLS_SMOKE_MYSQL_PASSWORD:-}"
    export SKILLS_SMOKE_MYSQL_DATABASE="${SKILLS_SMOKE_MYSQL_DATABASE:-rca}"
    return 0
  fi

  local mysqld_bin install_bin client_bin
  mysqld_bin="$(skills_smoke_find_mysql_bin mariadbd || skills_smoke_find_mysql_bin mysqld || true)"
  install_bin="$(skills_smoke_find_mysql_bin mariadb-install-db || skills_smoke_find_mysql_bin mysql_install_db || true)"
  client_bin="$(skills_smoke_find_mysql_bin mariadb || skills_smoke_find_mysql_bin mysql || true)"

  if [[ -z "${mysqld_bin}" || -z "${install_bin}" || -z "${client_bin}" ]]; then
    echo "local MariaDB/MySQL binaries not found; set SKILLS_SMOKE_MYSQL_ADDR/USER/PASSWORD/DATABASE or install mariadb" >&2
    return 1
  fi

  local mysql_port="${SKILLS_SMOKE_MYSQL_PORT:-23306}"
  local datadir="${SKILLS_SMOKE_WORKDIR}/mysql"
  local socket_path="${SKILLS_SMOKE_WORKDIR}/mysql.sock"
  local pid_file="${SKILLS_SMOKE_WORKDIR}/mysql.pid"
  local mysql_err="${SKILLS_SMOKE_WORKDIR}/mysql.err"
  local -a install_args
  mkdir -p "${datadir}"
  if [[ ! -d "${datadir}/mysql" ]]; then
    mapfile -t install_args < <(skills_smoke_mysql_install_args "${install_bin}" "${datadir}")
    "${install_bin}" "${install_args[@]}" >/dev/null 2>&1
  fi

  SKILLS_SMOKE_MYSQL_LOG="${mysql_err}"
  "${mysqld_bin}" \
    --datadir="${datadir}" \
    --bind-address=127.0.0.1 \
    --port="${mysql_port}" \
    --socket="${socket_path}" \
    --pid-file="${pid_file}" \
    --skip-log-bin \
    --skip-name-resolve \
    --character-set-server=utf8mb4 \
    --collation-server=utf8mb4_unicode_ci \
    --innodb-buffer-pool-size=64M \
    --max-connections=64 \
    --log-error="${mysql_err}" \
    >"${SKILLS_SMOKE_WORKDIR}/mysql.stdout.log" 2>&1 &
  SKILLS_SMOKE_MYSQL_PID="$!"

  local deadline
  deadline="$(( $(date +%s) + 60 ))"
  while true; do
    if ! kill -0 "${SKILLS_SMOKE_MYSQL_PID}" >/dev/null 2>&1; then
      echo "mysqld exited early" >&2
      tail -n 120 "${mysql_err}" >&2 || true
      return 1
    fi
    if "${client_bin}" -h127.0.0.1 -P"${mysql_port}" -uroot -e 'SELECT 1' >/dev/null 2>&1; then
      break
    fi
    if (( $(date +%s) > deadline )); then
      echo "timed out waiting for local mysql" >&2
      tail -n 120 "${mysql_err}" >&2 || true
      return 1
    fi
    sleep 1
  done

  "${client_bin}" -h127.0.0.1 -P"${mysql_port}" -uroot -e "CREATE DATABASE IF NOT EXISTS rca CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;" >/dev/null

  export SKILLS_SMOKE_MYSQL_ADDR="127.0.0.1:${mysql_port}"
  export SKILLS_SMOKE_MYSQL_USER="root"
  export SKILLS_SMOKE_MYSQL_PASSWORD=""
  export SKILLS_SMOKE_MYSQL_DATABASE="rca"
}

skills_smoke_write_apiserver_config() {
  SKILLS_SMOKE_APISERVER_CFG="${SKILLS_SMOKE_WORKDIR}/rca-apiserver-smoke.yaml"
  cat >"${SKILLS_SMOKE_APISERVER_CFG}" <<EOF
http:
  addr: 127.0.0.1:${SKILLS_SMOKE_APISERVER_PORT:-25555}
  timeout: 30s
otel:
  output-mode: classic
  level: debug
  add-source: true
  slog:
    format: json
    time-format: "2006-01-02 15:04:05"
    output: stdout
coredb:
  type: mysql
  addr: ${SKILLS_SMOKE_MYSQL_ADDR}
  username: ${SKILLS_SMOKE_MYSQL_USER}
  password: "${SKILLS_SMOKE_MYSQL_PASSWORD}"
  database: ${SKILLS_SMOKE_MYSQL_DATABASE}
  max-connection-life-time: 10s
  max-idle-connections: 20
  max-open-connections: 20
redis:
  enabled: true
  addr: "${SKILLS_SMOKE_REDIS_ADDR}"
  db: 0
  password: "${SKILLS_SMOKE_REDIS_PASSWORD:-}"
  fail_open: true
  pubsub:
    enabled: true
    topic_ai_job_signal: "rca:ai_job_queue_signal"
  limiter:
    enabled: true
    mode: "both"
    global_qps: 20
    channel_qps: 0
    burst: 20
  streams:
    enabled: false
    notice_delivery_stream: "rca:notice:delivery_stream"
    consumer_group: "notice_delivery_workers"
    reclaim_idle_seconds: 60
  alerting:
    enabled: true
alerting:
  ingest_policy:
    dedup_window_seconds: 0
    burst:
      window_seconds: 0
      threshold: 0
    redis_backend:
      enabled: false
      key_prefix: "rca:alert"
ai_job_longpoll:
  poll_interval: 1s
  watermark_cache_ttl: 1s
  max_polling_waiters: 200
  db_error_window: 20
  db_error_rate_threshold: 0.5
  db_error_min_samples: 6
notice_worker:
  poll_interval: 1s
  batch_size: 16
  lock_timeout: 60s
  channel_concurrency: 2
  global_qps: 20
  channel_qps: 0
  redis:
    key_prefix: "rca:notice"
    conc_ttl: 60s
    window_ttl: 2s
mcp:
  isolation:
    mode: filter
  tools:
    query_logs:
      enabled: true
      riskLevel: readonly
    query_metrics:
      enabled: true
      riskLevel: readonly
EOF
}

skills_smoke_start_apiserver() {
  skills_smoke_write_apiserver_config
  local toolset_base_url="http://127.0.0.1:${SKILLS_SMOKE_APISERVER_PORT:-25555}"
  local strategy_json toolset_json
  strategy_json='{"pipelines":{"basic_rca":{"template_id":"basic_rca"}}}'
  toolset_json='{"pipelines":{"basic_rca":["default"]},"toolsets":{"default":{"providers":[{"type":"mcp_http","base_url":"'"${toolset_base_url}"'","allow_tools":["query_metrics","query_logs"],"scopes":"evidence.query"}]}}}'
  SKILLS_SMOKE_APISERVER_LOG="${SKILLS_SMOKE_WORKDIR}/rca-apiserver.log"
  (
    cd "${SKILLS_SMOKE_REPO_ROOT}" && \
      RCA_STRATEGY_CONFIG_JSON="${strategy_json}" \
      RCA_TOOLSET_CONFIG_JSON="${toolset_json}" \
      GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn \
      go run ./cmd/rca-apiserver --config "${SKILLS_SMOKE_APISERVER_CFG}"
  ) >"${SKILLS_SMOKE_APISERVER_LOG}" 2>&1 &
  SKILLS_SMOKE_APISERVER_PID="$!"
  if ! skills_smoke_wait_http_ok "http://127.0.0.1:${SKILLS_SMOKE_APISERVER_PORT:-25555}/healthz" 90 1; then
    echo "apiserver failed to start" >&2
    tail -n 200 "${SKILLS_SMOKE_APISERVER_LOG}" >&2 || true
    return 1
  fi
}

skills_smoke_start_mock_agent() {
  local host="${SKILLS_SMOKE_AGENT_HOST:-127.0.0.1}"
  local port="${SKILLS_SMOKE_AGENT_PORT:-29131}"
  SKILLS_SMOKE_AGENT_LOG="${SKILLS_SMOKE_WORKDIR}/mock-agent.log"
  python3 "${SKILLS_SMOKE_REPO_ROOT}/scripts/mocks/skills_mock_agent.py" "${host}" "${port}" >"${SKILLS_SMOKE_AGENT_LOG}" 2>&1 &
  SKILLS_SMOKE_AGENT_PID="$!"
  if ! skills_smoke_wait_http_ok "http://${host}:${port}/healthz" 30 1; then
    echo "mock agent failed to start" >&2
    tail -n 120 "${SKILLS_SMOKE_AGENT_LOG}" >&2 || true
    return 1
  fi
}

skills_smoke_start_mock_datasource() {
  local host="${SKILLS_SMOKE_DS_HOST:-127.0.0.1}"
  local port="${SKILLS_SMOKE_DS_PORT:-29132}"
  SKILLS_SMOKE_DS_LOG="${SKILLS_SMOKE_WORKDIR}/mock-datasource.log"
  python3 "${SKILLS_SMOKE_REPO_ROOT}/scripts/mocks/skills_mock_datasource.py" "${host}" "${port}" >"${SKILLS_SMOKE_DS_LOG}" 2>&1 &
  SKILLS_SMOKE_DS_PID="$!"
  if ! skills_smoke_wait_http_ok "http://${host}:${port}/healthz" 30 1; then
    echo "mock datasource failed to start" >&2
    tail -n 120 "${SKILLS_SMOKE_DS_LOG}" >&2 || true
    return 1
  fi
}

skills_smoke_start_artifact_server() {
  SKILLS_SMOKE_ARTIFACT_DIR="${SKILLS_SMOKE_WORKDIR}/artifacts"
  mkdir -p "${SKILLS_SMOKE_ARTIFACT_DIR}"
  local host="${SKILLS_SMOKE_ARTIFACT_HOST:-127.0.0.1}"
  local port="${SKILLS_SMOKE_ARTIFACT_PORT:-29133}"
  SKILLS_SMOKE_ARTIFACT_LOG="${SKILLS_SMOKE_WORKDIR}/artifact-server.log"
  (
    cd "${SKILLS_SMOKE_ARTIFACT_DIR}" && \
      python3 -m http.server "${port}" --bind "${host}"
  ) >"${SKILLS_SMOKE_ARTIFACT_LOG}" 2>&1 &
  SKILLS_SMOKE_ARTIFACT_PID="$!"
  if ! skills_smoke_wait_http_ok "http://${host}:${port}/" 30 1; then
    echo "artifact server failed to start" >&2
    tail -n 120 "${SKILLS_SMOKE_ARTIFACT_LOG}" >&2 || true
    return 1
  fi
}

skills_smoke_env_start() {
  SKILLS_SMOKE_WORKDIR="${SKILLS_SMOKE_WORKDIR:-$(mktemp -d)}"
  mkdir -p "${SKILLS_SMOKE_WORKDIR}"
  skills_smoke_start_miniredis
  skills_smoke_start_mysql
  skills_smoke_start_artifact_server
  skills_smoke_start_mock_agent
  skills_smoke_start_mock_datasource
  skills_smoke_start_apiserver
}

skills_smoke_env_export() {
  export BASE_URL="http://127.0.0.1:${SKILLS_SMOKE_APISERVER_PORT:-25555}"
  export SCOPES="*"
  export CONFIG_SCOPES="config.admin,ai.read,ai.run,datasource.admin"
  export AGENT_MODEL="mock-skill-agent"
  export AGENT_BASE_URL="http://${SKILLS_SMOKE_AGENT_HOST:-127.0.0.1}:${SKILLS_SMOKE_AGENT_PORT:-29131}/v1"
  export AGENT_API_KEY="mock-agent-key"
  export DS_BASE_URL="http://${SKILLS_SMOKE_DS_HOST:-127.0.0.1}:${SKILLS_SMOKE_DS_PORT:-29132}"
  export SKILL_RELEASE_MODE="register"
  export ARTIFACT_BASE_URL="http://${SKILLS_SMOKE_ARTIFACT_HOST:-127.0.0.1}:${SKILLS_SMOKE_ARTIFACT_PORT:-29133}"
  export ARTIFACT_DIR="${SKILLS_SMOKE_ARTIFACT_DIR}"
  export JOB_WAIT_TIMEOUT_SEC="${JOB_WAIT_TIMEOUT_SEC:-180}"
}

skills_smoke_env_stop() {
  local pid_var
  for pid_var in \
    SKILLS_SMOKE_APISERVER_PID \
    SKILLS_SMOKE_AGENT_PID \
    SKILLS_SMOKE_DS_PID \
    SKILLS_SMOKE_ARTIFACT_PID \
    SKILLS_SMOKE_MYSQL_PID \
    SKILLS_SMOKE_MINIREDIS_PID; do
    local pid="${!pid_var:-}"
    if [[ -n "${pid}" ]]; then
      kill "${pid}" >/dev/null 2>&1 || true
      wait "${pid}" >/dev/null 2>&1 || true
    fi
  done
  if [[ "${SKILLS_SMOKE_KEEP_WORKDIR:-0}" != "1" ]] && [[ -n "${SKILLS_SMOKE_WORKDIR}" && -d "${SKILLS_SMOKE_WORKDIR}" ]]; then
    rm -rf "${SKILLS_SMOKE_WORKDIR}"
  fi
}
