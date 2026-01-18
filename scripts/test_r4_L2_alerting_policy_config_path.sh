#!/usr/bin/env bash
set -euo pipefail

CURL="${CURL:-curl}"
WAIT_TIMEOUT_SEC="${WAIT_TIMEOUT_SEC:-45}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
CONFIG_PATH="${CONFIG_PATH:-${REPO_ROOT}/configs/rca-apiserver.yaml}"
SERVER_CMD_BASE="${SERVER_CMD_BASE:-GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn go run ./cmd/rca-apiserver --config ${CONFIG_PATH}}"

PORT_BASE="${PORT_BASE:-$((18720 + RANDOM % 120))}"
PORT_OK="${PORT_OK:-${PORT_BASE}}"
PORT_STRICT_FALSE="${PORT_STRICT_FALSE:-$((PORT_BASE + 1))}"
PORT_STRICT_TRUE="${PORT_STRICT_TRUE:-$((PORT_BASE + 2))}"

LAST_HTTP_CODE=""
LAST_BODY=""
CURRENT_STEP=""

SERVER_PID=""
SERVER_LOG=""
TMP_POLICY=""
TMP_BAD_PATH=""

truncate_2kb() {
	printf '%s' "${1:-}" | head -c 2048
}

fail_step() {
	local step="$1"
	local code="${2:-${LAST_HTTP_CODE:-UNKNOWN}}"
	local body="${3:-${LAST_BODY:-}}"
	echo "FAIL R4_L2 step=${step}"
	echo "http_code=${code}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	if [[ -n "${SERVER_LOG}" ]]; then
		echo "server_log_tail<<EOF"
		tail -n 120 "${SERVER_LOG}" 2>/dev/null | head -c 2048
		echo
		echo "EOF"
	fi
	exit 1
}

start_server() {
	local port="$1"
	local extra_flags="$2"
	SERVER_LOG="$(mktemp)"
	(
		cd "${REPO_ROOT}" && \
			bash -lc "${SERVER_CMD_BASE} --http.addr=127.0.0.1:${port} --redis.enabled=false ${extra_flags}"
	) >"${SERVER_LOG}" 2>&1 &
	SERVER_PID="$!"
}

stop_server() {
	if [[ -n "${SERVER_PID}" ]]; then
		kill "${SERVER_PID}" >/dev/null 2>&1 || true
		wait "${SERVER_PID}" >/dev/null 2>&1 || true
		SERVER_PID=""
	fi
}

wait_healthz_or_fail() {
	local port="$1"
	local deadline="$(( $(date +%s) + WAIT_TIMEOUT_SEC ))"
	local base_url="http://127.0.0.1:${port}"
	while true; do
		if "${CURL}" -sS "${base_url}/healthz" >/dev/null 2>&1; then
			return 0
		fi
		if ! kill -0 "${SERVER_PID}" >/dev/null 2>&1; then
			LAST_HTTP_CODE="SERVER_EXITED"
			LAST_BODY="$(cat "${SERVER_LOG}" 2>/dev/null || true)"
			fail_step "${CURRENT_STEP}"
		fi
		if (( $(date +%s) > deadline )); then
			LAST_HTTP_CODE="SERVER_TIMEOUT"
			LAST_BODY="$(cat "${SERVER_LOG}" 2>/dev/null || true)"
			fail_step "${CURRENT_STEP}"
		fi
		sleep 0.5
	done
}

assert_metric_contains_or_fail() {
	local port="$1"
	local expected="$2"
	local metrics_body
	metrics_body="$("${CURL}" -sS "http://127.0.0.1:${port}/metrics" 2>&1 || true)"
	if [[ "${metrics_body}" != *"${expected}"* ]]; then
		LAST_HTTP_CODE="ASSERT_METRIC_FAILED"
		LAST_BODY="${metrics_body}"
		fail_step "${CURRENT_STEP}"
	fi
}

assert_startup_fails_or_fail() {
	local port="$1"
	local extra_flags="$2"
	SERVER_LOG="$(mktemp)"
	(
		cd "${REPO_ROOT}" && \
			bash -lc "${SERVER_CMD_BASE} --http.addr=127.0.0.1:${port} --redis.enabled=false ${extra_flags}"
	) >"${SERVER_LOG}" 2>&1 &
	local pid="$!"
	local deadline="$(( $(date +%s) + WAIT_TIMEOUT_SEC ))"

	while true; do
		if "${CURL}" -sS "http://127.0.0.1:${port}/healthz" >/dev/null 2>&1; then
			kill "${pid}" >/dev/null 2>&1 || true
			wait "${pid}" >/dev/null 2>&1 || true
			LAST_HTTP_CODE="UNEXPECTED_HEALTHZ_OK"
			LAST_BODY="$(cat "${SERVER_LOG}" 2>/dev/null || true)"
			fail_step "${CURRENT_STEP}"
		fi
		if ! kill -0 "${pid}" >/dev/null 2>&1; then
			set +e
			wait "${pid}"
			local rc=$?
			set -e
			if (( rc == 0 )); then
				LAST_HTTP_CODE="UNEXPECTED_EXIT_0"
				LAST_BODY="$(cat "${SERVER_LOG}" 2>/dev/null || true)"
				fail_step "${CURRENT_STEP}"
			fi
			return 0
		fi
		if (( $(date +%s) > deadline )); then
			kill "${pid}" >/dev/null 2>&1 || true
			wait "${pid}" >/dev/null 2>&1 || true
			LAST_HTTP_CODE="SERVER_TIMEOUT"
			LAST_BODY="$(cat "${SERVER_LOG}" 2>/dev/null || true)"
			fail_step "${CURRENT_STEP}"
		fi
		sleep 0.5
	done
}

cleanup() {
	stop_server
	rm -f "${SERVER_LOG:-}" "${TMP_POLICY:-}"
}
trap cleanup EXIT

TMP_POLICY="$(mktemp)"
cat >"${TMP_POLICY}" <<'YAML'
version: 1
defaults:
  on_ingest:
    enabled: false
  on_escalation:
    enabled: false
  scheduled:
    enabled: false
triggers:
  on_ingest:
    rules:
      - name: "default"
        match: {}
        action:
          run: false
  on_escalation:
    rules:
      - name: "default"
        match: {}
        action:
          run: false
  scheduled:
    rules:
      - name: "default"
        match: {}
        action:
          run: false
YAML

TMP_BAD_PATH="${TMP_POLICY}.missing"

CURRENT_STEP="CLIPathLoad.Success"
start_server "${PORT_OK}" "--alerting-policy-path='${TMP_POLICY}' --alerting-policy-strict=true"
wait_healthz_or_fail "${PORT_OK}"
assert_metric_contains_or_fail "${PORT_OK}" 'alerting_policy_load_total{result="ok",source="cli"}'
echo "PASS R4_L2 step=${CURRENT_STEP}"
stop_server
rm -f "${SERVER_LOG}"
SERVER_LOG=""

CURRENT_STEP="BadPath.StrictFalse.Fallback"
start_server "${PORT_STRICT_FALSE}" "--alerting-policy-path='${TMP_BAD_PATH}' --alerting-policy-strict=false"
wait_healthz_or_fail "${PORT_STRICT_FALSE}"
assert_metric_contains_or_fail "${PORT_STRICT_FALSE}" 'alerting_policy_load_total{result="error",source="cli"}'
echo "PASS R4_L2 step=${CURRENT_STEP}"
stop_server
rm -f "${SERVER_LOG}"
SERVER_LOG=""

CURRENT_STEP="BadPath.StrictTrue.Fail"
assert_startup_fails_or_fail "${PORT_STRICT_TRUE}" "--alerting-policy-path='${TMP_BAD_PATH}' --alerting-policy-strict=true"
echo "PASS R4_L2 step=${CURRENT_STEP}"

echo "PASS R4_L2 alerting policy config path"
