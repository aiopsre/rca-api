#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-incident.read incident.write}"
DEBUG="${DEBUG:-0}"

LAST_HTTP_CODE=""
LAST_BODY=""

INCIDENT_ID=""
ACTION_ID=""
RUN_ID=""
TIMELINE_STATUS="SKIP"

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

fail_step() {
	local step="$1"
	local code="${2:-${LAST_HTTP_CODE}}"
	local body="${3:-${LAST_BODY}}"

	echo "FAIL T7-L1 step=${step}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "incident_id=${INCIDENT_ID:-NONE}"
	echo "action_id=${ACTION_ID:-NONE}"
	echo "run_id=${RUN_ID:-NONE}"
	exit 1
}

assert_no_sensitive() {
	local step="$1"
	local body="$2"
	if printf '%s' "${body}" | grep -Eiq '(secret|token|authorization|headers)'; then
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
					(.[$k] // .data[$k] // .incident[$k] // .action[$k] // .run[$k] // .data.action[$k] // .data.run[$k]) |
					if . == null then empty
					elif type == "string" then .
					else tostring
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
			LAST_BODY="${LAST_BODY}"$'\n'"${curl_err}"
		fi
		return 1
	fi

	LAST_HTTP_CODE="${code}"
	debug "${method} ${url} -> ${LAST_HTTP_CODE}"
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
}

rand="${RAND:-$RANDOM}"

create_incident_body="$(cat <<EOF
{"namespace":"default","workloadKind":"Deployment","workloadName":"t7-${rand}","service":"t7-svc-${rand}","severity":"P1"}
EOF
)"

call_or_fail "CreateIncident" POST "${BASE_URL}/v1/incidents" "${create_incident_body}"
INCIDENT_ID="$(extract_field "${LAST_BODY}" "incidentID")" || true
if [[ -z "${INCIDENT_ID}" ]]; then
	fail_step "CreateIncident.ParseIncidentID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

action_details="$(printf '{"headers":{"Authorization":"Bearer t7"},"token":"abc","safe":"%s"}' "$(printf 'x%.0s' $(seq 1 3000))")"
escaped_action_details="$(printf '%s' "${action_details}" | sed 's/\\/\\\\/g; s/"/\\"/g')"
create_action_body="$(cat <<EOF
{"actionType":"restart","summary":"manual token check","detailsJSON":"${escaped_action_details}"}
EOF
)"

call_or_fail "CreateAction" POST "${BASE_URL}/v1/incidents/${INCIDENT_ID}/actions" "${create_action_body}"
ACTION_ID="$(extract_field "${LAST_BODY}" "actionID")" || true
if [[ -z "${ACTION_ID}" ]]; then
	fail_step "CreateAction.ParseActionID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi
assert_no_sensitive "CreateAction" "${LAST_BODY}"

call_or_fail "ListActions" GET "${BASE_URL}/v1/incidents/${INCIDENT_ID}/actions?page=1&limit=20"
if ! printf '%s' "${LAST_BODY}" | grep -q "${ACTION_ID}"; then
	fail_step "ListActions.AssertExists" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi
assert_no_sensitive "ListActions" "${LAST_BODY}"

params_json="$(printf '{"headers":{"Authorization":"Bearer t7"},"secret":"x","query":"%s"}' "$(printf 'y%.0s' $(seq 1 4000))")"
escaped_params_json="$(printf '%s' "${params_json}" | sed 's/\\/\\\\/g; s/"/\\"/g')"
create_run_body="$(cat <<EOF
{"source":"manual","stepIndex":1,"tool":"mcp.query_logs","paramsJSON":"${escaped_params_json}","observed":"authorization mismatch","meetsExpectation":false}
EOF
)"

call_or_fail "CreateVerificationRun" POST "${BASE_URL}/v1/incidents/${INCIDENT_ID}/verification-runs" "${create_run_body}"
RUN_ID="$(extract_field "${LAST_BODY}" "runID")" || true
if [[ -z "${RUN_ID}" ]]; then
	fail_step "CreateVerificationRun.ParseRunID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi
assert_no_sensitive "CreateVerificationRun" "${LAST_BODY}"

call_or_fail "ListVerificationRuns" GET "${BASE_URL}/v1/incidents/${INCIDENT_ID}/verification-runs?page=1&limit=20"
if ! printf '%s' "${LAST_BODY}" | grep -q "${RUN_ID}"; then
	fail_step "ListVerificationRuns.AssertExists" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi
assert_no_sensitive "ListVerificationRuns" "${LAST_BODY}"

timeline_body="$(cat <<EOF
{"tool":"get_incident_timeline","input":{"incident_id":"${INCIDENT_ID}","page":1,"limit":50}}
EOF
)"

if http_json POST "${BASE_URL}/v1/mcp/tools/call" "${timeline_body}" && [[ "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
	if printf '%s' "${LAST_BODY}" | grep -q "operator_action" && printf '%s' "${LAST_BODY}" | grep -q "verification_run"; then
		TIMELINE_STATUS="OK"
	else
		TIMELINE_STATUS="MISSING_EVENTS"
	fi
else
	TIMELINE_STATUS="SKIP"
fi

echo "PASS T7-L1 incident_id=${INCIDENT_ID} action_id=${ACTION_ID} run_id=${RUN_ID} timeline=${TIMELINE_STATUS}"
