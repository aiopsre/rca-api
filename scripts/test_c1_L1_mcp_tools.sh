#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
DEBUG="${DEBUG:-0}"
TRUNC_ALERT_COUNT="${TRUNC_ALERT_COUNT:-20}"
CURL_CONNECT_TIMEOUT="${CURL_CONNECT_TIMEOUT:-5}"
CURL_MAX_TIME="${CURL_MAX_TIME:-30}"
SEED_PROGRESS_EVERY="${SEED_PROGRESS_EVERY:-10}"
MCP_TOOLS_VERSION="${MCP_TOOLS_VERSION:-c1}"
MOCK_DS_HOST="${MOCK_DS_HOST:-127.0.0.1}"
MOCK_DS_PORT="${MOCK_DS_PORT:-19091}"
MOCK_DS_BASE_URL="${MOCK_DS_BASE_URL:-http://${MOCK_DS_HOST}:${MOCK_DS_PORT}}"

LAST_HTTP_CODE=""
LAST_BODY=""

INCIDENT_ID=""
TOOL_CALL_ID=""
DATASOURCE_METRICS_ID=""
DATASOURCE_LOGS_ID=""
MOCK_DS_PID=""
MOCK_DS_SCRIPT=""

need_cmd() {
	command -v "$1" >/dev/null 2>&1
}

debug() {
	if [[ "${DEBUG}" == "1" ]]; then
		echo "[DEBUG] $*" >&2
	fi
}

info() {
	echo "[INFO] $*" >&2
}

cleanup() {
	if [[ -n "${MOCK_DS_PID}" ]]; then
		kill "${MOCK_DS_PID}" >/dev/null 2>&1 || true
		wait "${MOCK_DS_PID}" 2>/dev/null || true
		MOCK_DS_PID=""
	fi
	if [[ -n "${MOCK_DS_SCRIPT}" ]]; then
		rm -f "${MOCK_DS_SCRIPT}" >/dev/null 2>&1 || true
		MOCK_DS_SCRIPT=""
	fi
}

trap cleanup EXIT

truncate_2kb() {
	printf '%s' "$1" | head -c 2048
}

fail_step() {
	local step="$1"
	local code="${2:-${LAST_HTTP_CODE}}"
	local body="${3:-${LAST_BODY}}"

	echo "FAIL C1 step=${step}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "incident_id=${INCIDENT_ID:-NONE}"
	echo "tool_call_id=${TOOL_CALL_ID:-NONE}"
	echo "datasource_metrics_id=${DATASOURCE_METRICS_ID:-NONE}"
	echo "datasource_logs_id=${DATASOURCE_LOGS_ID:-NONE}"
	exit 1
}

extract_field() {
	local json="$1"
	shift
	local keys=("$@")
	local key value

	if need_cmd jq; then
		for key in "${keys[@]}"; do
			value="$({
				printf '%s' "${json}" | jq -r --arg k "${key}" '
					(.[$k] // .data[$k] // .output[$k] // .error[$k] // .details[$k]) |
					if . == null then empty
					elif type == "string" then .
					else tojson
					end
				' 2>/dev/null
			} || true)"
			if [[ -n "${value}" ]]; then
				printf '%s' "${value}"
				return 0
			fi
		done
	else
		for key in "${keys[@]}"; do
			value="$(printf '%s' "${json}" | sed -n "s/.*\"${key}\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" | head -n 1)"
			if [[ -n "${value}" ]]; then
				printf '%s' "${value}"
				return 0
			fi
		done
	fi
	return 1
}

http_json() {
	local method="$1"
	local url="$2"
	local body="${3:-}"
	local scopes="${4:-${SCOPES}}"

	local tmp_body tmp_err code rc curl_err
	tmp_body="$(mktemp)"
	tmp_err="$(mktemp)"

	local -a cmd
	cmd=(
		"${CURL}"
		-sS
		--connect-timeout "${CURL_CONNECT_TIMEOUT}"
		--max-time "${CURL_MAX_TIME}"
		-o "${tmp_body}"
		-w "%{http_code}"
		-X "${method}"
		"${url}"
		-H "Accept: application/json"
	)
	if [[ -n "${scopes}" ]]; then
		cmd+=(-H "X-Scopes: ${scopes}")
	fi
	if [[ -n "${body}" ]]; then
		cmd+=(-H "Content-Type: application/json" -d "${body}")
	fi

	set +e
	code="$("${cmd[@]}" 2>"${tmp_err}")"
	rc=$?
	set -e

	LAST_BODY="$(cat "${tmp_body}")"
	curl_err="$(cat "${tmp_err}")"
	rm -f "${tmp_body}" "${tmp_err}"

	if (( rc != 0 )); then
		LAST_HTTP_CODE="CURL_${rc}"
		if [[ -n "${curl_err}" ]]; then
			if [[ -n "${LAST_BODY}" ]]; then
				LAST_BODY="${LAST_BODY}"$'\n'"${curl_err}"
			else
				LAST_BODY="${curl_err}"
			fi
		fi
		return 1
	fi

	LAST_HTTP_CODE="${code}"
	if [[ -n "${curl_err}" ]]; then
		debug "curl stderr: ${curl_err}"
	fi
	return 0
}

call_or_fail() {
	local step="$1"
	local method="$2"
	local url="$3"
	local body="${4:-}"
	local scopes="${5:-${SCOPES}}"

	if ! http_json "${method}" "${url}" "${body}" "${scopes}"; then
		fail_step "${step}"
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		fail_step "${step}"
	fi
	debug "${step} code=${LAST_HTTP_CODE}"
}

assert_error_shape_or_fail() {
	local step="$1"
	local expect_code="$2"
	local expect_http="$3"

	if [[ "${LAST_HTTP_CODE}" != "${expect_http}" ]]; then
		fail_step "${step}.HTTP" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi

	if need_cmd jq; then
		local code details_step
		code="$(printf '%s' "${LAST_BODY}" | jq -r '.error.code // empty' 2>/dev/null || true)"
		details_step="$(printf '%s' "${LAST_BODY}" | jq -r '.error.details.step // empty' 2>/dev/null || true)"
		if [[ "${code}" != "${expect_code}" ]]; then
			fail_step "${step}.ErrorCode" "${LAST_HTTP_CODE}" "${LAST_BODY}"
		fi
		if [[ "${details_step}" != "mcp.call" ]]; then
			fail_step "${step}.ErrorStep" "${LAST_HTTP_CODE}" "${LAST_BODY}"
		fi
	else
		if [[ "${LAST_BODY}" != *"\"code\":\"${expect_code}\""* ]]; then
			fail_step "${step}.ErrorCode" "${LAST_HTTP_CODE}" "${LAST_BODY}"
		fi
		if [[ "${LAST_BODY}" != *"\"step\":\"mcp.call\""* ]]; then
			fail_step "${step}.ErrorStep" "${LAST_HTTP_CODE}" "${LAST_BODY}"
		fi
	fi
}

assert_no_sensitive() {
	local step="$1"
	local body="${2:-${LAST_BODY}}"

	if printf '%s' "${body}" | grep -Eiq '("secret"|\\\"secret\\\"|"authorization"|\\\"authorization\\\"|"Authorization"|\\\"Authorization\\\"|"token"|\\\"token\\\"|"headers"|\\\"headers\\\")'; then
		fail_step "${step}.SensitiveLeak" "${LAST_HTTP_CODE}" "${body}"
	fi
}

start_mock_datasource() {
	if ! need_cmd python3; then
		fail_step "MockDatasource.MissingPython3" "PRECHECK" "python3 command not found"
	fi

	MOCK_DS_SCRIPT="$(mktemp)"
	cat >"${MOCK_DS_SCRIPT}" <<'PY'
import json
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer


HOST = sys.argv[1]
PORT = int(sys.argv[2])


class Handler(BaseHTTPRequestHandler):
    def _write(self, status, payload):
        raw = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self):
        if self.path.startswith("/healthz"):
            self._write(200, {"ok": True})
            return
        if self.path.startswith("/api/v1/query_range"):
            self._write(
                200,
                {
                    "status": "success",
                    "data": {
                        "resultType": "matrix",
                        "result": [
                            {
                                "metric": {"__name__": "up", "job": "mock"},
                                "values": [["1700000000", "1"]],
                            }
                        ],
                    },
                },
            )
            return
        if self.path.startswith("/loki/api/v1/query_range"):
            self._write(
                200,
                {
                    "status": "success",
                    "data": {
                        "resultType": "streams",
                        "result": [
                            {
                                "stream": {"app": "mock"},
                                "values": [["1700000000000000000", "mock log line"]],
                            }
                        ],
                    },
                },
            )
            return
        self._write(404, {"error": "not_found"})

    def log_message(self, *args):
        return


if __name__ == "__main__":
    HTTPServer((HOST, PORT), Handler).serve_forever()
PY

	python3 "${MOCK_DS_SCRIPT}" "${MOCK_DS_HOST}" "${MOCK_DS_PORT}" >/dev/null 2>&1 &
	MOCK_DS_PID=$!

	local i
	for i in $(seq 1 30); do
		if http_json GET "${MOCK_DS_BASE_URL}/healthz" "" ""; then
			if [[ "${LAST_HTTP_CODE}" == "200" ]]; then
				return 0
			fi
		fi
		sleep 0.1
	done

	fail_step "MockDatasource.StartTimeout" "${LAST_HTTP_CODE}" "${LAST_BODY}"
}

rand="${RAND:-$RANDOM}"
now_epoch="$(date -u +%s)"
start_epoch="$((now_epoch - 1800))"

call_or_fail "MCPListTools" GET "${BASE_URL}/v1/mcp/tools" "" "${SCOPES}"
assert_no_sensitive "MCPListTools.NoSensitive" "${LAST_BODY}"
if need_cmd jq; then
	tools_version="$(printf '%s' "${LAST_BODY}" | jq -r '.version // empty' 2>/dev/null || true)"
	if [[ "${tools_version}" != "${MCP_TOOLS_VERSION}" ]]; then
		fail_step "MCPListTools.Version" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
else
	if [[ "${LAST_BODY}" != *'"version":"'"${MCP_TOOLS_VERSION}"'"'* ]] && [[ "${LAST_BODY}" != *'"version": "'"${MCP_TOOLS_VERSION}"'"'* ]]; then
		fail_step "MCPListTools.Version" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
fi

start_mock_datasource

fingerprint="c1-l1-fp-${rand}"
ingest_body=$(cat <<JSON
{"idempotencyKey":"idem-c1-l1-${rand}","fingerprint":"${fingerprint}","status":"firing","severity":"P1","service":"c1-l1-svc","cluster":"prod-c1","namespace":"default","workload":"demo-c1","lastSeenAt":{"seconds":${now_epoch},"nanos":0},"labelsJSON":"{\"alertname\":\"C1MCP\"}"}
JSON
)

call_or_fail "IngestAlertEvent" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_body}" "${SCOPES}"
INCIDENT_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id")" || true
if [[ -z "${INCIDENT_ID}" ]]; then
	fail_step "IngestAlertEvent.ParseIncidentID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

allow_body=$(cat <<JSON
{"tool":"get_incident","input":{"incident_id":"${INCIDENT_ID}"},"idempotency_key":"mcp-allow-${rand}"}
JSON
)
call_or_fail "MCPAllowGetIncident" POST "${BASE_URL}/v1/mcp/tools/call" "${allow_body}" "incident.read"
assert_no_sensitive "MCPAllowGetIncident.NoSensitive" "${LAST_BODY}"
TOOL_CALL_ID="$(extract_field "${LAST_BODY}" "tool_call_id" "toolCallID")" || true
if [[ -z "${TOOL_CALL_ID}" ]]; then
	fail_step "MCPAllowGetIncident.ParseToolCallID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

if need_cmd jq; then
	returned_incident_id="$(printf '%s' "${LAST_BODY}" | jq -r '.output.incidentID // empty' 2>/dev/null || true)"
	if [[ "${returned_incident_id}" != "${INCIDENT_ID}" ]]; then
		fail_step "MCPAllowGetIncident.AssertIncidentID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
fi

call_or_fail "MCPAllowAuditListToolCalls" GET "${BASE_URL}/v1/ai/jobs/mcp-readonly/tool-calls?offset=0&limit=200" "" "ai.read"
assert_no_sensitive "MCPAllowAuditListToolCalls.NoSensitive" "${LAST_BODY}"
if need_cmd jq; then
	match_count="$(printf '%s' "${LAST_BODY}" | jq -r --arg tc "${TOOL_CALL_ID}" '
		(.toolCalls // .data.toolCalls // [])
		| map(select((.toolCallID // .tool_call_id // "") == $tc and (.toolName // .tool_name // "") == "mcp.get_incident"))
		| length
	' 2>/dev/null || true)"
	if [[ -z "${match_count}" ]] || (( match_count < 1 )); then
		fail_step "MCPAllowAudit.AssertToolCallStored" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
else
	if [[ "${LAST_BODY}" != *"${TOOL_CALL_ID}"* ]] || [[ "${LAST_BODY}" != *"mcp.get_incident"* ]]; then
		fail_step "MCPAllowAudit.AssertToolCallStored" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
fi

if ! http_json POST "${BASE_URL}/v1/mcp/tools/call" "${allow_body}" "evidence.read"; then
	fail_step "MCPDenyScope.Call"
fi
assert_error_shape_or_fail "MCPDenyScope" "SCOPE_DENIED" "403"

metrics_ds_body=$(cat <<JSON
{"type":"prometheus","name":"c1-metrics-${rand}","baseURL":"${MOCK_DS_BASE_URL}","authType":"none","timeoutMs":2000,"isEnabled":true}
JSON
)
call_or_fail "CreateMetricsDatasource" POST "${BASE_URL}/v1/datasources" "${metrics_ds_body}" "datasource.admin"
DATASOURCE_METRICS_ID="$(extract_field "${LAST_BODY}" "datasourceID" "datasource_id")" || true
if [[ -z "${DATASOURCE_METRICS_ID}" ]]; then
	fail_step "CreateMetricsDatasource.ParseID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

logs_ds_body=$(cat <<JSON
{"type":"loki","name":"c1-logs-${rand}","baseURL":"${MOCK_DS_BASE_URL}","authType":"none","timeoutMs":2000,"isEnabled":true}
JSON
)
call_or_fail "CreateLogsDatasource" POST "${BASE_URL}/v1/datasources" "${logs_ds_body}" "datasource.admin"
DATASOURCE_LOGS_ID="$(extract_field "${LAST_BODY}" "datasourceID" "datasource_id")" || true
if [[ -z "${DATASOURCE_LOGS_ID}" ]]; then
	fail_step "CreateLogsDatasource.ParseID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

metrics_success_body=$(cat <<JSON
{"tool":"query_metrics","input":{"datasource_id":"${DATASOURCE_METRICS_ID}","expr":"up","time_range_start":{"seconds":$((now_epoch - 300)),"nanos":0},"time_range_end":{"seconds":${now_epoch},"nanos":0},"step_seconds":30}}
JSON
)
call_or_fail "MCPSuccessQueryMetrics" POST "${BASE_URL}/v1/mcp/tools/call" "${metrics_success_body}" "evidence.query"
assert_no_sensitive "MCPSuccessQueryMetrics.NoSensitive" "${LAST_BODY}"
if need_cmd jq; then
	metrics_result="$(printf '%s' "${LAST_BODY}" | jq -r '.output.queryResultJSON // empty' 2>/dev/null || true)"
	if [[ -z "${metrics_result}" ]]; then
		fail_step "MCPSuccessQueryMetrics.Result" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
fi

metrics_guardrail_body=$(cat <<JSON
{"tool":"query_metrics","input":{"datasource_id":"${DATASOURCE_METRICS_ID}","expr":"up","time_range_start":{"seconds":$((now_epoch - 172800)),"nanos":0},"time_range_end":{"seconds":${now_epoch},"nanos":0},"step_seconds":30}}
JSON
)
if ! http_json POST "${BASE_URL}/v1/mcp/tools/call" "${metrics_guardrail_body}" "evidence.query"; then
	fail_step "MCPGuardrailMetrics.Call"
fi
if [[ "${LAST_HTTP_CODE}" != "400" && "${LAST_HTTP_CODE}" != "429" ]]; then
	fail_step "MCPGuardrailMetrics.HTTP" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi
if [[ "${LAST_HTTP_CODE}" == "400" ]]; then
	assert_error_shape_or_fail "MCPGuardrailMetrics" "INVALID_ARGUMENT" "400"
else
	assert_error_shape_or_fail "MCPGuardrailMetrics" "RATE_LIMITED" "429"
fi

logs_guardrail_body=$(cat <<JSON
{"tool":"query_logs","input":{"datasource_id":"${DATASOURCE_LOGS_ID}","query":"{app=\"demo\"}","time_range_start":{"seconds":${start_epoch},"nanos":0},"time_range_end":{"seconds":${now_epoch},"nanos":0},"limit":999}}
JSON
)
if ! http_json POST "${BASE_URL}/v1/mcp/tools/call" "${logs_guardrail_body}" "evidence.query"; then
	fail_step "MCPGuardrailLogs.Call"
fi
if [[ "${LAST_HTTP_CODE}" != "400" && "${LAST_HTTP_CODE}" != "429" ]]; then
	fail_step "MCPGuardrailLogs.HTTP" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi
if [[ "${LAST_HTTP_CODE}" == "400" ]]; then
	assert_error_shape_or_fail "MCPGuardrailLogs" "INVALID_ARGUMENT" "400"
else
	assert_error_shape_or_fail "MCPGuardrailLogs" "RATE_LIMITED" "429"
fi

long_suffix="$(printf 'x%.0s' $(seq 1 96))"
if (( TRUNC_ALERT_COUNT > 0 )); then
	info "MCPTruncationSeed total=${TRUNC_ALERT_COUNT} progress_every=${SEED_PROGRESS_EVERY}"
fi
for i in $(seq 1 "${TRUNC_ALERT_COUNT}"); do
	fp="c1-l1-trunc-${rand}-${i}"
	service="svc-${i}-${long_suffix}"
	trunc_ingest_body=$(cat <<JSON
{"fingerprint":"${fp}","status":"firing","severity":"P1","service":"${service}","cluster":"prod-c1","namespace":"default","workload":"demo-c1-${i}","lastSeenAt":{"seconds":$((now_epoch + i)),"nanos":0}}
JSON
)
	if ! http_json POST "${BASE_URL}/v1/alert-events:ingest" "${trunc_ingest_body}" "alert.ingest"; then
		fail_step "MCPTruncationSeed.Ingest.${i}"
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		fail_step "MCPTruncationSeed.Ingest.${i}" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi

	if (( i == 1 || i == TRUNC_ALERT_COUNT || (SEED_PROGRESS_EVERY > 0 && i % SEED_PROGRESS_EVERY == 0) )); then
		info "MCPTruncationSeed progress=${i}/${TRUNC_ALERT_COUNT}"
	fi
done

trunc_call_body='{"tool":"list_alert_events_current","input":{"namespace":"default","limit":200,"page":1}}'
call_or_fail "MCPTruncationCall" POST "${BASE_URL}/v1/mcp/tools/call" "${trunc_call_body}" "alert.read"

body_bytes="$(printf '%s' "${LAST_BODY}" | wc -c | tr -d '[:space:]')"
if [[ -z "${body_bytes}" ]] || (( body_bytes > 16384 )); then
	fail_step "MCPTruncation.BodySize" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

if need_cmd jq; then
	truncated_flag="$(printf '%s' "${LAST_BODY}" | jq -r '.truncated // false' 2>/dev/null || true)"
	warning_count="$(printf '%s' "${LAST_BODY}" | jq -r '[.warnings[]? | select(.=="TRUNCATED_OUTPUT")] | length' 2>/dev/null || true)"
	if [[ "${truncated_flag}" != "true" ]]; then
		fail_step "MCPTruncation.TruncatedFlag" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
	if [[ -z "${warning_count}" ]] || (( warning_count < 1 )); then
		fail_step "MCPTruncation.WarningFlag" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
else
	if [[ "${LAST_BODY}" != *'"truncated":true'* ]]; then
		fail_step "MCPTruncation.TruncatedFlag" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
	if [[ "${LAST_BODY}" != *'TRUNCATED_OUTPUT'* ]]; then
		fail_step "MCPTruncation.WarningFlag" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
fi

echo "PASS C1 allow incident_id=${INCIDENT_ID} tool_call_id=${TOOL_CALL_ID}"
echo "PASS C1 deny scope=incident.read"
echo "PASS C1 guardrails metrics_ds=${DATASOURCE_METRICS_ID} logs_ds=${DATASOURCE_LOGS_ID}"
echo "PASS C1 truncation body_bytes=${body_bytes}"
echo "PASS C1 mcp tools"
