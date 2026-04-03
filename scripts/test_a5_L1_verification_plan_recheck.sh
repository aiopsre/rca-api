#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
DEBUG="${DEBUG:-0}"
JOB_WAIT_TIMEOUT_SEC="${JOB_WAIT_TIMEOUT_SEC:-120}"
JOB_POLL_INTERVAL_SEC="${JOB_POLL_INTERVAL_SEC:-1}"
MOCK_PROM_PORT="${MOCK_PROM_PORT:-19095}"

LAST_HTTP_CODE=""
LAST_BODY=""

INCIDENT_ID=""
JOB_ID=""
TOOL_CALL_ID=""
DATASOURCE_ID=""

STEP_TOOL=""
STEP_PARAMS_JSON=""
PLAN_JSON=""
OBSERVED="unknown"
MEETS_EXPECTATION="unknown"

MOCK_PROM_PID=""
MOCK_PROM_FILE=""

need_cmd() {
	command -v "$1" >/dev/null 2>&1
}

debug() {
	if [[ "${DEBUG}" == "1" ]]; then
		echo "[DEBUG] $*" >&2
	fi
}

truncate_2kb() {
	printf '%s' "$1" | head -c 2048
}

cleanup() {
	if [[ -n "${MOCK_PROM_PID}" ]]; then
		kill "${MOCK_PROM_PID}" >/dev/null 2>&1 || true
		wait "${MOCK_PROM_PID}" >/dev/null 2>&1 || true
	fi
	if [[ -n "${MOCK_PROM_FILE}" ]]; then
		rm -f "${MOCK_PROM_FILE}" || true
	fi
}
trap cleanup EXIT

fail_step() {
	local step="$1"
	local code="${2:-${LAST_HTTP_CODE}}"
	local body="${3:-${LAST_BODY}}"

	echo "FAIL A5-L1 step=${step}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "incident_id=${INCIDENT_ID:-NONE}"
	echo "job_id=${JOB_ID:-NONE}"
	echo "tool_call_id=${TOOL_CALL_ID:-NONE}"
	exit 1
}

assert_no_sensitive() {
	local step="$1"
	local body="${2:-${LAST_BODY}}"
	if printf '%s' "${body}" | grep -Eiq '("secret"[[:space:]]*:|\\\"secret\\\"[[:space:]]*:|"authorization"[[:space:]]*:|\\\"authorization\\\"[[:space:]]*:|"Authorization"[[:space:]]*:|\\\"Authorization\\\"[[:space:]]*:|"token"[[:space:]]*:|\\\"token\\\"[[:space:]]*:|"headers"[[:space:]]*:|\\\"headers\\\"[[:space:]]*:)'; then
		fail_step "${step}.SensitiveLeak" "${LAST_HTTP_CODE}" "${body}"
	fi
}

extract_field() {
	local json="$1"
	shift
	local keys=("$@")
	local key value

	if need_cmd jq; then
		for key in "${keys[@]}"; do
			value="$(
				printf '%s' "${json}" | jq -r --arg k "${key}" '
					(.[$k] // .data[$k] // .job[$k] // .data.job[$k] //
					 .incident[$k] // .data.incident[$k] // .datasource[$k] // .data.datasource[$k]) |
					if . == null then empty
					elif type == "string" then .
					else tojson
					end
				' 2>/dev/null
			)"
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

	local tmp_body tmp_err code rc curl_err
	tmp_body="$(mktemp)"
	tmp_err="$(mktemp)"

	local -a cmd
	cmd=("${CURL}" -sS -o "${tmp_body}" -w "%{http_code}" -X "${method}" "${url}" -H "Accept: application/json")
	if [[ -n "${SCOPES}" ]]; then
		cmd+=(-H "X-Scopes: ${SCOPES}")
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

	if ! http_json "${method}" "${url}" "${body}"; then
		fail_step "${step}"
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		fail_step "${step}"
	fi
	debug "${step} code=${LAST_HTTP_CODE}"
}

wait_for_ai_job_terminal() {
	local deadline status now
	deadline="$(( $(date +%s) + JOB_WAIT_TIMEOUT_SEC ))"

	while true; do
		call_or_fail "PollAIJob" GET "${BASE_URL}/v1/ai/jobs/${JOB_ID}"
		status="$(extract_field "${LAST_BODY}" "status")" || true
		if [[ -z "${status}" ]]; then
			fail_step "PollAIJobStatusParse" "${LAST_HTTP_CODE}" "${LAST_BODY}"
		fi

		case "${status}" in
			queued|running)
				now="$(date +%s)"
				if (( now > deadline )); then
					fail_step "PollAIJobTimeout" "TIMEOUT" "${LAST_BODY}"
				fi
				sleep "${JOB_POLL_INTERVAL_SEC}"
				;;
			succeeded)
				return 0
				;;
			failed|canceled)
				fail_step "PollAIJobTerminal=${status}" "${LAST_HTTP_CODE}" "${LAST_BODY}"
				;;
			*)
				now="$(date +%s)"
				if (( now > deadline )); then
					fail_step "PollAIJobUnknownStatus=${status}" "TIMEOUT" "${LAST_BODY}"
				fi
				sleep "${JOB_POLL_INTERVAL_SEC}"
				;;
		esac
	done
}

start_mock_prometheus() {
	MOCK_PROM_FILE="$(mktemp).py"
	cat >"${MOCK_PROM_FILE}" <<'PYEOF'
import json
from http.server import BaseHTTPRequestHandler, HTTPServer

class Handler(BaseHTTPRequestHandler):
    def _write_json(self, code, payload):
        raw = json.dumps(payload).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self):
        if self.path.startswith("/api/v1/query_range"):
            self._write_json(200, {
                "status": "success",
                "data": {
                    "resultType": "matrix",
                    "result": [
                        {
                            "metric": {"__name__": "up"},
                            "values": [[1700000000, "1"]]
                        }
                    ]
                }
            })
            return
        self._write_json(404, {"error": "not found"})

    def log_message(self, fmt, *args):
        return

if __name__ == "__main__":
    server = HTTPServer(("127.0.0.1", int(__import__("os").environ.get("A5_MOCK_PROM_PORT", "19095"))), Handler)
    server.serve_forever()
PYEOF

	A5_MOCK_PROM_PORT="${MOCK_PROM_PORT}" python3 "${MOCK_PROM_FILE}" >/dev/null 2>&1 &
	MOCK_PROM_PID=$!

	local ready=0
	for _ in $(seq 1 30); do
		if curl -fsS "http://127.0.0.1:${MOCK_PROM_PORT}/api/v1/query_range?query=up&start=1&end=2&step=1" >/dev/null 2>&1; then
			ready=1
			break
		fi
		sleep 1
	done
	if [[ "${ready}" != "1" ]]; then
		fail_step "StartMockPrometheusTimeout" "TIMEOUT" "mock_prom_port=${MOCK_PROM_PORT}"
	fi
}

assert_verification_plan_in_incident() {
	local body="$1"
	if need_cmd jq; then
		local diagnosis_raw version step_count
		diagnosis_raw="$(printf '%s' "${body}" | jq -r '(.incident.diagnosisJSON // .incident.diagnosis_json // .diagnosisJSON // .diagnosis_json // empty)' 2>/dev/null || true)"
		if [[ -z "${diagnosis_raw}" ]]; then
			fail_step "GetIncident.DiagnosisMissing" "${LAST_HTTP_CODE}" "${body}"
		fi
		PLAN_JSON="$(printf '%s' "${diagnosis_raw}" | jq -c '.verification_plan // empty' 2>/dev/null || true)"
		if [[ -z "${PLAN_JSON}" ]]; then
			fail_step "GetIncident.VerificationPlanMissing" "${LAST_HTTP_CODE}" "${body}"
		fi
		version="$(printf '%s' "${PLAN_JSON}" | jq -r '.version // empty' 2>/dev/null || true)"
		step_count="$(printf '%s' "${PLAN_JSON}" | jq -r '(.steps // []) | length' 2>/dev/null || true)"
		if [[ "${version}" != "a5" ]]; then
			fail_step "GetIncident.VerificationVersionInvalid" "${LAST_HTTP_CODE}" "${body}"
		fi
		if [[ -z "${step_count}" ]] || (( step_count < 1 )); then
			fail_step "GetIncident.VerificationStepsInvalid" "${LAST_HTTP_CODE}" "${body}"
		fi
		assert_no_sensitive "GetIncident.VerificationPlanNoSensitive" "${PLAN_JSON}"
	else
		if ! printf '%s' "${body}" | grep -Eq '"verification_plan"'; then
			fail_step "GetIncident.VerificationPlanMissingNoJQ" "${LAST_HTTP_CODE}" "${body}"
		fi
	fi
}

assert_verification_plan_mirrored_to_toolcall() {
	local body="$1"
	if need_cmd jq; then
		local hit
		hit="$(printf '%s' "${body}" | jq -c '
			(.toolCalls // .data.toolCalls // [])
			| map(
				. as $tc
				| ((.responseJSON // .response_json // "") as $raw
					| if ($raw|type) == "string" and ($raw|length) > 0
					  then (try ($raw|fromjson) catch {})
					  else {}
					  end
				  ) as $resp
				| {
					tool_call_id: ($tc.toolCallID // $tc.tool_call_id // ""),
					version: ($resp.verification_plan.version // ""),
					step0_tool: ($resp.verification_plan.steps[0].tool // ""),
					step0_params: ($resp.verification_plan.steps[0].params // {})
				  }
			)
			| map(select(.version == "a5" and (.step0_tool | length) > 0))
			| .[0] // empty
		' 2>/dev/null || true)"
		if [[ -z "${hit}" ]]; then
			fail_step "ListToolCalls.VerificationPlanMirrorMissing" "${LAST_HTTP_CODE}" "${body}"
		fi
		TOOL_CALL_ID="$(printf '%s' "${hit}" | jq -r '.tool_call_id // empty' 2>/dev/null || true)"
		STEP_TOOL="$(printf '%s' "${hit}" | jq -r '.step0_tool // empty' 2>/dev/null || true)"
		STEP_PARAMS_JSON="$(printf '%s' "${hit}" | jq -c '.step0_params // {}' 2>/dev/null || true)"
		if [[ -z "${STEP_TOOL}" || -z "${STEP_PARAMS_JSON}" ]]; then
			fail_step "ListToolCalls.VerificationStepParse" "${LAST_HTTP_CODE}" "${body}"
		fi
	else
		if ! printf '%s' "${body}" | grep -Eq '"verification_plan"'; then
			fail_step "ListToolCalls.VerificationPlanMirrorMissingNoJQ" "${LAST_HTTP_CODE}" "${body}"
		fi
		STEP_TOOL="mcp.query_metrics"
		STEP_PARAMS_JSON="{}"
	fi
	if [[ -z "${TOOL_CALL_ID}" ]]; then
		TOOL_CALL_ID="NONE"
	fi
}

evaluate_expectation_best_effort() {
	local plan_json="$1"
	local mcp_resp="$2"
	if ! need_cmd jq; then
		MEETS_EXPECTATION="unknown"
		OBSERVED="jq_unavailable"
		return 0
	fi

	local expected_type keyword threshold row_count result_json_lc
	expected_type="$(printf '%s' "${plan_json}" | jq -r '.steps[0].expected.type // "exists"' 2>/dev/null || true)"
	keyword="$(printf '%s' "${plan_json}" | jq -r '.steps[0].expected.keyword // empty' 2>/dev/null || true)"
	threshold="$(printf '%s' "${plan_json}" | jq -r '.steps[0].expected.value // empty' 2>/dev/null || true)"
	row_count="$(printf '%s' "${mcp_resp}" | jq -r '.output.rowCount // -1' 2>/dev/null || true)"
	result_json_lc="$(printf '%s' "${mcp_resp}" | jq -r '(.output.queryResultJSON // "" | ascii_downcase)' 2>/dev/null || true)"

	OBSERVED="row_count=${row_count}"
	MEETS_EXPECTATION="unknown"

	case "${expected_type}" in
		exists)
			if [[ "${row_count}" =~ ^[0-9]+$ ]] && (( row_count > 0 )); then
				MEETS_EXPECTATION="true"
			elif [[ -n "${result_json_lc}" && "${result_json_lc}" != "null" ]]; then
				MEETS_EXPECTATION="true"
			else
				MEETS_EXPECTATION="false"
			fi
			;;
		contains_keyword)
			if [[ -n "${keyword}" ]] && printf '%s' "${result_json_lc}" | grep -Fqi "${keyword}"; then
				MEETS_EXPECTATION="true"
			else
				MEETS_EXPECTATION="false"
			fi
			;;
		threshold_above)
			if [[ "${row_count}" =~ ^[0-9]+$ ]] && [[ "${threshold}" =~ ^[0-9]+([.][0-9]+)?$ ]]; then
				if awk "BEGIN {exit !(${row_count} > ${threshold})}"; then
					MEETS_EXPECTATION="true"
				else
					MEETS_EXPECTATION="false"
				fi
			fi
			;;
		threshold_below)
			if [[ "${row_count}" =~ ^[0-9]+$ ]] && [[ "${threshold}" =~ ^[0-9]+([.][0-9]+)?$ ]]; then
				if awk "BEGIN {exit !(${row_count} < ${threshold})}"; then
					MEETS_EXPECTATION="true"
				else
					MEETS_EXPECTATION="false"
				fi
			fi
			;;
	esac
}

start_mock_prometheus

rand="${RAND:-$RANDOM}"
now_epoch="$(date -u +%s)"
start_epoch="$((now_epoch - 1800))"
namespace="a5-l1-ns-${rand}"
service="a5-l1-svc"
fingerprint="a5-l1-fp-${rand}"

create_ds_body=$(cat <<EOF
{"type":"prometheus","name":"a5-mock-prom-${rand}","baseURL":"http://127.0.0.1:${MOCK_PROM_PORT}","authType":"none","timeoutMs":3000,"isEnabled":true}
EOF
)
call_or_fail "CreateDatasource" POST "${BASE_URL}/v1/datasources" "${create_ds_body}"
DATASOURCE_ID="$(extract_field "${LAST_BODY}" "datasourceID" "datasource_id")" || true
if [[ -z "${DATASOURCE_ID}" ]]; then
	fail_step "CreateDatasource.ParseDatasourceID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

ingest_body=$(cat <<EOF
{"idempotencyKey":"idem-a5-l1-ingest-${rand}","fingerprint":"${fingerprint}","status":"firing","severity":"P1","service":"${service}","cluster":"prod-a5","namespace":"${namespace}","workload":"a5-l1-workload","lastSeenAt":{"seconds":${now_epoch},"nanos":0},"labelsJSON":"{\"alertname\":\"A5Verification\",\"service\":\"${service}\"}"}
EOF
)
call_or_fail "IngestAlertEvent" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_body}"
INCIDENT_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id")" || true
if [[ -z "${INCIDENT_ID}" ]]; then
	fail_step "IngestAlertEvent.ParseIncidentID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

run_body=$(cat <<EOF
{"incidentID":"${INCIDENT_ID}","idempotencyKey":"idem-a5-l1-ai-run-${rand}","pipeline":"basic_rca","trigger":"manual","timeRangeStart":{"seconds":${start_epoch},"nanos":0},"timeRangeEnd":{"seconds":${now_epoch},"nanos":0},"inputHintsJSON":"{\"scenario\":\"A5_L1_VERIFICATION_PLAN\"}","createdBy":"system"}
EOF
)
call_or_fail "RunAIJob" POST "${BASE_URL}/v1/incidents/${INCIDENT_ID}/ai:run" "${run_body}"
JOB_ID="$(extract_field "${LAST_BODY}" "jobID" "job_id")" || true
if [[ -z "${JOB_ID}" ]]; then
	fail_step "RunAIJob.ParseJobID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

wait_for_ai_job_terminal

call_or_fail "GetIncident" GET "${BASE_URL}/v1/incidents/${INCIDENT_ID}"
assert_verification_plan_in_incident "${LAST_BODY}"

call_or_fail "ListToolCalls" GET "${BASE_URL}/v1/ai/jobs/${JOB_ID}/tool-calls?offset=0&limit=50"
assert_verification_plan_mirrored_to_toolcall "${LAST_BODY}"
assert_no_sensitive "ListToolCalls.NoSensitive" "${LAST_BODY}"

if need_cmd jq; then
	mcp_call_body="$(jq -cn --arg tool "${STEP_TOOL}" --argjson input "${STEP_PARAMS_JSON}" '{tool:$tool,input:$input}')"
else
	mcp_call_body="{\"tool\":\"${STEP_TOOL}\",\"input\":${STEP_PARAMS_JSON}}"
fi

call_or_fail "MCPToolRecheck" POST "${BASE_URL}/v1/mcp/tools/call" "${mcp_call_body}"
assert_no_sensitive "MCPToolRecheck.NoSensitive" "${LAST_BODY}"
evaluate_expectation_best_effort "${PLAN_JSON}" "${LAST_BODY}"

echo "PASS A5-L1"
echo "incident_id=${INCIDENT_ID}"
echo "job_id=${JOB_ID}"
echo "tool_call_id=${TOOL_CALL_ID}"
echo "verification_plan_version=a5"
echo "observed=${OBSERVED}"
echo "meets_expectation=${MEETS_EXPECTATION}"
