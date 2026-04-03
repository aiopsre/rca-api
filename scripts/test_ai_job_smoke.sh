#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
CURL="${CURL:-curl}"
SCOPES="${SCOPES:-*}"
AUTO_CREATE_INCIDENT="${AUTO_CREATE_INCIDENT:-1}"
ORCHESTRATOR_INSTANCE_ID="${ORCHESTRATOR_INSTANCE_ID:-test-instance}"

need_cmd() { command -v "$1" >/dev/null 2>&1; }

http_json() {
  # Usage: http_json METHOD URL [JSON_BODY]
  local method="$1"; shift
  local url="$1"; shift
  local body="${1:-}"
  local -a headers
  headers=(-H "X-Scopes: ${SCOPES}")
  if [[ "${method}" == "POST" ]] && [[ "${url}" =~ /v1/ai/jobs/[^/]+/(start|tool-calls|finalize|cancel|heartbeat)$ ]]; then
    headers+=(-H "X-Orchestrator-Instance-ID: ${ORCHESTRATOR_INSTANCE_ID}")
  fi

  if [[ -n "$body" ]]; then
    $CURL -sS -i -X "$method" "$url" \
      -H 'Content-Type: application/json' \
      "${headers[@]}" \
      -d "$body"
  else
    $CURL -sS -i -X "$method" "$url" \
      "${headers[@]}"
  fi
}

status_code() { awk 'NR==1 {print $2}'; }

body_only() { awk 'BEGIN{p=0} /^\r?$/{p=1; next} {if(p) print}'; }

extract_field() {
  local json="$1"
  local field="$2"
  if need_cmd jq; then
    echo "$json" | jq -r ".${field} // .data.${field} // empty"
  else
    echo "$json" | sed -n "s/.*\"${field}\":\"\\([^\"]*\\)\".*/\\1/p"
  fi
}

json_string() {
  local raw="$1"
  if need_cmd jq; then
    printf '%s' "$raw" | jq -Rs .
  else
    printf '"%s"' "$(printf '%s' "$raw" | sed 's/\\/\\\\/g; s/"/\\"/g')"
  fi
}

assert_2xx() {
  local step="$1"
  local resp="$2"
  local code
  code="$(echo "$resp" | status_code)"
  if [[ ! "$code" =~ ^2 ]]; then
    echo "FAIL ${step}: HTTP ${code}"
    echo "---- response ----"
    echo "$resp"
    echo "------------------"
    exit 1
  fi
}

assert_contains() {
  local step="$1"
  local text="$2"
  local pattern="$3"
  if ! echo "$text" | grep -q "$pattern"; then
    echo "FAIL ${step}: expected pattern ${pattern}"
    echo "---- body ----"
    echo "$text"
    echo "-------------"
    exit 1
  fi
}

assert_not_contains() {
  local step="$1"
  local text="$2"
  local pattern="$3"
  if echo "$text" | grep -q "$pattern"; then
    echo "FAIL ${step}: unexpected pattern ${pattern}"
    echo "---- body ----"
    echo "$text"
    echo "-------------"
    exit 1
  fi
}

create_incident() {
  local rand="$1"
  local body
  body=$(cat <<EOF
{"namespace":"default","workloadKind":"Deployment","workloadName":"ai-smoke-${rand}","service":"ai-svc-${rand}","severity":"P1"}
EOF
)
  local resp json id
  resp="$(http_json POST "${BASE_URL}/v1/incidents" "$body")"
  assert_2xx "CreateIncident" "$resp"
  json="$(echo "$resp" | body_only)"
  id="$(extract_field "$json" "incidentID")"
  if [[ -z "$id" ]]; then
    echo "FAIL CreateIncident: cannot parse incidentID"
    echo "$json"
    exit 1
  fi
  echo "$id"
}

echo "BASE_URL=${BASE_URL}"
echo "SCOPES=${SCOPES}"

rand="${RAND:-$RANDOM}"
incident_id="${INCIDENT_ID:-}"

if [[ -z "${incident_id}" ]] && [[ "${AUTO_CREATE_INCIDENT}" == "1" ]]; then
  incident_id="$(create_incident "$rand")"
fi
if [[ -z "${incident_id}" ]]; then
  echo "FAIL: incident_id is empty, set INCIDENT_ID or AUTO_CREATE_INCIDENT=1"
  exit 1
fi
echo "incident_id=${incident_id}"

now_epoch="$(date -u +%s)"
start_epoch="$((now_epoch - 1800))"
idem_key="idem-ai-run-${rand}"

run_body=$(cat <<EOF
{"incidentID":"${incident_id}","idempotencyKey":"${idem_key}","pipeline":"basic_rca","trigger":"manual","timeRangeStart":{"seconds":${start_epoch},"nanos":0},"timeRangeEnd":{"seconds":${now_epoch},"nanos":0},"inputHintsJSON":"{\"hint\":\"ai smoke\"}","createdBy":"system"}
EOF
)

run_resp="$(http_json POST "${BASE_URL}/v1/incidents/${incident_id}/ai:run" "$run_body")"
assert_2xx "RunAIJob" "$run_resp"
run_json="$(echo "$run_resp" | body_only)"
job_id="$(extract_field "$run_json" "jobID")"
if [[ -z "${job_id}" ]]; then
  echo "FAIL RunAIJob: cannot parse jobID"
  echo "$run_json"
  exit 1
fi
echo "PASS RunAIJob job_id=${job_id}"

run2_resp="$(http_json POST "${BASE_URL}/v1/incidents/${incident_id}/ai:run" "$run_body")"
assert_2xx "RunAIJobIdempotent" "$run2_resp"
run2_json="$(echo "$run2_resp" | body_only)"
job_id2="$(extract_field "$run2_json" "jobID")"
if [[ "${job_id}" != "${job_id2}" ]]; then
  echo "FAIL RunAIJobIdempotent: job_id changed ${job_id} -> ${job_id2}"
  exit 1
fi

list_jobs_resp="$(http_json GET "${BASE_URL}/v1/incidents/${incident_id}/ai?offset=0&limit=20")"
assert_2xx "ListIncidentAIJobs" "$list_jobs_resp"
list_jobs_json="$(echo "$list_jobs_resp" | body_only)"
assert_contains "ListIncidentAIJobs" "$list_jobs_json" "\"jobID\":\"${job_id}\""

get_job_resp="$(http_json GET "${BASE_URL}/v1/ai/jobs/${job_id}")"
assert_2xx "GetAIJob" "$get_job_resp"
get_job_json="$(echo "$get_job_resp" | body_only)"
assert_contains "GetAIJob" "$get_job_json" "\"jobID\":\"${job_id}\""
assert_contains "GetAIJob" "$get_job_json" "\"incidentID\":\"${incident_id}\""

start_resp="$(http_json POST "${BASE_URL}/v1/ai/jobs/${job_id}/start" '{}')"
assert_2xx "StartAIJob" "$start_resp"

get_running_resp="$(http_json GET "${BASE_URL}/v1/ai/jobs/${job_id}")"
assert_2xx "GetAIJobRunning" "$get_running_resp"
get_running_json="$(echo "$get_running_resp" | body_only)"
assert_contains "GetAIJobRunning" "$get_running_json" "\"status\":\"running\""

tc1_req='{"query":"up"}'
tc1_rsp='{"status":"ok","rows":12}'
tc1_body=$(cat <<EOF
{"jobID":"${job_id}","seq":1,"nodeName":"metrics_specialist","toolName":"evidence.queryMetrics","requestJSON":$(json_string "$tc1_req"),"responseJSON":$(json_string "$tc1_rsp"),"status":"ok","latencyMs":12,"evidenceIDs":["evidence-smoke-1"]}
EOF
)
tc1_resp="$(http_json POST "${BASE_URL}/v1/ai/jobs/${job_id}/tool-calls" "$tc1_body")"
assert_2xx "CreateAIToolCall#1" "$tc1_resp"
tc1_json="$(echo "$tc1_resp" | body_only)"
tool_call_id1="$(extract_field "$tc1_json" "toolCallID")"
if [[ -z "${tool_call_id1}" ]]; then
  echo "FAIL CreateAIToolCall#1: cannot parse toolCallID"
  echo "$tc1_json"
  exit 1
fi

tc2_req='{"query":"{app=\"demo\"} |= \"error\""}'
tc2_rsp='{"status":"error","reason":"mock timeout"}'
tc2_body=$(cat <<EOF
{"jobID":"${job_id}","seq":2,"nodeName":"logs_specialist","toolName":"evidence.queryLogs","requestJSON":$(json_string "$tc2_req"),"responseJSON":$(json_string "$tc2_rsp"),"status":"error","latencyMs":30,"errorMessage":"mock timeout","evidenceIDs":["evidence-smoke-2"]}
EOF
)
tc2_resp="$(http_json POST "${BASE_URL}/v1/ai/jobs/${job_id}/tool-calls" "$tc2_body")"
assert_2xx "CreateAIToolCall#2" "$tc2_resp"

list_tc_resp="$(http_json GET "${BASE_URL}/v1/ai/jobs/${job_id}/tool-calls?offset=0&limit=50")"
assert_2xx "ListAIToolCalls" "$list_tc_resp"
list_tc_json="$(echo "$list_tc_resp" | body_only)"
assert_contains "ListAIToolCalls" "$list_tc_json" "\"seq\":1"
assert_contains "ListAIToolCalls" "$list_tc_json" "\"seq\":2"

list_tc_seq_resp="$(http_json GET "${BASE_URL}/v1/ai/jobs/${job_id}/tool-calls?offset=0&limit=50&seq=1")"
assert_2xx "ListAIToolCallsBySeq" "$list_tc_seq_resp"
list_tc_seq_json="$(echo "$list_tc_seq_resp" | body_only)"
assert_contains "ListAIToolCallsBySeq" "$list_tc_seq_json" "\"seq\":1"
assert_not_contains "ListAIToolCallsBySeq" "$list_tc_seq_json" "\"seq\":2"

diagnosis_json='{"summary":"ai smoke summary","root_cause":{"category":"app","statement":"error spike after change","confidence":0.9,"evidence_ids":["evidence-smoke-1","evidence-smoke-2"]},"timeline":[{"t":"2026-02-07T00:00:00Z","event":"alert_fired","ref":"alert-smoke"}],"hypotheses":[{"statement":"error spike after change","confidence":0.9,"supporting_evidence_ids":["evidence-smoke-1","evidence-smoke-2"],"missing_evidence":[]}],"recommendations":[{"type":"readonly_check","action":"inspect error logs","risk":"low"}],"unknowns":[],"next_steps":["watch error rate"]}'
finalize_body=$(cat <<EOF
{"jobID":"${job_id}","status":"succeeded","outputSummary":"ai smoke summary","diagnosisJSON":$(json_string "$diagnosis_json"),"evidenceIDs":["evidence-smoke-1","evidence-smoke-2"]}
EOF
)
finalize_resp="$(http_json POST "${BASE_URL}/v1/ai/jobs/${job_id}/finalize" "$finalize_body")"
assert_2xx "FinalizeAIJob" "$finalize_resp"

get_done_resp="$(http_json GET "${BASE_URL}/v1/ai/jobs/${job_id}")"
assert_2xx "GetAIJobDone" "$get_done_resp"
get_done_json="$(echo "$get_done_resp" | body_only)"
assert_contains "GetAIJobDone" "$get_done_json" "\"status\":\"succeeded\""
assert_contains "GetAIJobDone" "$get_done_json" "\"outputJSON\":"
assert_contains "GetAIJobDone" "$get_done_json" "\"evidenceIDsJSON\":"

echo "PASS ai job smoke incident_id=${incident_id} job_id=${job_id}"
