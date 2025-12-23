#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
DEBUG="${DEBUG:-0}"

LAST_HTTP_CODE=""
LAST_BODY=""

SILENCE_ID=""
EVENT_ID_1=""
EVENT_ID_2=""
INCIDENT_ID=""
FINGERPRINT=""

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

extract_field() {
	local json="$1"
	shift
	local keys=("$@")
	local key value

	if need_cmd jq; then
		for key in "${keys[@]}"; do
			value="$(
				printf '%s' "${json}" | jq -r --arg k "${key}" '
					(.[$k] // .data[$k] // .silence[$k] // .data.silence[$k] //
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
	else
		for key in "${keys[@]}"; do
			value="$(printf '%s' "${json}" | sed -n "s/.*\"${key}\"[[:space:]]*:[[:space:]]*\"\\([^\"]*\\)\".*/\\1/p" | head -n 1)"
			if [[ -n "${value}" ]]; then
				printf '%s' "${value}"
				return 0
			fi
		done
	fi
	return 1
}

fail_l3() {
	local step="$1"
	local detail="${2:-non-2xx response}"
	local code="${3:-${LAST_HTTP_CODE}}"
	local body="${4:-${LAST_BODY}}"

	echo "FAIL L3 step=${step}"
	echo "detail=${detail}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	trim_2kb "${body}"
	echo
	echo "EOF"
	echo "silence_id=${SILENCE_ID:-NONE}"
	echo "event_id_1=${EVENT_ID_1:-NONE}"
	echo "event_id_2=${EVENT_ID_2:-NONE}"
	echo "incident_id=${INCIDENT_ID:-NONE}"
	echo "fingerprint=${FINGERPRINT:-NONE}"
	exit 1
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
	return 0
}

call_or_fail() {
	local step="$1"
	local method="$2"
	local url="$3"
	local body="${4:-}"

	if ! http_json "${method}" "${url}" "${body}"; then
		fail_l3 "${step}" "curl failed"
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		fail_l3 "${step}"
	fi
	debug "${step} code=${LAST_HTTP_CODE}"
}

assert_json_bool() {
	local step="$1"
	local expr="$2"
	local expect="$3"
	local parsed

	if ! need_cmd jq; then
		if [[ "${expect}" == "true" ]]; then
			printf '%s' "${LAST_BODY}" | grep -Eq "\"${expr}\"[[:space:]]*:[[:space:]]*true" || fail_l3 "${step}" "expected ${expr}=true"
		else
			printf '%s' "${LAST_BODY}" | grep -Eq "\"${expr}\"[[:space:]]*:[[:space:]]*false" || fail_l3 "${step}" "expected ${expr}=false"
		fi
		return 0
	fi

	parsed="$(printf '%s' "${LAST_BODY}" | jq -r "${expr}" 2>/dev/null || true)"
	if [[ "${parsed}" != "${expect}" ]]; then
		fail_l3 "${step}" "expected ${expr}=${expect}, got ${parsed:-EMPTY}"
	fi
}

rand="${RAND:-$RANDOM}"
now_epoch="$(date -u +%s)"
start_epoch="$((now_epoch - 60))"
end_epoch="$((now_epoch + 3600))"
FINGERPRINT="p1-l3-fp-${rand}"
service="p1-l3-svc-${rand}"

create_body=$(cat <<EOF
{"namespace":"default","enabled":true,"startsAt":{"seconds":${start_epoch},"nanos":0},"endsAt":{"seconds":${end_epoch},"nanos":0},"reason":"p1-l3-maintenance","createdBy":"tester","matchers":[{"key":"fingerprint","op":"=","value":"${FINGERPRINT}"}]}
EOF
)

call_or_fail "CreateSilence" POST "${BASE_URL}/v1/silences" "${create_body}"
SILENCE_ID="$(extract_field "${LAST_BODY}" "silenceID" "silence_id")" || true
if [[ -z "${SILENCE_ID}" ]]; then
	fail_l3 "CreateSilenceParseSilenceID" "silence_id missing"
fi

ingest1_body=$(cat <<EOF
{"idempotencyKey":"idem-p1-l3-ingest-a-${rand}","fingerprint":"${FINGERPRINT}","status":"firing","severity":"P1","service":"${service}","cluster":"prod-p1","namespace":"default","workload":"svc-p1","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
EOF
)
call_or_fail "IngestSilenced" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest1_body}"
EVENT_ID_1="$(extract_field "${LAST_BODY}" "eventID" "event_id")" || true
if [[ -z "${EVENT_ID_1}" ]]; then
	fail_l3 "IngestSilencedParseEventID" "event_id missing"
fi

if need_cmd jq; then
	silenced_flag="$(printf '%s' "${LAST_BODY}" | jq -r '.silenced // .data.silenced // empty' 2>/dev/null || true)"
	if [[ "${silenced_flag}" != "true" ]]; then
		fail_l3 "IngestSilencedAssertFlag" "expected silenced=true"
	fi
	resp_silence_id="$(printf '%s' "${LAST_BODY}" | jq -r '.silenceID // .data.silenceID // empty' 2>/dev/null || true)"
	if [[ "${resp_silence_id}" != "${SILENCE_ID}" ]]; then
		fail_l3 "IngestSilencedAssertSilenceID" "silence_id mismatch"
	fi
	inc_from_first="$(printf '%s' "${LAST_BODY}" | jq -r '.incidentID // .data.incidentID // empty' 2>/dev/null || true)"
	if [[ -n "${inc_from_first}" ]]; then
		fail_l3 "IngestSilencedAssertIncidentBlocked" "incident_id should be empty when silenced"
	fi
else
	printf '%s' "${LAST_BODY}" | grep -q '"silenced"[[:space:]]*:[[:space:]]*true' || fail_l3 "IngestSilencedAssertFlag" "expected silenced=true"
	printf '%s' "${LAST_BODY}" | grep -q "\"silenceID\"[[:space:]]*:[[:space:]]*\"${SILENCE_ID}\"" || fail_l3 "IngestSilencedAssertSilenceID" "silence_id mismatch"
fi

call_or_fail "ListCurrentSilenced" GET "${BASE_URL}/v1/alert-events:current?fingerprint=${FINGERPRINT}&offset=0&limit=20"
if need_cmd jq; then
	current_count="$(printf '%s' "${LAST_BODY}" | jq -r '.totalCount // .data.totalCount // 0' 2>/dev/null || true)"
	if [[ "${current_count}" != "1" ]]; then
		fail_l3 "ListCurrentSilencedCount" "expected totalCount=1, got ${current_count:-EMPTY}"
	fi
	cur_silenced="$(printf '%s' "${LAST_BODY}" | jq -r '(.events // .data.events // [])[0].isSilenced // empty' 2>/dev/null || true)"
	cur_silence_id="$(printf '%s' "${LAST_BODY}" | jq -r '(.events // .data.events // [])[0].silenceID // empty' 2>/dev/null || true)"
	if [[ "${cur_silenced}" != "true" ]] || [[ "${cur_silence_id}" != "${SILENCE_ID}" ]]; then
		fail_l3 "ListCurrentSilencedAssert" "current event is not silenced as expected"
	fi
fi

patch_body='{"enabled":false,"reason":"disable for regression"}'
call_or_fail "DisableSilence" PATCH "${BASE_URL}/v1/silences/${SILENCE_ID}" "${patch_body}"

ingest2_body=$(cat <<EOF
{"idempotencyKey":"idem-p1-l3-ingest-b-${rand}","fingerprint":"${FINGERPRINT}","status":"firing","severity":"P1","service":"${service}","cluster":"prod-p1","namespace":"default","workload":"svc-p1","lastSeenAt":{"seconds":$((now_epoch + 30)),"nanos":0}}
EOF
)
call_or_fail "IngestAfterDisable" POST "${BASE_URL}/v1/alert-events:ingest" "${ingest2_body}"
EVENT_ID_2="$(extract_field "${LAST_BODY}" "eventID" "event_id")" || true
INCIDENT_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id")" || true
if [[ -z "${EVENT_ID_2}" ]]; then
	fail_l3 "IngestAfterDisableParseEventID" "event_id missing"
fi
if [[ -z "${INCIDENT_ID}" ]]; then
	fail_l3 "IngestAfterDisableParseIncidentID" "incident_id missing"
fi

if need_cmd jq; then
	silenced_flag_2="$(printf '%s' "${LAST_BODY}" | jq -r '((.silenced // .data.silenced // false) | tostring)' 2>/dev/null || true)"
	silence_id_2="$(printf '%s' "${LAST_BODY}" | jq -r '.silenceID // .data.silenceID // empty' 2>/dev/null || true)"
	if [[ "${silenced_flag_2}" != "false" ]]; then
		fail_l3 "IngestAfterDisableAssertFlag" "expected silenced=false"
	fi
	if [[ -n "${silence_id_2}" ]]; then
		fail_l3 "IngestAfterDisableAssertSilenceID" "silence_id should be empty after disable"
	fi
fi

call_or_fail "ListCurrentAfterDisable" GET "${BASE_URL}/v1/alert-events:current?fingerprint=${FINGERPRINT}&offset=0&limit=20"
if need_cmd jq; then
	cur2_silenced="$(printf '%s' "${LAST_BODY}" | jq -r '(((.events // .data.events // [])[0].isSilenced // false) | tostring)' 2>/dev/null || true)"
	cur2_incident="$(printf '%s' "${LAST_BODY}" | jq -r '(.events // .data.events // [])[0].incidentID // empty' 2>/dev/null || true)"
	if [[ "${cur2_silenced}" != "false" ]]; then
		fail_l3 "ListCurrentAfterDisableAssertSilenced" "expected current isSilenced=false"
	fi
	if [[ "${cur2_incident}" != "${INCIDENT_ID}" ]]; then
		fail_l3 "ListCurrentAfterDisableAssertIncident" "current incident_id mismatch"
	fi
fi

echo "PASS L3"
echo "silence_id=${SILENCE_ID}"
echo "event_id_1=${EVENT_ID_1}"
echo "event_id_2=${EVENT_ID_2}"
echo "incident_id=${INCIDENT_ID}"
