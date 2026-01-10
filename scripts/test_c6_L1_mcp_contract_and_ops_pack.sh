#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
DENY_BASE_URL="${DENY_BASE_URL:-}"
NOT_FOUND_BASE_URL="${NOT_FOUND_BASE_URL:-}"
AUTO_BOOT_ISOLATION_SERVERS="${AUTO_BOOT_ISOLATION_SERVERS:-1}"
DENY_PORT="${DENY_PORT:-5566}"
NOT_FOUND_PORT="${NOT_FOUND_PORT:-5567}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
DEBUG="${DEBUG:-0}"
CURL_CONNECT_TIMEOUT="${CURL_CONNECT_TIMEOUT:-5}"
CURL_MAX_TIME="${CURL_MAX_TIME:-30}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
APISERVER_CONFIG="${APISERVER_CONFIG:-${REPO_ROOT}/configs/rca-apiserver.yaml}"
if [[ "${APISERVER_CONFIG}" != /* ]]; then
	APISERVER_CONFIG="${REPO_ROOT}/${APISERVER_CONFIG}"
fi

LAST_HTTP_CODE=""
LAST_BODY=""
LAST_HEADERS=""
LAST_REQUEST_ID=""

MCP_VERSION=""
REQUEST_ID=""
TOOL_CALL_ID=""
INCIDENT_ID=""
JOB_ID=""

DENY_PID=""
NOT_FOUND_PID=""

TMP_FILES=()

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
	if [[ -n "${DENY_PID}" ]] && kill -0 "${DENY_PID}" 2>/dev/null; then
		kill "${DENY_PID}" >/dev/null 2>&1 || true
		wait "${DENY_PID}" >/dev/null 2>&1 || true
	fi
	if [[ -n "${NOT_FOUND_PID}" ]] && kill -0 "${NOT_FOUND_PID}" 2>/dev/null; then
		kill "${NOT_FOUND_PID}" >/dev/null 2>&1 || true
		wait "${NOT_FOUND_PID}" >/dev/null 2>&1 || true
	fi
	for f in "${TMP_FILES[@]:-}"; do
		[[ -n "${f}" ]] && rm -f "${f}" >/dev/null 2>&1 || true
	done
}
trap cleanup EXIT

fail_step() {
	local step="$1"
	local code="${2:-${LAST_HTTP_CODE}}"
	local body="${3:-${LAST_BODY}}"

	echo "FAIL C6-L1 step=${step}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "request_id=${REQUEST_ID:-NONE}"
	echo "tool_call_id=${TOOL_CALL_ID:-NONE}"
	echo "incident_id=${INCIDENT_ID:-NONE}"
	echo "job_id=${JOB_ID:-NONE}"
	echo "mcp_version=${MCP_VERSION:-NONE}"
	exit 1
}

assert_no_sensitive() {
	local step="$1"
	local body="${2:-${LAST_BODY}}"
	if printf '%s' "${body}" | grep -Eiq '("secret"[[:space:]]*:|\\"secret\\"[[:space:]]*:|"authorization"[[:space:]]*:|\\"authorization\\"[[:space:]]*:|"Authorization"[[:space:]]*:|\\"Authorization\\"[[:space:]]*:|"token"[[:space:]]*:|\\"token\\"[[:space:]]*:|"headers"[[:space:]]*:|\\"headers\\"[[:space:]]*:)'; then
		fail_step "${step}.SensitiveLeak" "${LAST_HTTP_CODE}" "${body}"
	fi
}

parse_request_id_from_headers() {
	local headers_file="$1"
	local rid
	rid="$(awk 'BEGIN{IGNORECASE=1} /^X-Request-Id:/{gsub("\r", "", $2); print $2; exit}' "${headers_file}" || true)"
	if [[ -z "${rid}" ]]; then
		rid="$(awk 'BEGIN{IGNORECASE=1} /^X-Trace-Id:/{gsub("\r", "", $2); print $2; exit}' "${headers_file}" || true)"
	fi
	printf '%s' "${rid}"
}

http_json() {
	local method="$1"
	local url="$2"
	local body="${3:-}"
	local scopes="${4:-${SCOPES}}"
	shift 4 || true
	local extra_headers=("$@")

	local tmp_body tmp_headers tmp_err code rc curl_err
	tmp_body="$(mktemp)"
	tmp_headers="$(mktemp)"
	tmp_err="$(mktemp)"
	TMP_FILES+=("${tmp_body}" "${tmp_headers}" "${tmp_err}")

	local -a cmd
	cmd=(
		"${CURL}"
		-sS
		--connect-timeout "${CURL_CONNECT_TIMEOUT}"
		--max-time "${CURL_MAX_TIME}"
		-D "${tmp_headers}"
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
	for header in "${extra_headers[@]}"; do
		if [[ -n "${header}" ]]; then
			cmd+=(-H "${header}")
		fi
	done

	set +e
	code="$("${cmd[@]}" 2>"${tmp_err}")"
	rc=$?
	set -e

	LAST_BODY="$(cat "${tmp_body}")"
	LAST_HEADERS="$(cat "${tmp_headers}")"
	LAST_REQUEST_ID="$(parse_request_id_from_headers "${tmp_headers}")"
	curl_err="$(cat "${tmp_err}")"

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
	shift 5 || true
	local extra_headers=("$@")

	if ! http_json "${method}" "${url}" "${body}" "${scopes}" "${extra_headers[@]}"; then
		fail_step "${step}"
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		fail_step "${step}"
	fi
}

assert_error_code() {
	local step="$1"
	local expect_http="$2"
	local expect_code="$3"

	if [[ "${LAST_HTTP_CODE}" != "${expect_http}" ]]; then
		fail_step "${step}.HTTP" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
	local code details_step
	code="$(printf '%s' "${LAST_BODY}" | jq -r '.error.code // empty' 2>/dev/null || true)"
	details_step="$(printf '%s' "${LAST_BODY}" | jq -r '.error.details.step // empty' 2>/dev/null || true)"
	if [[ -z "${code}" ]]; then
		fail_step "${step}.MissingCode" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
	case "${code}" in
		SCOPE_DENIED|INVALID_ARGUMENT|NOT_FOUND|RATE_LIMITED|INTERNAL) ;;
		*) fail_step "${step}.Enum" "${LAST_HTTP_CODE}" "${LAST_BODY}" ;;
	esac
	if [[ "${code}" != "${expect_code}" ]]; then
		fail_step "${step}.Code" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
	if [[ "${details_step}" != "mcp.call" ]]; then
		fail_step "${step}.Step" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
}

write_temp_config() {
	local mode="$1"
	local port="$2"
	local out_file="$3"

	python3 - "${APISERVER_CONFIG}" "${out_file}" "${port}" "${mode}" <<'PY'
import re
import sys
from pathlib import Path

src = Path(sys.argv[1])
out = Path(sys.argv[2])
port = sys.argv[3]
mode = sys.argv[4]

lines = src.read_text(encoding="utf-8").splitlines()
result = []

in_http = False
in_mcp = False
in_mcp_isolation = False
http_addr_set = False
mcp_mode_set = False
mcp_exists = any(re.match(r"^mcp:\s*$", line.strip()) for line in lines)

for line in lines:
    stripped = line.strip()

    if re.match(r"^[^\s].*:\s*$", line):
        in_http = stripped == "http:"
        in_mcp = stripped == "mcp:"
        in_mcp_isolation = False

    if in_mcp and re.match(r"^  [^\s].*:\s*$", line):
        in_mcp_isolation = stripped == "isolation:"

    if in_http and re.match(r"^\s{2}addr:\s*", line):
        line = f"  addr: 127.0.0.1:{port}"
        http_addr_set = True

    if in_mcp and in_mcp_isolation and re.match(r"^\s{4}mode:\s*", line):
        line = f"    mode: {mode}"
        mcp_mode_set = True

    result.append(line)

if not http_addr_set:
    raise SystemExit("missing http.addr in config")

if not mcp_exists:
    result.extend([
        "",
        "mcp:",
        "  isolation:",
        f"    mode: {mode}",
    ])
elif not mcp_mode_set:
    result.extend([
        "  isolation:",
        f"    mode: {mode}",
    ])

out.write_text("\n".join(result) + "\n", encoding="utf-8")
PY
}

start_mode_server() {
	local mode="$1"
	local port="$2"
	local out_url_var="$3"
	local out_pid_var="$4"
	local out_cfg_var="$5"
	local out_log_var="$6"

	local cfg log pid
	cfg="$(mktemp).yaml"
	log="$(mktemp).log"
	TMP_FILES+=("${cfg}" "${log}")

	write_temp_config "${mode}" "${port}" "${cfg}"

	(
		cd "${REPO_ROOT}"
		env GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn go run ./cmd/rca-apiserver --config "${cfg}"
	) >"${log}" 2>&1 &
	pid=$!

	for i in $(seq 1 180); do
		if curl -fsS "http://127.0.0.1:${port}/healthz" >/dev/null 2>&1; then
			printf -v "${out_url_var}" 'http://127.0.0.1:%s' "${port}"
			printf -v "${out_pid_var}" '%s' "${pid}"
			printf -v "${out_cfg_var}" '%s' "${cfg}"
			printf -v "${out_log_var}" '%s' "${log}"
			debug "mode=${mode} apiserver ready at http://127.0.0.1:${port}"
			return 0
		fi
		if ! kill -0 "${pid}" 2>/dev/null; then
			LAST_HTTP_CODE="STARTUP_FAIL"
			LAST_BODY="$(tail -n 120 "${log}" || true)"
			fail_step "Start${mode}APIServer"
		fi
		sleep 1
	done

	LAST_HTTP_CODE="TIMEOUT"
	LAST_BODY="$(tail -n 120 "${log}" || true)"
	fail_step "Start${mode}APIServerTimeout"
}

if ! need_cmd jq; then
	fail_step "Precheck.MissingJQ" "PRECHECK" "jq command not found"
fi
if ! need_cmd "${CURL}"; then
	fail_step "Precheck.MissingCurl" "PRECHECK" "curl command not found"
fi

call_or_fail "Healthz" GET "${BASE_URL}/healthz" "" "${SCOPES}"

# 1) Contract: /v1/mcp/tools version + metadata.required_scopes/policy
call_or_fail "MCPListTools" GET "${BASE_URL}/v1/mcp/tools" "" "${SCOPES}"
assert_no_sensitive "MCPListTools.NoSensitive" "${LAST_BODY}"

MCP_VERSION="$(printf '%s' "${LAST_BODY}" | jq -r '.version // empty' 2>/dev/null || true)"
if [[ -z "${MCP_VERSION}" ]]; then
	fail_step "MCPListTools.VersionMissing" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

if ! printf '%s' "${LAST_BODY}" | jq -e '
	(.version | type == "string" and length > 0)
	and (.tools | type == "array" and length > 0)
	and (.tools | all(.[];
		((.required_scopes | type) == "array")
		and ((.required_scopes | length) > 0)
		and ((.metadata.policy | type) == "object")
	))
' >/dev/null 2>&1; then
	fail_step "MCPListTools.ContractAssert" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

# 2) Ops trace: one call -> request_id -> search_tool_calls -> mcp.<tool>
list_incidents_body='{"tool":"list_incidents","input":{"limit":1,"page":1}}'
call_or_fail "MCPCallListIncidents" POST "${BASE_URL}/v1/mcp/tools/call" "${list_incidents_body}" "incident.read"
assert_no_sensitive "MCPCallListIncidents.NoSensitive" "${LAST_BODY}"
REQUEST_ID="${LAST_REQUEST_ID}"
if [[ -z "${REQUEST_ID}" ]]; then
	fail_step "MCPCallListIncidents.RequestIDMissing" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi
INCIDENT_ID="$(printf '%s' "${LAST_BODY}" | jq -r '.output.incidents[0].incidentID // empty' 2>/dev/null || true)"

search_audit_body="$(jq -cn --arg rid "${REQUEST_ID}" '{tool:"search_tool_calls",input:{request_id:$rid,limit:50,page:1}}')"
call_or_fail "MCPSearchToolCallsByRequestID" POST "${BASE_URL}/v1/mcp/tools/call" "${search_audit_body}" "toolcall.read"
assert_no_sensitive "MCPSearchToolCallsByRequestID.NoSensitive" "${LAST_BODY}"

match_count="$(printf '%s' "${LAST_BODY}" | jq -r '
	(.output.toolCalls // [])
	| map(select((.toolName // "") == "mcp.list_incidents"))
	| length
' 2>/dev/null || true)"
if [[ -z "${match_count}" ]] || (( match_count < 1 )); then
	fail_step "MCPSearchToolCallsByRequestID.MatchTool" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi
TOOL_CALL_ID="$(printf '%s' "${LAST_BODY}" | jq -r '
	(.output.toolCalls // [])
	| map(select((.toolName // "") == "mcp.list_incidents"))
	| .[0].toolCallID // empty
' 2>/dev/null || true)"
JOB_ID="$(printf '%s' "${LAST_BODY}" | jq -r '.output.toolCalls[0].jobID // empty' 2>/dev/null || true)"

# 3) Error enum: scope denied / invalid arg
if ! http_json POST "${BASE_URL}/v1/mcp/tools/call" '{"tool":"get_incident","input":{"incident_id":"incident-c6-scope-denied"}}' "datasource.read"; then
	fail_step "ErrorScopeDenied.Call"
fi
assert_error_code "ErrorScopeDenied" "403" "SCOPE_DENIED"

if ! http_json POST "${BASE_URL}/v1/mcp/tools/call" '{"tool":"unknown_tool_for_c6","input":{}}' "incident.read"; then
	fail_step "ErrorInvalidArgument.Call"
fi
assert_error_code "ErrorInvalidArgument" "400" "INVALID_ARGUMENT"

# 4) isolation.mode deny vs not_found diff on one search tool
if [[ -z "${DENY_BASE_URL}" || -z "${NOT_FOUND_BASE_URL}" ]]; then
	if [[ "${AUTO_BOOT_ISOLATION_SERVERS}" != "1" ]]; then
		fail_step "IsolationMode.BaseURLMissing" "PRECHECK" "set DENY_BASE_URL/NOT_FOUND_BASE_URL or AUTO_BOOT_ISOLATION_SERVERS=1"
	fi
	if ! need_cmd go; then
		fail_step "IsolationMode.MissingGo" "PRECHECK" "go command not found"
	fi
	if ! need_cmd python3; then
		fail_step "IsolationMode.MissingPython3" "PRECHECK" "python3 command not found"
	fi
	if [[ -z "${DENY_BASE_URL}" ]]; then
		start_mode_server "deny" "${DENY_PORT}" DENY_BASE_URL DENY_PID DENY_CFG DENY_LOG
	fi
	if [[ -z "${NOT_FOUND_BASE_URL}" ]]; then
		start_mode_server "not_found" "${NOT_FOUND_PORT}" NOT_FOUND_BASE_URL NOT_FOUND_PID NOT_FOUND_CFG NOT_FOUND_LOG
	fi
fi

allowed_ns="c6-allow-ns-${RANDOM}"
denied_ns="c6-deny-ns-${RANDOM}"
isolation_body="$(jq -cn --arg ns "${denied_ns}" '{tool:"search_incidents",input:{namespace:$ns,limit:20,page:1}}')"

if ! http_json POST "${DENY_BASE_URL}/v1/mcp/tools/call" "${isolation_body}" "incident.read" "X-Allowed-Namespaces: ${allowed_ns}"; then
	fail_step "IsolationModeDeny.Call"
fi
assert_error_code "IsolationModeDeny" "403" "SCOPE_DENIED"

if ! http_json POST "${NOT_FOUND_BASE_URL}/v1/mcp/tools/call" "${isolation_body}" "incident.read" "X-Allowed-Namespaces: ${allowed_ns}"; then
	fail_step "IsolationModeNotFound.Call"
fi
assert_error_code "IsolationModeNotFound" "404" "NOT_FOUND"

echo "PASS C6-L1"
echo "mcp_version=${MCP_VERSION}"
echo "request_id=${REQUEST_ID:-NONE}"
echo "tool_call_id=${TOOL_CALL_ID:-NONE}"
echo "incident_id=${INCIDENT_ID:-NONE}"
echo "job_id=${JOB_ID:-NONE}"
echo "deny_base_url=${DENY_BASE_URL}"
echo "not_found_base_url=${NOT_FOUND_BASE_URL}"
