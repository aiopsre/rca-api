#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
DEBUG="${DEBUG:-0}"
JOB_WAIT_TIMEOUT_SEC="${JOB_WAIT_TIMEOUT_SEC:-120}"
JOB_POLL_INTERVAL_SEC="${JOB_POLL_INTERVAL_SEC:-1}"
KB_WAIT_TIMEOUT_SEC="${KB_WAIT_TIMEOUT_SEC:-20}"

MYSQL_ADDR="${MYSQL_ADDR:-192.168.39.1:3306}"
MYSQL_USERNAME="${MYSQL_USERNAME:-root}"
MYSQL_PASSWORD="${MYSQL_PASSWORD:-Az123456_}"
MYSQL_DATABASE="${MYSQL_DATABASE:-rca}"

LAST_HTTP_CODE=""
LAST_BODY=""

INCIDENT_ID=""
JOB_ID=""
TOOL_CALL_ID=""
KB_ID=""

FIRST_INCIDENT_ID=""
FIRST_JOB_ID=""
SECOND_INCIDENT_ID=""
SECOND_JOB_ID=""
THIRD_JOB_ID=""

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

	echo "FAIL A4-L1 step=${step}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "incident_id=${INCIDENT_ID:-NONE}"
	echo "job_id=${JOB_ID:-NONE}"
	echo "tool_call_id=${TOOL_CALL_ID:-NONE}"
	echo "kb_id=${KB_ID:-NONE}"
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
	local step_prefix="$1"
	local deadline status now
	deadline="$(( $(date +%s) + JOB_WAIT_TIMEOUT_SEC ))"

	while true; do
		call_or_fail "${step_prefix}.PollAIJob" GET "${BASE_URL}/v1/ai/jobs/${JOB_ID}"
		status="$(extract_field "${LAST_BODY}" "status")" || true
		if [[ -z "${status}" ]]; then
			fail_step "${step_prefix}.PollAIJobStatusParse" "${LAST_HTTP_CODE}" "${LAST_BODY}"
		fi
		case "${status}" in
			queued|running)
				now="$(date +%s)"
				if (( now > deadline )); then
					fail_step "${step_prefix}.PollAIJobTimeout" "TIMEOUT" "${LAST_BODY}"
				fi
				sleep "${JOB_POLL_INTERVAL_SEC}"
				;;
			succeeded)
				return 0
				;;
			failed|canceled)
				fail_step "${step_prefix}.PollAIJobTerminal=${status}" "${LAST_HTTP_CODE}" "${LAST_BODY}"
				;;
			*)
				now="$(date +%s)"
				if (( now > deadline )); then
					fail_step "${step_prefix}.PollAIJobUnknownStatus=${status}" "TIMEOUT" "${LAST_BODY}"
				fi
				sleep "${JOB_POLL_INTERVAL_SEC}"
				;;
		esac
	done
}

run_mysql_query() {
	local step="$1"
	local sql_stmt="$2"
	local go_file out rc

	go_file="$(mktemp).go"
	cat >"${go_file}" <<'EOF'
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

func toString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(t)
	case time.Time:
		return t.UTC().Format(time.RFC3339)
	default:
		return fmt.Sprintf("%v", t)
	}
}

func main() {
	addr := os.Getenv("A4_MYSQL_ADDR")
	user := os.Getenv("A4_MYSQL_USER")
	pass := os.Getenv("A4_MYSQL_PASS")
	dbName := os.Getenv("A4_MYSQL_DB")
	stmt := os.Getenv("A4_MYSQL_SQL")

	dsn := fmt.Sprintf("%s:%s@tcp(%s)/%s?charset=utf8mb4&parseTime=true&loc=Local", user, pass, addr, dbName)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}

	rows, err := db.QueryContext(ctx, stmt)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	values := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range values {
		ptrs[i] = &values[i]
	}

	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		fields := make([]string, len(values))
		for i := range values {
			fields[i] = toString(values[i])
		}
		fmt.Println(strings.Join(fields, "\t"))
	}
	if err := rows.Err(); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}
EOF

	set +e
	out="$(
		A4_MYSQL_ADDR="${MYSQL_ADDR}" \
			A4_MYSQL_USER="${MYSQL_USERNAME}" \
			A4_MYSQL_PASS="${MYSQL_PASSWORD}" \
			A4_MYSQL_DB="${MYSQL_DATABASE}" \
			A4_MYSQL_SQL="${sql_stmt}" \
			GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn go run "${go_file}" 2>&1
	)"
	rc=$?
	set -e
	rm -f "${go_file}"

	if (( rc != 0 )); then
		LAST_HTTP_CODE="MYSQL_SQL_FAILED"
		LAST_BODY="${out}"
		debug "mysql query failed step=${step} err=${out}"
		return 1
	fi
	printf '%s' "${out}"
	return 0
}

assert_quality_gate_pass() {
	local step="$1"
	local body="$2"

	if need_cmd jq; then
		local pass_count
		pass_count="$(printf '%s' "${body}" | jq -r '
			(.toolCalls // .data.toolCalls // [])
			| map(
				((.responseJSON // .response_json // "") as $raw |
					if ($raw|type) == "string" and ($raw|length) > 0
					then (try ($raw|fromjson) catch {})
					else {}
					end
				)
				| select((.quality_gate.decision // "") == "pass")
			)
			| length
		' 2>/dev/null || true)"
		if [[ -z "${pass_count}" ]] || (( pass_count < 1 )); then
			fail_step "${step}.QualityGatePassMissing" "${LAST_HTTP_CODE}" "${body}"
		fi
	else
		if ! printf '%s' "${body}" | grep -Eq '"quality_gate"'; then
			fail_step "${step}.QualityGateMissingNoJQ" "${LAST_HTTP_CODE}" "${body}"
		fi
		if ! printf '%s' "${body}" | grep -Eq '"decision"[[:space:]]*:[[:space:]]*"pass"'; then
			fail_step "${step}.QualityGatePassMissingNoJQ" "${LAST_HTTP_CODE}" "${body}"
		fi
	fi
}

kb_refs_match() {
	local body="$1"
	local expected_kb_id="$2"

	if need_cmd jq; then
		local hit_obj ref_count actual_kb_id refs_json
		hit_obj="$(printf '%s' "${body}" | jq -c '
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
					kb_refs: ($resp.kb_refs // [])
				  }
			)
			| map(select((.kb_refs | length) > 0))
			| .[0] // empty
		' 2>/dev/null || true)"
		if [[ -z "${hit_obj}" ]]; then
			return 1
		fi

		TOOL_CALL_ID="$(printf '%s' "${hit_obj}" | jq -r '.tool_call_id // empty' 2>/dev/null || true)"
		ref_count="$(printf '%s' "${hit_obj}" | jq -r '(.kb_refs // []) | length' 2>/dev/null || true)"
		actual_kb_id="$(printf '%s' "${hit_obj}" | jq -r '.kb_refs[0].kb_id // empty' 2>/dev/null || true)"
		refs_json="$(printf '%s' "${hit_obj}" | jq -c '.kb_refs' 2>/dev/null || true)"

		if [[ -z "${ref_count}" ]] || (( ref_count < 1 )); then
			return 1
		fi
		if [[ -z "${actual_kb_id}" ]]; then
			return 1
		fi
		if [[ -n "${expected_kb_id}" && "${actual_kb_id}" != "${expected_kb_id}" ]]; then
			return 1
		fi
		assert_no_sensitive "KBRefs.NoSensitive" "${refs_json}"
	else
		if ! printf '%s' "${body}" | grep -Eq '"kb_refs"'; then
			return 1
		fi
		if [[ -n "${expected_kb_id}" ]] && ! printf '%s' "${body}" | grep -Fq "${expected_kb_id}"; then
			return 1
		fi
	fi

	if [[ -z "${TOOL_CALL_ID}" ]]; then
		TOOL_CALL_ID="$(extract_field "${body}" "toolCallID" "tool_call_id")" || true
	fi
	if [[ -z "${TOOL_CALL_ID}" ]]; then
		TOOL_CALL_ID="NONE"
	fi
	return 0
}

assert_kb_refs() {
	local step="$1"
	local body="$2"
	local expected_kb_id="$3"
	if ! kb_refs_match "${body}" "${expected_kb_id}"; then
		fail_step "${step}.KBRefsMissing" "${LAST_HTTP_CODE}" "${body}"
	fi
}

wait_for_kb_refs() {
	local step="$1"
	local job_id="$2"
	local expected_kb_id="$3"
	local deadline now
	deadline="$(( $(date +%s) + KB_WAIT_TIMEOUT_SEC ))"

	while true; do
		call_or_fail "${step}.ListToolCallsForKB" GET "${BASE_URL}/v1/ai/jobs/${job_id}/tool-calls?offset=0&limit=50"
		if kb_refs_match "${LAST_BODY}" "${expected_kb_id}"; then
			return 0
		fi
		now="$(date +%s)"
		if (( now > deadline )); then
			fail_step "${step}.KBRefsTimeout" "TIMEOUT" "${LAST_BODY}"
		fi
		sleep 1
	done
}

create_incident() {
	local step="$1"
	local idempotency="$2"
	local fingerprint="$3"
	local namespace="$4"
	local service="$5"
	local now_epoch="$6"

	local ingest_body
	ingest_body=$(cat <<EOF
{"idempotencyKey":"${idempotency}","fingerprint":"${fingerprint}","status":"firing","severity":"P1","service":"${service}","cluster":"prod-a4","namespace":"${namespace}","workload":"a4-l1-workload","lastSeenAt":{"seconds":${now_epoch},"nanos":0},"labelsJSON":"{\"alertname\":\"A4KBWriteback\",\"service\":\"${service}\"}"}
EOF
)
	call_or_fail "${step}.IngestAlertEvent" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest_body}"
	INCIDENT_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id")" || true
	if [[ -z "${INCIDENT_ID}" ]]; then
		fail_step "${step}.ParseIncidentID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
}

run_ai_job_for_incident() {
	local step="$1"
	local incident_id="$2"
	local idempotency="$3"
	local now_epoch="$4"
	local start_epoch="$5"

	local run_body
	run_body=$(cat <<EOF
{"incidentID":"${incident_id}","idempotencyKey":"${idempotency}","pipeline":"basic_rca","trigger":"manual","timeRangeStart":{"seconds":${start_epoch},"nanos":0},"timeRangeEnd":{"seconds":${now_epoch},"nanos":0},"inputHintsJSON":"{\"scenario\":\"A4_L1_KB\"}","createdBy":"system"}
EOF
)
	call_or_fail "${step}.RunAIJob" POST "${BASE_URL}/v1/incidents/${incident_id}/ai:run" "${run_body}"
	JOB_ID="$(extract_field "${LAST_BODY}" "jobID" "job_id")" || true
	if [[ -z "${JOB_ID}" ]]; then
		fail_step "${step}.ParseJobID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi

	wait_for_ai_job_terminal "${step}"
	call_or_fail "${step}.ListToolCalls" GET "${BASE_URL}/v1/ai/jobs/${JOB_ID}/tool-calls?offset=0&limit=50"
	assert_quality_gate_pass "${step}" "${LAST_BODY}"
}

rand="${RAND:-$RANDOM}"
now_epoch="$(date -u +%s)"
start_epoch="$((now_epoch - 1800))"
namespace="a4-l1-ns-${rand}"
service="a4-l1-svc"

INCIDENT_ID=""
JOB_ID=""
TOOL_CALL_ID=""
KB_ID=""

create_incident "Run1" "idem-a4-l1-ingest-1-${rand}" "a4-l1-fp-1-${rand}" "${namespace}" "${service}" "${now_epoch}"
FIRST_INCIDENT_ID="${INCIDENT_ID}"
run_ai_job_for_incident "Run1" "${FIRST_INCIDENT_ID}" "idem-a4-l1-ai-run-1-${rand}" "${now_epoch}" "${start_epoch}"
FIRST_JOB_ID="${JOB_ID}"
assert_no_sensitive "Run1.ToolCallsNoSensitive" "${LAST_BODY}"

count_sql="SELECT COUNT(*) FROM kb_entries WHERE namespace='${namespace}' AND service='${service}'"
kb_count_after_first="$(run_mysql_query "Run1.QueryKBCount" "${count_sql}")" || fail_step "Run1.QueryKBCount" "${LAST_HTTP_CODE}" "${LAST_BODY}"
kb_count_after_first="${kb_count_after_first//$'\n'/}"
if [[ -z "${kb_count_after_first}" ]] || ! [[ "${kb_count_after_first}" =~ ^[0-9]+$ ]]; then
	fail_step "Run1.KBCountParse" "MYSQL_PARSE_FAILED" "${kb_count_after_first}"
fi
if (( kb_count_after_first < 1 )); then
	fail_step "Run1.KBCountTooSmall" "ASSERT_FAILED" "kb_count_after_first=${kb_count_after_first}"
fi

kb_id_sql="SELECT kb_id FROM kb_entries WHERE namespace='${namespace}' AND service='${service}' ORDER BY updated_at DESC, id DESC LIMIT 1"
KB_ID="$(run_mysql_query "Run1.QueryKBID" "${kb_id_sql}")" || fail_step "Run1.QueryKBID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
KB_ID="${KB_ID//$'\n'/}"
if [[ -z "${KB_ID}" ]]; then
	fail_step "Run1.KBIDEmpty" "ASSERT_FAILED" "kb_id empty"
fi

create_incident "Run2" "idem-a4-l1-ingest-2-${rand}" "a4-l1-fp-2-${rand}" "${namespace}" "${service}" "${now_epoch}"
SECOND_INCIDENT_ID="${INCIDENT_ID}"
run_ai_job_for_incident "Run2" "${SECOND_INCIDENT_ID}" "idem-a4-l1-ai-run-2-${rand}" "${now_epoch}" "${start_epoch}"
SECOND_JOB_ID="${JOB_ID}"
wait_for_kb_refs "Run2" "${SECOND_JOB_ID}" "${KB_ID}"
assert_kb_refs "Run2" "${LAST_BODY}" "${KB_ID}"
assert_no_sensitive "Run2.ToolCallsNoSensitive" "${LAST_BODY}"

kb_count_after_second="$(run_mysql_query "Run2.QueryKBCount" "${count_sql}")" || fail_step "Run2.QueryKBCount" "${LAST_HTTP_CODE}" "${LAST_BODY}"
kb_count_after_second="${kb_count_after_second//$'\n'/}"
if [[ -z "${kb_count_after_second}" ]] || ! [[ "${kb_count_after_second}" =~ ^[0-9]+$ ]]; then
	fail_step "Run2.KBCountParse" "MYSQL_PARSE_FAILED" "${kb_count_after_second}"
fi

run_ai_job_for_incident "Run3" "${SECOND_INCIDENT_ID}" "idem-a4-l1-ai-run-3-${rand}" "${now_epoch}" "${start_epoch}"
THIRD_JOB_ID="${JOB_ID}"
wait_for_kb_refs "Run3" "${THIRD_JOB_ID}" "${KB_ID}"
assert_kb_refs "Run3" "${LAST_BODY}" "${KB_ID}"
assert_no_sensitive "Run3.ToolCallsNoSensitive" "${LAST_BODY}"

kb_count_after_third="$(run_mysql_query "Run3.QueryKBCount" "${count_sql}")" || fail_step "Run3.QueryKBCount" "${LAST_HTTP_CODE}" "${LAST_BODY}"
kb_count_after_third="${kb_count_after_third//$'\n'/}"
if [[ -z "${kb_count_after_third}" ]] || ! [[ "${kb_count_after_third}" =~ ^[0-9]+$ ]]; then
	fail_step "Run3.KBCountParse" "MYSQL_PARSE_FAILED" "${kb_count_after_third}"
fi
if (( kb_count_after_third != kb_count_after_second )); then
	fail_step "Run3.IdempotencyDuplicate" "ASSERT_FAILED" "kb_count_after_second=${kb_count_after_second},kb_count_after_third=${kb_count_after_third}"
fi

kb_row_sql="SELECT kb_id, root_cause_summary, patterns_json, IFNULL(evidence_signature_json,'') FROM kb_entries WHERE kb_id='${KB_ID}' LIMIT 1"
kb_row="$(run_mysql_query "Run3.QueryKBRow" "${kb_row_sql}")" || fail_step "Run3.QueryKBRow" "${LAST_HTTP_CODE}" "${LAST_BODY}"
if [[ -z "${kb_row}" ]]; then
	fail_step "Run3.KBRowMissing" "ASSERT_FAILED" "kb row missing for ${KB_ID}"
fi
assert_no_sensitive "Run3.KBRowNoSensitive" "${kb_row}"

echo "PASS A4-L1"
echo "incident_id_1=${FIRST_INCIDENT_ID}"
echo "job_id_1=${FIRST_JOB_ID}"
echo "incident_id_2=${SECOND_INCIDENT_ID}"
echo "job_id_2=${SECOND_JOB_ID}"
echo "job_id_3=${THIRD_JOB_ID}"
echo "tool_call_id=${TOOL_CALL_ID}"
echo "kb_id=${KB_ID}"
