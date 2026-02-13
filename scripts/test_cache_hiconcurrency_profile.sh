#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CONCURRENCY="${CONCURRENCY:-80}"
REQUESTS_PER_WORKER="${REQUESTS_PER_WORKER:-6}"
OPERATOR_TOKEN="${OPERATOR_TOKEN:-}"
SESSION_ID="${SESSION_ID:-}"
JOB_ID="${JOB_ID:-}"
LEFT_JOB_ID="${LEFT_JOB_ID:-}"
RIGHT_JOB_ID="${RIGHT_JOB_ID:-}"
CURL_BIN="${CURL_BIN:-curl}"
JQ_BIN="${JQ_BIN:-jq}"

require_bin() {
  local bin="$1"
  if ! command -v "${bin}" >/dev/null 2>&1; then
    echo "missing required binary: ${bin}" >&2
    exit 1
  fi
}

build_auth_headers() {
  AUTH_HEADER_ARGS=()
  if [[ -n "${OPERATOR_TOKEN}" ]]; then
    AUTH_HEADER_ARGS=(-H "Authorization: Bearer ${OPERATOR_TOKEN}")
  fi
}

json_get() {
  local url="$1"
  build_auth_headers
  "${CURL_BIN}" -sS -X GET "${url}" -H "Accept: application/json" "${AUTH_HEADER_ARGS[@]}"
}

http_status() {
  local url="$1"
  build_auth_headers
  "${CURL_BIN}" -sS -o /dev/null -w '%{http_code}' -X GET "${url}" -H "Accept: application/json" "${AUTH_HEADER_ARGS[@]}"
}

prom_sum_cache_ops() {
  local metrics_file="$1"
  local result="$2"
  awk -v target_result="${result}" '
    /^rca_cache_operation_total\{/ {
      if ($0 ~ /op="get"/ && $0 ~ ("result=\"" target_result "\"") &&
          $0 ~ /module="inbox"|module="workbench"|module="dashboard"|module="history"|module="trace"|module="compare"/) {
        sum += $NF
      }
    }
    END { printf "%.6f", sum + 0 }
  ' "${metrics_file}"
}

prom_pick_db_query_metric() {
  local metrics_file="$1"
  awk '
    /^[a-zA-Z_:][a-zA-Z0-9_:]*\{/ {
      name = $1
      sub(/\{.*/, "", name)
      if (name ~ /(db_query|gorm).*_total/) {
        print name
        exit 0
      }
    }
    /^[a-zA-Z_:][a-zA-Z0-9_:]* [0-9.eE+-]+$/ {
      name = $1
      if (name ~ /(db_query|gorm).*_total/) {
        print name
        exit 0
      }
    }
  ' "${metrics_file}"
}

prom_sum_metric() {
  local metrics_file="$1"
  local metric_name="$2"
  awk -v target="${metric_name}" '
    $1 ~ ("^" target "(\\{|$)") { sum += $NF }
    END { printf "%.6f", sum + 0 }
  ' "${metrics_file}"
}

run_request_once() {
  local url="$1"
  local header=()
  if [[ -n "${OPERATOR_TOKEN}" ]]; then
    header=(-H "Authorization: Bearer ${OPERATOR_TOKEN}")
  fi
  local out
  if ! out="$(${CURL_BIN} -sS -o /dev/null -w '%{http_code} %{time_total}' -H 'Accept: application/json' "${header[@]}" "${url}" 2>/dev/null)"; then
    echo "000 0"
    return 0
  fi
  echo "${out}"
}

export -f run_request_once
export CURL_BIN OPERATOR_TOKEN

require_bin "${CURL_BIN}"
require_bin "${JQ_BIN}"

if [[ "$(http_status "${BASE_URL}/healthz")" != "200" ]]; then
  echo "health check failed: ${BASE_URL}/healthz" >&2
  exit 1
fi

inbox_json="$(json_get "${BASE_URL}/v1/operator/inbox?offset=0&limit=5")"
if [[ -z "${SESSION_ID}" ]]; then
  SESSION_ID="$(echo "${inbox_json}" | ${JQ_BIN} -r '.sessions[0].session_id // .sessions[0].sessionID // .items[0].session_id // .items[0].sessionID // empty')"
fi

if [[ -z "${SESSION_ID}" ]]; then
  echo "unable to resolve SESSION_ID, provide SESSION_ID env" >&2
  exit 1
fi

workbench_json="$(json_get "${BASE_URL}/v1/sessions/${SESSION_ID}/workbench?limit=10")"
if [[ -z "${JOB_ID}" ]]; then
  JOB_ID="$(echo "${workbench_json}" | ${JQ_BIN} -r '.latest_run.job_id // .latest_run.jobID // .latest_decision.job_id // .latest_decision.jobID // .session.latest_job_id // empty')"
fi
if [[ -z "${LEFT_JOB_ID}" ]]; then
  LEFT_JOB_ID="$(echo "${workbench_json}" | ${JQ_BIN} -r '.recent_runs[0].job_id // .recent_runs[0].jobID // empty')"
fi
if [[ -z "${RIGHT_JOB_ID}" ]]; then
  RIGHT_JOB_ID="$(echo "${workbench_json}" | ${JQ_BIN} -r '.recent_runs[1].job_id // .recent_runs[1].jobID // empty')"
fi

if [[ -z "${JOB_ID}" ]]; then
  echo "unable to resolve JOB_ID, provide JOB_ID env" >&2
  exit 1
fi
if [[ -z "${LEFT_JOB_ID}" ]]; then
  LEFT_JOB_ID="${JOB_ID}"
fi
if [[ -z "${RIGHT_JOB_ID}" ]]; then
  RIGHT_JOB_ID="${JOB_ID}"
fi

metrics_before="$(mktemp)"
metrics_after="$(mktemp)"
request_plan="$(mktemp)"
request_result="$(mktemp)"
cleanup() {
  rm -f "${metrics_before}" "${metrics_after}" "${request_plan}" "${request_result}"
}
trap cleanup EXIT

build_auth_headers
${CURL_BIN} -sS -X GET "${BASE_URL}/metrics" -H "Accept: text/plain" "${AUTH_HEADER_ARGS[@]}" > "${metrics_before}"

endpoints=(
  "${BASE_URL}/v1/operator/inbox?offset=0&limit=10"
  "${BASE_URL}/v1/sessions/${SESSION_ID}/workbench?limit=10"
  "${BASE_URL}/v1/operator/dashboard"
  "${BASE_URL}/v1/sessions/${SESSION_ID}/history?offset=0&limit=10&order=desc"
  "${BASE_URL}/v1/ai/jobs/${JOB_ID}/trace"
  "${BASE_URL}/v1/ai/jobs:trace-compare?left_job_id=${LEFT_JOB_ID}&right_job_id=${RIGHT_JOB_ID}"
)

endpoint_count="${#endpoints[@]}"
total_requests=$((CONCURRENCY * REQUESTS_PER_WORKER))
for ((i=0; i<total_requests; i++)); do
  idx=$((i % endpoint_count))
  echo "${endpoints[idx]}" >> "${request_plan}"
done

start_ts="$(date +%s)"
cat "${request_plan}" | xargs -I{} -P "${CONCURRENCY}" bash -c 'run_request_once "$@"' _ {} > "${request_result}"
end_ts="$(date +%s)"

build_auth_headers
${CURL_BIN} -sS -X GET "${BASE_URL}/metrics" -H "Accept: text/plain" "${AUTH_HEADER_ARGS[@]}" > "${metrics_after}"

ok_count="$(awk '$1 ~ /^2/ {ok++} END {print ok+0}' "${request_result}")"
err_count="$(awk '$1 !~ /^2/ {err++} END {print err+0}' "${request_result}")"
avg_latency="$(awk '{sum+=$2; cnt++} END {if (cnt==0) print 0; else printf "%.6f", sum/cnt}' "${request_result}")"
max_latency="$(awk 'BEGIN{max=0} {if ($2>max) max=$2} END {printf "%.6f", max}' "${request_result}")"

hit_before="$(prom_sum_cache_ops "${metrics_before}" "hit")"
hit_after="$(prom_sum_cache_ops "${metrics_after}" "hit")"
miss_before="$(prom_sum_cache_ops "${metrics_before}" "miss")"
miss_after="$(prom_sum_cache_ops "${metrics_after}" "miss")"

hit_delta="$(awk -v a="${hit_after}" -v b="${hit_before}" 'BEGIN{printf "%.0f", a-b}')"
miss_delta="$(awk -v a="${miss_after}" -v b="${miss_before}" 'BEGIN{printf "%.0f", a-b}')"
hitrate="$(awk -v h="${hit_delta}" -v m="${miss_delta}" 'BEGIN{den=h+m; if (den<=0) printf "0.00"; else printf "%.2f", (h*100.0)/den}')"

db_metric_name="$(prom_pick_db_query_metric "${metrics_after}")"
db_query_delta="N/A"
if [[ -n "${db_metric_name}" ]]; then
  db_before="$(prom_sum_metric "${metrics_before}" "${db_metric_name}")"
  db_after="$(prom_sum_metric "${metrics_after}" "${db_metric_name}")"
  db_query_delta="$(awk -v a="${db_after}" -v b="${db_before}" 'BEGIN{printf "%.0f", a-b}')"
fi

wall_seconds=$((end_ts - start_ts))
if (( wall_seconds <= 0 )); then
  wall_seconds=1
fi
qps="$(awk -v n="${total_requests}" -v s="${wall_seconds}" 'BEGIN{printf "%.2f", n/s}')"

cat <<REPORT
== RCA Cache High-Concurrency Profile ==
base_url=${BASE_URL}
concurrency=${CONCURRENCY}
requests_per_worker=${REQUESTS_PER_WORKER}
total_requests=${total_requests}
wall_seconds=${wall_seconds}
qps=${qps}

session_id=${SESSION_ID}
job_id=${JOB_ID}
left_job_id=${LEFT_JOB_ID}
right_job_id=${RIGHT_JOB_ID}

status_ok=${ok_count}
status_error=${err_count}
avg_latency_seconds=${avg_latency}
max_latency_seconds=${max_latency}

cache_hit_delta=${hit_delta}
cache_miss_delta=${miss_delta}
cache_hit_rate_percent=${hitrate}

db_query_metric=${db_metric_name:-N/A}
db_query_delta=${db_query_delta}

note=if db_query_metric is N/A, expose a db query counter metric to enable direct DB reduction measurement.
REPORT
