#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
DEBUG="${DEBUG:-0}"
JOB_WAIT_TIMEOUT_SEC="${JOB_WAIT_TIMEOUT_SEC:-120}"
JOB_POLL_INTERVAL_SEC="${JOB_POLL_INTERVAL_SEC:-1}"
A3_MAX_CALLS="${A3_MAX_CALLS:-1}"
A3_MAX_TOTAL_BYTES="${A3_MAX_TOTAL_BYTES:-2097152}"
A3_MAX_TOTAL_LATENCY_MS="${A3_MAX_TOTAL_LATENCY_MS:-8000}"

LAST_HTTP_CODE=""
LAST_BODY=""

INCIDENT_ID=""
JOB_ID=""
TOOL_CALL_ID=""

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

	echo "FAIL A3-L1 step=${step}"
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
					 .incident[$k] // .data.incident[$k]) |
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
					fail_step "PollAIJobTimeoutUnknownStatus=${status}" "TIMEOUT" "${LAST_BODY}"
				fi
				sleep "${JOB_POLL_INTERVAL_SEC}"
				;;
		esac
	done
}

assert_evidence_plan() {
	local body="$1"
	if need_cmd jq; then
		local plan_obj used_calls max_calls skipped_budget_count candidate_count invalid_count first_executed quality_decision
		plan_obj="$(printf '%s' "${body}" | jq -c '
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
					evidence_plan: ($resp.evidence_plan // {}),
					quality_gate: ($resp.quality_gate // {})
				  }
			)
			| map(select((.evidence_plan.version // "") == "a3"))
			| .[0] // empty
		' 2>/dev/null || true)"
		if [[ -z "${plan_obj}" ]]; then
			fail_step "AssertEvidencePlan.VersionMissing" "${LAST_HTTP_CODE}" "${body}"
		fi

		TOOL_CALL_ID="$(printf '%s' "${plan_obj}" | jq -r '.tool_call_id // empty' 2>/dev/null || true)"
		used_calls="$(printf '%s' "${plan_obj}" | jq -r '.evidence_plan.used.calls // -1' 2>/dev/null || true)"
		max_calls="$(printf '%s' "${plan_obj}" | jq -r '.evidence_plan.budget.max_calls // -1' 2>/dev/null || true)"
		if [[ -z "${used_calls}" || -z "${max_calls}" ]]; then
			fail_step "AssertEvidencePlan.BudgetFieldsMissing" "${LAST_HTTP_CODE}" "${body}"
		fi
		if (( used_calls < 0 || max_calls < 0 || used_calls > max_calls )); then
			fail_step "AssertEvidencePlan.CallsBudgetViolated" "${LAST_HTTP_CODE}" "${body}"
		fi

		skipped_budget_count="$(printf '%s' "${plan_obj}" | jq -r '[.evidence_plan.skipped[]? | select((.reason // "") == "budget_exhausted")] | length' 2>/dev/null || true)"
		if [[ -z "${skipped_budget_count}" ]] || (( skipped_budget_count < 1 )); then
			fail_step "AssertEvidencePlan.BudgetExhaustedMissing" "${LAST_HTTP_CODE}" "${body}"
		fi

		candidate_count="$(printf '%s' "${plan_obj}" | jq -r '(.evidence_plan.candidates // []) | length' 2>/dev/null || true)"
		if [[ -z "${candidate_count}" ]] || (( candidate_count < 2 )); then
			fail_step "AssertEvidencePlan.CandidateCount" "${LAST_HTTP_CODE}" "${body}"
		fi

		invalid_count="$(printf '%s' "${plan_obj}" | jq -r '
			[
				.evidence_plan.candidates[]?
				| select(
					((.score // null) | type) != "number"
					or (((.reasons // []) | type) != "array")
					or (((.reasons // []) | length) < 1)
				)
			] | length
		' 2>/dev/null || true)"
		if [[ -z "${invalid_count}" ]] || (( invalid_count > 0 )); then
			fail_step "AssertEvidencePlan.CandidateShape" "${LAST_HTTP_CODE}" "${body}"
		fi

		first_executed="$(printf '%s' "${plan_obj}" | jq -r '.evidence_plan.executed[0] // empty' 2>/dev/null || true)"
		if [[ "${first_executed}" != "query_metrics:apiserver_5xx_rate" ]]; then
			fail_step "AssertEvidencePlan.StableFirstExecuted" "${LAST_HTTP_CODE}" "${body}"
		fi

		quality_decision="$(printf '%s' "${plan_obj}" | jq -r '.quality_gate.decision // empty' 2>/dev/null || true)"
		if [[ -z "${quality_decision}" ]]; then
			fail_step "AssertEvidencePlan.QualityGateMissing" "${LAST_HTTP_CODE}" "${body}"
		fi
	else
		if ! printf '%s' "${body}" | grep -Eq '"evidence_plan"'; then
			fail_step "AssertEvidencePlan.MissingNoJQ" "${LAST_HTTP_CODE}" "${body}"
		fi
		if ! printf '%s' "${body}" | grep -Eq '"version"[[:space:]]*:[[:space:]]*"a3"'; then
			fail_step "AssertEvidencePlan.VersionNoJQ" "${LAST_HTTP_CODE}" "${body}"
		fi
		if ! printf '%s' "${body}" | grep -Eq 'budget_exhausted'; then
			fail_step "AssertEvidencePlan.BudgetNoJQ" "${LAST_HTTP_CODE}" "${body}"
		fi
		TOOL_CALL_ID="$(extract_field "${body}" "toolCallID" "tool_call_id")" || true
	fi

	if [[ -z "${TOOL_CALL_ID}" ]]; then
		TOOL_CALL_ID="NONE"
	fi
}

rand="${RAND:-$RANDOM}"
now_epoch="$(date -u +%s)"
start_epoch="$((now_epoch - 1800))"
fingerprint="a3-l1-fp-${rand}"

ingest_body=$(cat <<EOF
{"idempotencyKey":"idem-a3-l1-ingest-${rand}","fingerprint":"${fingerprint}","status":"firing","severity":"P1","service":"a3-l1-svc","cluster":"prod-a3","namespace":"default","workload":"a3-l1-workload","lastSeenAt":{"seconds":${now_epoch},"nanos":0},"labelsJSON":"{\"alertname\":\"A3EvidencePlan\",\"service\":\"a3-l1-svc\"}"}
EOF
)

call_or_fail "IngestAlertEvent" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_body}"
INCIDENT_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id")" || true
if [[ -z "${INCIDENT_ID}" ]]; then
	fail_step "IngestAlertEventParseIncidentID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

run_body=$(cat <<EOF
{"incidentID":"${INCIDENT_ID}","idempotencyKey":"idem-a3-l1-ai-run-${rand}","pipeline":"basic_rca","trigger":"manual","timeRangeStart":{"seconds":${start_epoch},"nanos":0},"timeRangeEnd":{"seconds":${now_epoch},"nanos":0},"inputHintsJSON":"{\"scenario\":\"A3_L1_EVIDENCE_PLAN\",\"A3_MAX_CALLS\":${A3_MAX_CALLS},\"A3_MAX_TOTAL_BYTES\":${A3_MAX_TOTAL_BYTES},\"A3_MAX_TOTAL_LATENCY_MS\":${A3_MAX_TOTAL_LATENCY_MS}}","createdBy":"system"}
EOF
)

call_or_fail "RunAIJob" POST "${BASE_URL}/v1/incidents/${INCIDENT_ID}/ai:run" "${run_body}"
JOB_ID="$(extract_field "${LAST_BODY}" "jobID" "job_id")" || true
if [[ -z "${JOB_ID}" ]]; then
	fail_step "RunAIJobParseJobID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
fi

wait_for_ai_job_terminal

call_or_fail "ListToolCalls" GET "${BASE_URL}/v1/ai/jobs/${JOB_ID}/tool-calls?offset=0&limit=50"
assert_evidence_plan "${LAST_BODY}"

echo "PASS A3-L1"
echo "incident_id=${INCIDENT_ID}"
echo "job_id=${JOB_ID}"
echo "tool_call_id=${TOOL_CALL_ID}"
echo "evidence_plan_version=a3"
