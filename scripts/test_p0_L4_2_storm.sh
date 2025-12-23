#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
N="${N:-200}"
CONCURRENCY="${CONCURRENCY:-10}"
IDEM_REUSE_RATIO="${IDEM_REUSE_RATIO:-0.5}"
FINGERPRINT_MODE="${FINGERPRINT_MODE:-single}"
WAIT_JOB="${WAIT_JOB:-1}"
JOB_TIMEOUT_S="${JOB_TIMEOUT_S:-180}"
JOB_POLL_INTERVAL_S="${JOB_POLL_INTERVAL_S:-1}"
DEBUG="${DEBUG:-0}"
SLEEP_MS="${SLEEP_MS:-0}"
INGEST_RETRY_MAX="${INGEST_RETRY_MAX:-5}"
INGEST_RETRY_BACKOFF_MS="${INGEST_RETRY_BACKOFF_MS:-30}"

FINGERPRINT_BUCKETS="${FINGERPRINT_BUCKETS:-5}"
CURRENT_QUERY_LIMIT="${CURRENT_QUERY_LIMIT:-200}"
MAX_TOOLCALLS_PER_JOB="${MAX_TOOLCALLS_PER_JOB:-6}"
MIN_TOOLCALLS_PER_JOB="${MIN_TOOLCALLS_PER_JOB:-2}"
MAX_EVIDENCES_PER_JOB="${MAX_EVIDENCES_PER_JOB:-3}"

LAST_HTTP_CODE=""
LAST_BODY=""

TMP_DIR="${TMP_DIR:-}"
ALL_RESULTS="${ALL_RESULTS:-}"
SAMPLE_INCIDENT_ID="${SAMPLE_INCIDENT_ID:-NONE}"
SAMPLE_EVENT_ID="${SAMPLE_EVENT_ID:-NONE}"
SAMPLE_JOB_ID="${SAMPLE_JOB_ID:-NONE}"

STATS_N="${STATS_N:-}"
STATS_UNIQUE_FINGERPRINTS="${STATS_UNIQUE_FINGERPRINTS:-}"
STATS_UNIQUE_INCIDENTS="${STATS_UNIQUE_INCIDENTS:-}"
STATS_UNIQUE_CURRENT_EVENTS="${STATS_UNIQUE_CURRENT_EVENTS:-}"
STATS_UNIQUE_JOBS="${STATS_UNIQUE_JOBS:-}"
STATS_UNIQUE_TOOLCALLS="${STATS_UNIQUE_TOOLCALLS:-}"
STATS_UNIQUE_EVIDENCES="${STATS_UNIQUE_EVIDENCES:-}"
STATS_INGEST_REUSED="${STATS_INGEST_REUSED:-}"
STATS_RUN_ALREADY_RUNNING="${STATS_RUN_ALREADY_RUNNING:-}"
STATS_INGEST_IDEM_REUSE_COUNT="${STATS_INGEST_IDEM_REUSE_COUNT:-}"
STATS_RUN_IDEM_REUSE_COUNT="${STATS_RUN_IDEM_REUSE_COUNT:-}"
STORM_RUN_ID="${STORM_RUN_ID:-}"

MAX_UNIQUE_INCIDENTS="${MAX_UNIQUE_INCIDENTS:-}"
MAX_UNIQUE_JOBS="${MAX_UNIQUE_JOBS:-}"

INGEST_REUSE_COUNT="${INGEST_REUSE_COUNT:-}"
RUN_REUSE_COUNT="${RUN_REUSE_COUNT:-}"
INGEST_REUSE_BUCKETS="${INGEST_REUSE_BUCKETS:-}"
RUN_REUSE_BUCKETS="${RUN_REUSE_BUCKETS:-}"
FINGERPRINT_BASE="${FINGERPRINT_BASE:-}"
SCRIPT_PATH="${SCRIPT_PATH:-}"

usage() {
	cat <<USAGE
Usage: scripts/test_p0_L4_2_storm.sh

Env:
  BASE_URL (default: http://127.0.0.1:5555)
  SCOPES (default: *)
  N (default: 200)
  CONCURRENCY (default: 10)
  IDEM_REUSE_RATIO (default: 0.5)
  FINGERPRINT_MODE (default: single; values: single|few)
  WAIT_JOB (default: 1)
  JOB_TIMEOUT_S (default: 180)
  JOB_POLL_INTERVAL_S (default: 1)
  INGEST_RETRY_MAX (default: 5)
  INGEST_RETRY_BACKOFF_MS (default: 30)
  DEBUG (default: 0)
USAGE
}

debug() {
	if [[ "${DEBUG}" == "1" ]]; then
		echo "[DEBUG] $*" >&2
	fi
}

need_cmd() {
	command -v "$1" >/dev/null 2>&1
}

trim_2kb() {
	printf '%s' "$1" | head -c 2048
}

snapshot_lines() {
	echo "n=${STATS_N:-UNKNOWN}"
	echo "unique_fingerprints=${STATS_UNIQUE_FINGERPRINTS:-UNKNOWN}"
	echo "unique_incidents=${STATS_UNIQUE_INCIDENTS:-UNKNOWN}"
	echo "unique_current_events=${STATS_UNIQUE_CURRENT_EVENTS:-UNKNOWN}"
	echo "unique_jobs=${STATS_UNIQUE_JOBS:-UNKNOWN}"
	echo "unique_toolcalls=${STATS_UNIQUE_TOOLCALLS:-UNKNOWN}"
	echo "unique_evidences=${STATS_UNIQUE_EVIDENCES:-UNKNOWN}"
	echo "ingest_reused=${STATS_INGEST_REUSED:-UNKNOWN}"
	echo "run_already_running=${STATS_RUN_ALREADY_RUNNING:-UNKNOWN}"
	echo "ingest_idem_reuse_count=${STATS_INGEST_IDEM_REUSE_COUNT:-UNKNOWN}"
	echo "run_idem_reuse_count=${STATS_RUN_IDEM_REUSE_COUNT:-UNKNOWN}"
	echo "storm_run_id=${STORM_RUN_ID:-UNKNOWN}"
	echo "sample_incident_id=${SAMPLE_INCIDENT_ID:-NONE}"
	echo "sample_event_id=${SAMPLE_EVENT_ID:-NONE}"
	echo "sample_job_id=${SAMPLE_JOB_ID:-NONE}"
}

fail_l42() {
	local step="$1"
	local detail="${2:-}"
	local code="${3:-${LAST_HTTP_CODE}}"
	local body="${4:-${LAST_BODY}}"

	echo "FAIL L4-2 step=${step}"
	echo "detail=${detail:-N/A}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	trim_2kb "${body}"
	echo
	echo "EOF"
	snapshot_lines
	exit 1
}

fail_from_worker_file() {
	local ff="$1"
	if [[ ! -f "${ff}" ]]; then
		fail_l42 "WorkerUnknown" "worker failed but no failure file found"
	fi

	local step code detail body
	step="$(sed -n 's/^step=//p' "${ff}" | head -n1)"
	code="$(sed -n 's/^http_code=//p' "${ff}" | head -n1)"
	detail="$(sed -n 's/^detail=//p' "${ff}" | head -n1)"
	body="$(sed -n '/^body<<EOF$/,/^EOF$/p' "${ff}" | sed '1d;$d')"
	SAMPLE_INCIDENT_ID="$(sed -n 's/^incident_id=//p' "${ff}" | head -n1)"
	SAMPLE_EVENT_ID="$(sed -n 's/^event_id=//p' "${ff}" | head -n1)"
	SAMPLE_JOB_ID="$(sed -n 's/^job_id=//p' "${ff}" | head -n1)"
	fail_l42 "${step:-WorkerUnknown}" "${detail:-worker error}" "${code:-UNKNOWN}" "${body}"
}

extract_field() {
	local json="$1"
	shift
	local keys=("$@")
	local key value

	for key in "${keys[@]}"; do
		value="$(
			printf '%s' "${json}" | jq -r --arg k "${key}" '
				(.[$k] // .data[$k] // .job[$k] // .data.job[$k] //
				 .incident[$k] // .data.incident[$k] //
				 .event[$k] // .data.event[$k]) |
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
	return 1
}

http_json() {
	local method="$1"
	local url="$2"
	local body="${3:-}"

	local tmp_body tmp_err rc code curl_err
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
	return 0
}

call_or_fail() {
	local step="$1"
	local method="$2"
	local url="$3"
	local body="${4:-}"

	if ! http_json "${method}" "${url}" "${body}"; then
		fail_l42 "${step}" "curl failed"
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		fail_l42 "${step}" "non-2xx response"
	fi
}

worker_fail() {
	local idx="$1"
	local step="$2"
	local detail="$3"
	local code="$4"
	local body="$5"
	local incident_id="$6"
	local event_id="$7"
	local job_id="$8"

	local fail_base="${TMP_DIR:-}"
	if [[ -z "${fail_base}" ]]; then
		fail_base="/tmp/test_p0_L4_2_storm_fallback_${PPID}"
	fi
	mkdir -p "${fail_base}/fail" 2>/dev/null || true

	local ff="${fail_base}/fail/${idx}.log"
	{
		echo "step=${step}"
		echo "detail=${detail}"
		echo "http_code=${code}"
		echo "incident_id=${incident_id:-NONE}"
		echo "event_id=${event_id:-NONE}"
		echo "job_id=${job_id:-NONE}"
		echo "body<<EOF"
		trim_2kb "${body}"
		echo
		echo "EOF"
	} >"${ff}"
	exit 1
}

worker_compute_fingerprint() {
	local idx="$1"
	if [[ "${FINGERPRINT_MODE}" == "single" ]]; then
		echo "${FINGERPRINT_BASE}"
		return 0
	fi
	if [[ "${FINGERPRINT_MODE}" == "few" ]]; then
		local bucket=$(( (idx - 1) % FINGERPRINT_BUCKETS ))
		echo "${FINGERPRINT_BASE}-${bucket}"
		return 0
	fi
	echo ""
	return 1
}

worker_compute_idem_key() {
	local kind="$1"
	local idx="$2"
	local fp="$3"
	local reuse_count="$4"
	local reuse_buckets="$5"

	if (( idx <= reuse_count )); then
		local bucket=$(( (idx - 1) % reuse_buckets ))
		echo "idem-l42-${kind}-${STORM_RUN_ID}-${fp}-reuse-${bucket}"
		return 0
	fi
	echo "idem-l42-${kind}-${STORM_RUN_ID}-${fp}-u-${idx}"
}

worker_main() {
	local idx="$1"
	local fingerprint ingest_idem run_idem_base run_idem now_epoch start_epoch body
	local run_result="created"
	local ingest_attempt=0
	local incident_id="" event_id="" job_id="" merge_result=""

	fingerprint="$(worker_compute_fingerprint "${idx}")" || true
	if [[ -z "${fingerprint}" ]]; then
		worker_fail "${idx}" "ComputeFingerprint" "unsupported FINGERPRINT_MODE=${FINGERPRINT_MODE}" "N/A" "" "" "" ""
	fi

	ingest_idem="$(worker_compute_idem_key ingest "${idx}" "${fingerprint}" "${INGEST_REUSE_COUNT}" "${INGEST_REUSE_BUCKETS}")"
	run_idem_base="$(worker_compute_idem_key run "${idx}" "${fingerprint}" "${RUN_REUSE_COUNT}" "${RUN_REUSE_BUCKETS}")"

	now_epoch="$(date -u +%s)"
	start_epoch=$((now_epoch - 1800))

	body="$(jq -cn \
		--arg idem "${ingest_idem}" \
		--arg fp "${fingerprint}" \
		--arg service "storm-svc" \
		--arg cluster "prod-storm" \
		--arg namespace "default" \
		--arg workload "storm-workload" \
		--arg labels '{"alertname":"StormReplay","service":"storm-svc"}' \
		--argjson now "${now_epoch}" \
		'{
			idempotencyKey:$idem,
			fingerprint:$fp,
			status:"firing",
			severity:"P1",
			service:$service,
			cluster:$cluster,
			namespace:$namespace,
			workload:$workload,
			lastSeenAt:{seconds:$now,nanos:0},
			labelsJSON:$labels
		}')"

	while true; do
		if ! http_json "POST" "${BASE_URL}/v1/alert-events:ingest" "${body}"; then
			worker_fail "${idx}" "IngestAlertEvent" "curl failed" "${LAST_HTTP_CODE}" "${LAST_BODY}" "${incident_id}" "${event_id}" "${job_id}"
		fi
		if [[ "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
			break
		fi

		if [[ "${LAST_HTTP_CODE}" == "500" ]] && printf '%s' "${LAST_BODY}" | grep -q "InternalError.IncidentCreateFailed"; then
			ingest_attempt=$((ingest_attempt + 1))
			if (( ingest_attempt <= INGEST_RETRY_MAX )); then
				sleep "$(awk -v ms="${INGEST_RETRY_BACKOFF_MS}" 'BEGIN{printf "%.3f", ms/1000}')"
				continue
			fi
			worker_fail "${idx}" "IngestAlertEvent" "incident create failed after retries=${INGEST_RETRY_MAX}" "${LAST_HTTP_CODE}" "${LAST_BODY}" "${incident_id}" "${event_id}" "${job_id}"
		fi
		if [[ "${LAST_HTTP_CODE}" == "409" ]] && printf '%s' "${LAST_BODY}" | grep -q "Conflict.AlertEventIdempotencyConflict"; then
			ingest_attempt=$((ingest_attempt + 1))
			if (( ingest_attempt <= INGEST_RETRY_MAX )); then
				sleep "$(awk -v ms="${INGEST_RETRY_BACKOFF_MS}" 'BEGIN{printf "%.3f", ms/1000}')"
				continue
			fi
			worker_fail "${idx}" "IngestAlertEvent" "idempotency conflict after retries=${INGEST_RETRY_MAX} (ingest_idem=${ingest_idem}, fingerprint=${fingerprint})" "${LAST_HTTP_CODE}" "${LAST_BODY}" "${incident_id}" "${event_id}" "${job_id}"
		fi

		worker_fail "${idx}" "IngestAlertEvent" "non-2xx response" "${LAST_HTTP_CODE}" "${LAST_BODY}" "${incident_id}" "${event_id}" "${job_id}"
	done

	incident_id="$(extract_field "${LAST_BODY}" "incidentID" "incident_id")" || true
	event_id="$(extract_field "${LAST_BODY}" "eventID" "event_id")" || true
	merge_result="$(extract_field "${LAST_BODY}" "mergeResult" "merge_result")" || true
	if [[ -z "${incident_id}" ]] || [[ -z "${event_id}" ]]; then
		worker_fail "${idx}" "ParseIngestResponse" "missing incidentID/eventID" "${LAST_HTTP_CODE}" "${LAST_BODY}" "${incident_id}" "${event_id}" "${job_id}"
	fi
	# AIJob idempotency key is globally unique in current backend implementation.
	# Scope reuse to incident to satisfy "(incident_id, idem) reuse" semantics and avoid cross-incident conflicts.
	run_idem="${run_idem_base}-${incident_id}"

	body="$(jq -cn \
		--arg incident_id "${incident_id}" \
		--arg idem "${run_idem}" \
		--arg event_id "${event_id}" \
		--arg fp "${fingerprint}" \
		--argjson start "${start_epoch}" \
		--argjson end "${now_epoch}" \
		'{
			incidentID:$incident_id,
			idempotencyKey:$idem,
			pipeline:"basic_rca",
			trigger:"manual",
			timeRangeStart:{seconds:$start,nanos:0},
			timeRangeEnd:{seconds:$end,nanos:0},
			inputHintsJSON:( {scenario:"L4-2", event_id:$event_id, fingerprint:$fp} | tostring ),
			createdBy:"system"
		}')"

	if ! http_json "POST" "${BASE_URL}/v1/incidents/${incident_id}/ai:run" "${body}"; then
		worker_fail "${idx}" "RunAIJob" "curl failed" "${LAST_HTTP_CODE}" "${LAST_BODY}" "${incident_id}" "${event_id}" "${job_id}"
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		if [[ "${LAST_HTTP_CODE}" == "409" ]] && printf '%s' "${LAST_BODY}" | grep -q "Conflict.AIJobAlreadyRunning"; then
			run_result="already_running_conflict"
			if ! http_json "GET" "${BASE_URL}/v1/incidents/${incident_id}/ai?offset=0&limit=50"; then
				worker_fail "${idx}" "RunAIJobConflictFallback" "list incident jobs failed" "${LAST_HTTP_CODE}" "${LAST_BODY}" "${incident_id}" "${event_id}" "${job_id}"
			fi
			if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
				worker_fail "${idx}" "RunAIJobConflictFallback" "list incident jobs non-2xx" "${LAST_HTTP_CODE}" "${LAST_BODY}" "${incident_id}" "${event_id}" "${job_id}"
			fi
			job_id="$(
				printf '%s' "${LAST_BODY}" | jq -r '
					def jid: (.jobID // .job_id // empty);
					(
						((.jobs // .data.jobs // []) |
							map(select(((.status // .Status // "") | ascii_downcase) == "queued" or
								((.status // .Status // "") | ascii_downcase) == "running")) |
							.[0] | jid) //
						((.jobs // .data.jobs // [])[0] | jid) //
						empty
					)
				' 2>/dev/null || true
			)"
			if [[ -z "${job_id}" ]]; then
				worker_fail "${idx}" "RunAIJobConflictFallbackParse" "no fallback job found (run_idem=${run_idem})" "${LAST_HTTP_CODE}" "${LAST_BODY}" "${incident_id}" "${event_id}" "${job_id}"
			fi
		else
			worker_fail "${idx}" "RunAIJob" "non-2xx response (run_idem=${run_idem})" "${LAST_HTTP_CODE}" "${LAST_BODY}" "${incident_id}" "${event_id}" "${job_id}"
		fi
	else
		job_id="$(extract_field "${LAST_BODY}" "jobID" "job_id")" || true
		if [[ -z "${job_id}" ]]; then
			worker_fail "${idx}" "ParseRunAIJobResponse" "missing jobID" "${LAST_HTTP_CODE}" "${LAST_BODY}" "${incident_id}" "${event_id}" "${job_id}"
		fi
	fi

	printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
		"${idx}" "${fingerprint}" "${ingest_idem}" "${event_id}" "${incident_id}" "${merge_result}" "${run_idem}" "${job_id}" "${run_result}" \
		>"${TMP_DIR}/results/${idx}.tsv"

	if (( SLEEP_MS > 0 )); then
		sleep "$(awk -v ms="${SLEEP_MS}" 'BEGIN{printf "%.3f", ms/1000}')"
	fi
}

count_unique_column() {
	local col="$1"
	awk -F'\t' -v c="${col}" 'NF>=c{print $c}' "${ALL_RESULTS}" | awk 'NF' | sort -u | wc -l | tr -d ' '
}

check_idem_stability() {
	local key_col="$1"
	local val_col="$2"
	local label="$3"

	local mismatch
	mismatch="$(awk -F'\t' -v k="${key_col}" -v v="${val_col}" '
		NF>=v {
			key=$k
			val=$v
			if (key=="" || val=="") next
			if ((key in seen) && seen[key] != val) {
				print key "\t" seen[key] "\t" val
				exit 0
			}
			seen[key]=val
		}
	' "${ALL_RESULTS}")"
	if [[ -n "${mismatch}" ]]; then
		fail_l42 "Idempotency-${label}" "inconsistent idempotent mapping: ${mismatch}" "N/A" ""
	fi
}

check_run_idem_stability() {
	local mismatch
	mismatch="$(awk -F'\t' '
		NF>=9 && $9 != "already_running_conflict" {
			key=$7
			val=$8
			if (key=="" || val=="") next
			if ((key in seen) && seen[key] != val) {
				print key "\t" seen[key] "\t" val
				exit 0
			}
			seen[key]=val
		}
	' "${ALL_RESULTS}")"
	if [[ -n "${mismatch}" ]]; then
		fail_l42 "Idempotency-ai_run" "inconsistent idempotent mapping: ${mismatch}" "N/A" ""
	fi
}

collect_sample_ids() {
	SAMPLE_EVENT_ID="$(awk -F'\t' 'NF>=4 && $4!="" {print $4; exit}' "${ALL_RESULTS}")"
	SAMPLE_INCIDENT_ID="$(awk -F'\t' 'NF>=5 && $5!="" {print $5; exit}' "${ALL_RESULTS}")"
	SAMPLE_JOB_ID="$(awk -F'\t' 'NF>=8 && $8!="" {print $8; exit}' "${ALL_RESULTS}")"
	SAMPLE_EVENT_ID="${SAMPLE_EVENT_ID:-NONE}"
	SAMPLE_INCIDENT_ID="${SAMPLE_INCIDENT_ID:-NONE}"
	SAMPLE_JOB_ID="${SAMPLE_JOB_ID:-NONE}"
}

wait_jobs_terminal() {
	local jobs_file="$1"
	local pending_file next_file deadline now job_id status

	pending_file="${TMP_DIR}/jobs_pending.txt"
	next_file="${TMP_DIR}/jobs_next.txt"
	cp "${jobs_file}" "${pending_file}"
	deadline=$(( $(date +%s) + JOB_TIMEOUT_S ))

	while [[ -s "${pending_file}" ]]; do
		: >"${next_file}"
		while IFS= read -r job_id; do
			[[ -z "${job_id}" ]] && continue
			call_or_fail "PollAIJob" "GET" "${BASE_URL}/v1/ai/jobs/${job_id}"
			status="$(extract_field "${LAST_BODY}" "status")" || true
			if [[ -z "${status}" ]]; then
				fail_l42 "PollAIJobStatusParse" "job_id=${job_id}" "${LAST_HTTP_CODE}" "${LAST_BODY}"
			fi

			case "${status}" in
				succeeded)
					;;
				queued|running)
					echo "${job_id}" >>"${next_file}"
					;;
				failed|canceled)
					fail_l42 "PollAIJobTerminal=${status}" "job_id=${job_id}" "${LAST_HTTP_CODE}" "${LAST_BODY}"
					;;
				*)
					echo "${job_id}" >>"${next_file}"
					;;
			esac
		done <"${pending_file}"

		mv "${next_file}" "${pending_file}"
		if [[ -s "${pending_file}" ]]; then
			now="$(date +%s)"
			if (( now > deadline )); then
				local sample_pending
				sample_pending="$(head -n1 "${pending_file}")"
				fail_l42 "PollAIJobTimeout" "pending_job=${sample_pending}" "TIMEOUT" ""
			fi
			sleep "${JOB_POLL_INTERVAL_S}"
		fi
	done
}

collect_current_events() {
	local fp_file="$1"
	local current_ids_file="${TMP_DIR}/current_event_ids.txt"
	local total_all=0 fp total

	: >"${current_ids_file}"
	while IFS= read -r fp; do
		[[ -z "${fp}" ]] && continue
		call_or_fail "ListCurrentAlertEvents" "GET" "${BASE_URL}/v1/alert-events:current?fingerprint=${fp}&offset=0&limit=${CURRENT_QUERY_LIMIT}"
		total="$(printf '%s' "${LAST_BODY}" | jq -r '(.totalCount // .data.totalCount // 0) | tonumber' 2>/dev/null || true)"
		if [[ -z "${total}" ]]; then
			fail_l42 "ListCurrentAlertEventsParse" "fingerprint=${fp}" "${LAST_HTTP_CODE}" "${LAST_BODY}"
		fi
		total_all=$((total_all + total))

		if (( total > 1 )); then
			fail_l42 "CurrentViewExplosion" "fingerprint=${fp} total=${total}" "${LAST_HTTP_CODE}" "${LAST_BODY}"
		fi

		printf '%s' "${LAST_BODY}" | jq -r '(.events // .data.events // [])[].eventID // .event_id // empty' 2>/dev/null >>"${current_ids_file}" || true
	done <"${fp_file}"

	STATS_UNIQUE_CURRENT_EVENTS="$(awk 'NF' "${current_ids_file}" | sort -u | wc -l | tr -d ' ')"
}

collect_toolcalls_and_evidences() {
	local jobs_file="$1"
	local tool_ids_file="${TMP_DIR}/toolcall_ids.txt"
	local evidence_ids_file="${TMP_DIR}/evidence_ids.txt"
	local job_id evidence_raw output_raw

	: >"${tool_ids_file}"
	: >"${evidence_ids_file}"

	while IFS= read -r job_id; do
		[[ -z "${job_id}" ]] && continue

		call_or_fail "GetAIJob" "GET" "${BASE_URL}/v1/ai/jobs/${job_id}"
		evidence_raw="$(extract_field "${LAST_BODY}" "evidenceIDsJSON" "evidence_ids_json")" || true
		if [[ -n "${evidence_raw}" ]]; then
			printf '%s' "${evidence_raw}" | jq -r '.[]? // empty' 2>/dev/null >>"${evidence_ids_file}" || true
		fi
		output_raw="$(extract_field "${LAST_BODY}" "outputJSON" "output_json")" || true
		if [[ -n "${output_raw}" ]]; then
			printf '%s' "${output_raw}" | jq -r '.root_cause.evidence_ids[]? // empty, .hypotheses[]?.supporting_evidence_ids[]? // empty' 2>/dev/null >>"${evidence_ids_file}" || true
		fi

		call_or_fail "ListToolCalls" "GET" "${BASE_URL}/v1/ai/jobs/${job_id}/tool-calls?offset=0&limit=200"
		printf '%s' "${LAST_BODY}" | jq -r '(.toolCalls // .data.toolCalls // [])[].toolCallID // .tool_call_id // empty' 2>/dev/null >>"${tool_ids_file}" || true

		printf '%s' "${LAST_BODY}" | jq -r '(.toolCalls // .data.toolCalls // [])[].evidenceIDsJSON // .evidence_ids_json // empty' 2>/dev/null | \
		while IFS= read -r evidence_raw; do
			[[ -z "${evidence_raw}" ]] && continue
			printf '%s' "${evidence_raw}" | jq -r '.[]? // empty' 2>/dev/null >>"${evidence_ids_file}" || true
		done
	done <"${jobs_file}"

	STATS_UNIQUE_TOOLCALLS="$(awk 'NF' "${tool_ids_file}" | sort -u | wc -l | tr -d ' ')"
	STATS_UNIQUE_EVIDENCES="$(awk 'NF' "${evidence_ids_file}" | sort -u | wc -l | tr -d ' ')"
}

validate_thresholds() {
	local max_current_allowed max_tc max_ev

	if [[ -z "${MAX_UNIQUE_INCIDENTS}" ]]; then
		if [[ "${FINGERPRINT_MODE}" == "single" ]]; then
			MAX_UNIQUE_INCIDENTS=5
		else
			MAX_UNIQUE_INCIDENTS=$(( FINGERPRINT_BUCKETS * 3 ))
		fi
	fi
	if [[ -z "${MAX_UNIQUE_JOBS}" ]]; then
		MAX_UNIQUE_JOBS=$(( (N - RUN_REUSE_COUNT) + RUN_REUSE_BUCKETS + 10 ))
	fi

	max_current_allowed=$(( STATS_UNIQUE_FINGERPRINTS * 1 ))
	max_tc=$(( STATS_UNIQUE_JOBS * MAX_TOOLCALLS_PER_JOB ))
	max_ev=$(( STATS_UNIQUE_JOBS * MAX_EVIDENCES_PER_JOB ))

	if (( STATS_UNIQUE_INCIDENTS > MAX_UNIQUE_INCIDENTS )); then
		fail_l42 "AssertIncidents" "unique_incidents=${STATS_UNIQUE_INCIDENTS} > ${MAX_UNIQUE_INCIDENTS}" "N/A" ""
	fi
	if (( STATS_UNIQUE_CURRENT_EVENTS > max_current_allowed )); then
		fail_l42 "AssertCurrentEvents" "unique_current_events=${STATS_UNIQUE_CURRENT_EVENTS} > ${max_current_allowed}" "N/A" ""
	fi
	if (( STATS_UNIQUE_JOBS > N )); then
		fail_l42 "AssertJobsUpperN" "unique_jobs=${STATS_UNIQUE_JOBS} > N=${N}" "N/A" ""
	fi
	if (( STATS_UNIQUE_JOBS > MAX_UNIQUE_JOBS )); then
		fail_l42 "AssertJobsThreshold" "unique_jobs=${STATS_UNIQUE_JOBS} > ${MAX_UNIQUE_JOBS}" "N/A" ""
	fi
	if (( STATS_UNIQUE_TOOLCALLS > max_tc )); then
		fail_l42 "AssertToolCallsUpper" "unique_toolcalls=${STATS_UNIQUE_TOOLCALLS} > ${max_tc}" "N/A" ""
	fi
	if (( WAIT_JOB == 1 )); then
		if (( STATS_UNIQUE_TOOLCALLS < STATS_UNIQUE_JOBS * MIN_TOOLCALLS_PER_JOB )); then
			fail_l42 "AssertToolCallsLower" "unique_toolcalls=${STATS_UNIQUE_TOOLCALLS} < unique_jobs*${MIN_TOOLCALLS_PER_JOB}" "N/A" ""
		fi
	fi
	if (( STATS_UNIQUE_EVIDENCES > max_ev )); then
		fail_l42 "AssertEvidencesUpper" "unique_evidences=${STATS_UNIQUE_EVIDENCES} > ${max_ev}" "N/A" ""
	fi
	if (( WAIT_JOB == 1 )) && (( STATS_UNIQUE_JOBS > 0 )) && (( STATS_UNIQUE_EVIDENCES < 1 )); then
		fail_l42 "AssertEvidencesLower" "unique_evidences is empty" "N/A" ""
	fi
}

run_parent() {
	local xargs_rc first_fail

	if ! [[ "${N}" =~ ^[0-9]+$ ]] || (( N <= 0 )); then
		fail_l42 "ValidateEnv" "N must be positive integer"
	fi
	if ! [[ "${CONCURRENCY}" =~ ^[0-9]+$ ]] || (( CONCURRENCY <= 0 )); then
		fail_l42 "ValidateEnv" "CONCURRENCY must be positive integer"
	fi
	if ! [[ "${WAIT_JOB}" =~ ^[01]$ ]]; then
		fail_l42 "ValidateEnv" "WAIT_JOB must be 0 or 1"
	fi
	if ! [[ "${CURRENT_QUERY_LIMIT}" =~ ^[0-9]+$ ]] || (( CURRENT_QUERY_LIMIT <= 0 )); then
		fail_l42 "ValidateEnv" "CURRENT_QUERY_LIMIT must be positive integer"
	fi
	if (( CURRENT_QUERY_LIMIT > 200 )); then
		CURRENT_QUERY_LIMIT=200
	fi

	if ! need_cmd jq; then
		fail_l42 "PrecheckJQ" "jq is required"
	fi
	if ! need_cmd seq; then
		fail_l42 "PrecheckSeq" "seq is required"
	fi
	if ! need_cmd xargs; then
		fail_l42 "PrecheckXargs" "xargs is required"
	fi

	INGEST_REUSE_COUNT="$(awk -v n="${N}" -v r="${IDEM_REUSE_RATIO}" 'BEGIN{v=int(n*r+0.5); if(v<0)v=0; if(v>n)v=n; print v}')"
	RUN_REUSE_COUNT="${INGEST_REUSE_COUNT}"
	INGEST_REUSE_BUCKETS="$(awk -v n="${INGEST_REUSE_COUNT}" 'BEGIN{v=int(n/10); if(v<1)v=1; print v}')"
	RUN_REUSE_BUCKETS="${INGEST_REUSE_BUCKETS}"
	FINGERPRINT_BASE="p0-l4-2-fp-${RANDOM}-$(date +%s)"
	STORM_RUN_ID="run-$(date +%s)-$$-${RANDOM}"

	TMP_DIR="$(mktemp -d)"
	ALL_RESULTS="${TMP_DIR}/all_results.tsv"
	mkdir -p "${TMP_DIR}/results" "${TMP_DIR}/fail"
	trap 'rm -rf "${TMP_DIR}"' EXIT

	SCRIPT_PATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"

	export BASE_URL CURL SCOPES N CONCURRENCY IDEM_REUSE_RATIO FINGERPRINT_MODE WAIT_JOB JOB_TIMEOUT_S JOB_POLL_INTERVAL_S DEBUG SLEEP_MS
	export FINGERPRINT_BUCKETS CURRENT_QUERY_LIMIT MAX_TOOLCALLS_PER_JOB MIN_TOOLCALLS_PER_JOB MAX_EVIDENCES_PER_JOB
	export TMP_DIR FINGERPRINT_BASE INGEST_REUSE_COUNT RUN_REUSE_COUNT INGEST_REUSE_BUCKETS RUN_REUSE_BUCKETS STORM_RUN_ID

	call_or_fail "PrecheckListIncidents" "GET" "${BASE_URL}/v1/incidents?offset=0&limit=1"

	set +e
	seq 1 "${N}" | xargs -I{} -P "${CONCURRENCY}" "${SCRIPT_PATH}" --worker "{}"
	xargs_rc=$?
	set -e
	if (( xargs_rc != 0 )); then
		first_fail="$(find "${TMP_DIR}/fail" -type f | sort | head -n1)"
		fail_from_worker_file "${first_fail}"
	fi

	if ! find "${TMP_DIR}/results" -type f | grep -q .; then
		fail_l42 "StormRun" "no worker results found"
	fi
	find "${TMP_DIR}/results" -type f | sort | xargs cat >"${ALL_RESULTS}"

	collect_sample_ids
	STATS_N="$(wc -l <"${ALL_RESULTS}" | tr -d ' ')"
	STATS_UNIQUE_FINGERPRINTS="$(count_unique_column 2)"
	STATS_UNIQUE_INCIDENTS="$(count_unique_column 5)"
	STATS_UNIQUE_JOBS="$(count_unique_column 8)"
	STATS_INGEST_REUSED="$(awk -F'\t' 'NF>=6 && $6=="idempotent_reused" {c++} END{print c+0}' "${ALL_RESULTS}")"
	STATS_RUN_ALREADY_RUNNING="$(awk -F'\t' 'NF>=9 && $9=="already_running_conflict" {c++} END{print c+0}' "${ALL_RESULTS}")"
	STATS_INGEST_IDEM_REUSE_COUNT="${INGEST_REUSE_COUNT}"
	STATS_RUN_IDEM_REUSE_COUNT="${RUN_REUSE_COUNT}"

	check_idem_stability 3 4 ingest
	check_run_idem_stability

	awk -F'\t' 'NF>=2 && $2!="" {print $2}' "${ALL_RESULTS}" | sort -u >"${TMP_DIR}/fingerprints.txt"
	collect_current_events "${TMP_DIR}/fingerprints.txt"

	awk -F'\t' 'NF>=8 && $8!="" {print $8}' "${ALL_RESULTS}" | sort -u >"${TMP_DIR}/jobs.txt"
	if (( WAIT_JOB == 1 )); then
		wait_jobs_terminal "${TMP_DIR}/jobs.txt"
	fi
	collect_toolcalls_and_evidences "${TMP_DIR}/jobs.txt"

	validate_thresholds

	echo "PASS L4-2"
	echo "n=${STATS_N}"
	echo "concurrency=${CONCURRENCY}"
	echo "idem_reuse_ratio=${IDEM_REUSE_RATIO}"
	echo "fingerprint_mode=${FINGERPRINT_MODE}"
	echo "unique_fingerprints=${STATS_UNIQUE_FINGERPRINTS}"
	echo "unique_incidents=${STATS_UNIQUE_INCIDENTS}"
	echo "unique_current_events=${STATS_UNIQUE_CURRENT_EVENTS}"
	echo "unique_jobs=${STATS_UNIQUE_JOBS}"
	echo "unique_toolcalls=${STATS_UNIQUE_TOOLCALLS}"
	echo "unique_evidences=${STATS_UNIQUE_EVIDENCES}"
	echo "ingest_reused=${STATS_INGEST_REUSED}"
	echo "run_already_running=${STATS_RUN_ALREADY_RUNNING}"
	echo "storm_run_id=${STORM_RUN_ID}"
	echo "sample_incident_id=${SAMPLE_INCIDENT_ID}"
	echo "sample_event_id=${SAMPLE_EVENT_ID}"
	echo "sample_job_id=${SAMPLE_JOB_ID}"
}

if [[ "${1:-}" == "--help" ]]; then
	usage
	exit 0
fi

if [[ "${1:-}" == "--worker" ]]; then
	if [[ -z "${2:-}" ]]; then
		exit 2
	fi
	worker_main "$2"
	exit 0
fi

run_parent
