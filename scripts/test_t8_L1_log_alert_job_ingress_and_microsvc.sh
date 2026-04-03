#!/usr/bin/env bash
set -euo pipefail

CURL="${CURL:-curl}"
DEBUG="${DEBUG:-0}"
WAIT_TIMEOUT_SEC="${WAIT_TIMEOUT_SEC:-90}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

ES_FAIL_PORT="${ES_FAIL_PORT:-$((19100 + RANDOM % 200))}"
ES_OK_PORT="${ES_OK_PORT:-$((ES_FAIL_PORT + 1))}"
WEBHOOK_PORT="${WEBHOOK_PORT:-$((ES_FAIL_PORT + 2))}"
JOB_METRICS_PORT="${JOB_METRICS_PORT:-$((ES_FAIL_PORT + 3))}"

LAST_HTTP_CODE=""
LAST_BODY=""
RULE_ID_SAMPLE=""
FINGERPRINT_SAMPLE=""

MOCK_PID=""
JOB_PID=""
MOCK_LOG_FILE=""
JOB_LOG_FILE=""
WEBHOOK_EVENTS_FILE=""
ES_EVENTS_FILE=""
JOB_CONFIG_FILE=""

ES_USER="${ES_USER:-esuser}"
ES_PASS="${ES_PASS:-espass123}"

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

	echo "FAIL T8-L1 step=${step}"
	echo "http_code=${code:-UNKNOWN}"
	echo "body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "rule_id=${RULE_ID_SAMPLE:-NONE}"
	echo "fingerprint=${FINGERPRINT_SAMPLE:-NONE}"
	if [[ -n "${JOB_LOG_FILE}" ]]; then
		echo "job_log_tail<<EOF"
		tail -n 100 "${JOB_LOG_FILE}" 2>/dev/null | head -c 2048
		echo
		echo "EOF"
	fi
	if [[ -n "${MOCK_LOG_FILE}" ]]; then
		echo "mock_log_tail<<EOF"
		tail -n 100 "${MOCK_LOG_FILE}" 2>/dev/null | head -c 2048
		echo
		echo "EOF"
	fi
	exit 1
}

http_get() {
	local url="$1"
	local tmp_body tmp_err code rc curl_err

	tmp_body="$(mktemp)"
	tmp_err="$(mktemp)"

	set +e
	code="$("${CURL}" -sS -o "${tmp_body}" -w "%{http_code}" "${url}" 2>"${tmp_err}")"
	rc=$?
	set -e

	LAST_BODY="$(cat "${tmp_body}")"
	curl_err="$(cat "${tmp_err}")"
	rm -f "${tmp_body}" "${tmp_err}"

	if (( rc != 0 )); then
		LAST_HTTP_CODE="CURL_${rc}"
		LAST_BODY="${curl_err}"
		return 1
	fi

	LAST_HTTP_CODE="${code}"
	return 0
}

metric_sum() {
	local metric_name="$1"
	local body="$2"
	printf '%s\n' "${body}" | awk -v name="${metric_name}" '
		$1 ~ ("^" name "(\\{|$)") { sum += $NF }
		END { printf "%.0f\n", sum + 0 }
	'
}

cleanup() {
	if [[ -n "${JOB_PID}" ]]; then
		kill "${JOB_PID}" >/dev/null 2>&1 || true
		wait "${JOB_PID}" >/dev/null 2>&1 || true
	fi
	if [[ -n "${MOCK_PID}" ]]; then
		kill "${MOCK_PID}" >/dev/null 2>&1 || true
		wait "${MOCK_PID}" >/dev/null 2>&1 || true
	fi
	rm -f "${MOCK_LOG_FILE:-}" "${JOB_LOG_FILE:-}" "${WEBHOOK_EVENTS_FILE:-}" "${ES_EVENTS_FILE:-}" "${JOB_CONFIG_FILE:-}"
}
trap cleanup EXIT

start_mock_servers() {
	local pybin
	if need_cmd python3; then
		pybin="python3"
	elif need_cmd python; then
		pybin="python"
	else
		fail_step "Precheck.MissingPython" "MISSING_PYTHON" "python3/python not found"
	fi

	"${pybin}" -u - <<'PY' \
		"${ES_FAIL_PORT}" "${ES_OK_PORT}" "${WEBHOOK_PORT}" \
		"${WEBHOOK_EVENTS_FILE}" "${ES_EVENTS_FILE}" "${ES_USER}" "${ES_PASS}" >"${MOCK_LOG_FILE}" 2>&1 &
import base64
import datetime
import json
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

es_fail_port = int(sys.argv[1])
es_ok_port = int(sys.argv[2])
webhook_port = int(sys.argv[3])
webhook_events_file = sys.argv[4]
es_events_file = sys.argv[5]
es_user = sys.argv[6]
es_pass = sys.argv[7]

expected_auth = ""
if es_user:
    expected_auth = "Basic " + base64.b64encode(f"{es_user}:{es_pass}".encode("utf-8")).decode("utf-8")

def now_ts():
    return datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")

def log_es_event(item):
    with open(es_events_file, "a", encoding="utf-8") as fp:
        fp.write(json.dumps(item, ensure_ascii=False) + "\n")

def write_webhook(payload):
    with open(webhook_events_file, "a", encoding="utf-8") as fp:
        fp.write(json.dumps(payload, ensure_ascii=False) + "\n")

def ingress_5xx_docs():
    ts = now_ts()
    docs = []
    for idx in range(1, 5):
        docs.append({
            "@timestamp": ts,
            "event": {"runenv": "prod", "svcType": "ingress"},
            "destination": {"domain": "api.prod.local"},
            "http": {
                "request": {"uri_path": "/checkout"},
                "response": {"status_code": 502},
            },
            "nginx": {
                "upstream": {
                    "address": "10.9.0.1:8080",
                    "response": {"time": 0.4},
                }
            },
            "Trace": {"Id": f"trace-5xx-{idx}"},
            "user_agent": {"request_id": f"req-5xx-{idx}"},
        })
    return docs

def ingress_slow_docs():
    ts = now_ts()
    docs = []
    for idx in range(1, 5):
        docs.append({
            "@timestamp": ts,
            "event": {"runenv": "prod", "svcType": "ingress"},
            "destination": {"domain": "api.prod.local"},
            "http": {
                "request": {"uri_path": "/payment"},
                "response": {"status_code": 200},
            },
            "nginx": {
                "upstream": {
                    "address": "10.9.0.2:8080",
                    "response": {"time": 2.8},
                }
            },
            "Trace": {"Id": f"trace-slow-{idx}"},
            "user_agent": {"request_id": f"req-slow-{idx}"},
        })
    return docs

def microsvc_docs():
    ts = now_ts()
    docs = []
    for idx in range(1, 5):
        docs.append({
            "@timestamp": ts,
            "event": {"dataset": "order-svc", "runenv": "green"},
            "k8s": {"ns": "payments"},
            "Level": "ERROR",
            "Msg": f"Authorization token secret headers failure for order {1000+idx} user {2000+idx} trace {3000+idx}",
            "Trace": {"Id": f"trace-micro-{idx}"},
        })
    return docs

class BaseHandler(BaseHTTPRequestHandler):
    def healthz(self):
        if self.path == "/healthz":
            self.send_response(200)
            self.end_headers()
            self.wfile.write(b"ok")
            return True
        return False

    def require_auth(self):
        if not expected_auth:
            return True
        got = self.headers.get("Authorization", "")
        return got == expected_auth

    def log_message(self, fmt, *args):
        return

class ESFailHandler(BaseHandler):
    def do_GET(self):
        if self.healthz():
            return
        self.send_response(404)
        self.end_headers()

    def do_POST(self):
        if self.healthz():
            return
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length) if length > 0 else b"{}"
        rule_query = ""
        try:
            payload = json.loads(body.decode("utf-8"))
            filters = payload.get("query", {}).get("bool", {}).get("filter", [])
            if filters and isinstance(filters, list):
                for item in filters:
                    if "query_string" in item:
                        rule_query = item.get("query_string", {}).get("query", "")
                        break
        except Exception:
            pass
        auth_ok = self.require_auth()
        log_es_event({"server": "fail", "path": self.path, "query": rule_query, "auth_ok": auth_ok})
        if not auth_ok:
            self.send_response(401)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"error":"unauthorized"}')
            return
        self.send_response(500)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"error":"mock failover"}')

class ESOKHandler(BaseHandler):
    def do_GET(self):
        if self.healthz():
            return
        self.send_response(404)
        self.end_headers()

    def do_POST(self):
        if self.healthz():
            return
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length) if length > 0 else b"{}"
        query = ""
        try:
            payload = json.loads(body.decode("utf-8"))
            filters = payload.get("query", {}).get("bool", {}).get("filter", [])
            if filters and isinstance(filters, list):
                for item in filters:
                    if "query_string" in item:
                        query = item.get("query_string", {}).get("query", "")
                        break
        except Exception:
            query = ""

        auth_ok = self.require_auth()
        log_es_event({"server": "ok", "path": self.path, "query": query, "auth_ok": auth_ok})
        if not auth_ok:
            self.send_response(401)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"error":"unauthorized"}')
            return

        query_lower = query.lower()
        docs = []
        if "http.response.status_code:[500 to 599]" in query_lower:
            docs = ingress_5xx_docs()
        elif "nginx.upstream.response.time:[2 to *]" in query_lower:
            docs = ingress_slow_docs()
        elif "level:error" in query_lower:
            docs = microsvc_docs()

        hits = [{"_source": item} for item in docs]
        response = {
            "hits": {
                "total": {"value": len(hits), "relation": "eq"},
                "hits": hits,
            }
        }
        encoded = json.dumps(response).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

class WebhookHandler(BaseHandler):
    def do_GET(self):
        if self.healthz():
            return
        self.send_response(404)
        self.end_headers()

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length) if length > 0 else b"{}"
        try:
            payload = json.loads(body.decode("utf-8"))
        except Exception:
            payload = {}
        write_webhook(payload)
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"ok":true}')

def serve(server):
    server.serve_forever()

es_fail_server = ThreadingHTTPServer(("127.0.0.1", es_fail_port), ESFailHandler)
es_ok_server = ThreadingHTTPServer(("127.0.0.1", es_ok_port), ESOKHandler)
webhook_server = ThreadingHTTPServer(("127.0.0.1", webhook_port), WebhookHandler)

for item in [es_fail_server, es_ok_server, webhook_server]:
    thread = threading.Thread(target=serve, args=(item,), daemon=True)
    thread.start()

while True:
    time.sleep(1)
PY

	MOCK_PID="$!"

	local deadline now
	deadline="$(( $(date +%s) + 20 ))"
	while true; do
		if "${CURL}" -sS "http://127.0.0.1:${ES_FAIL_PORT}/healthz" >/dev/null 2>&1 && \
			"${CURL}" -sS "http://127.0.0.1:${ES_OK_PORT}/healthz" >/dev/null 2>&1 && \
			"${CURL}" -sS "http://127.0.0.1:${WEBHOOK_PORT}/healthz" >/dev/null 2>&1; then
			return 0
		fi
		now="$(date +%s)"
		if (( now > deadline )); then
			LAST_HTTP_CODE="MOCK_START_TIMEOUT"
			LAST_BODY="$(cat "${MOCK_LOG_FILE}" 2>/dev/null || true)"
			fail_step "StartMockServers"
		fi
		sleep 0.5
	done
}

start_job() {
	(
		cd "${REPO_ROOT}" && \
			CONFIG_PATH="${JOB_CONFIG_FILE}" \
			ES_URLS="http://127.0.0.1:${ES_FAIL_PORT},http://127.0.0.1:${ES_OK_PORT}" \
			ES_USER="${ES_USER}" \
			ES_PASS="${ES_PASS}" \
			RCA_BASE_URL="http://127.0.0.1:${WEBHOOK_PORT}" \
			GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn \
			go run ./tools/log-alert-job --config "${JOB_CONFIG_FILE}" --tick-seconds 1 --metrics-addr "127.0.0.1:${JOB_METRICS_PORT}"
	) >"${JOB_LOG_FILE}" 2>&1 &
	JOB_PID="$!"

	local deadline now
	deadline="$(( $(date +%s) + 30 ))"
	while true; do
		if "${CURL}" -sS "http://127.0.0.1:${JOB_METRICS_PORT}/healthz" >/dev/null 2>&1; then
			return 0
		fi
		if ! kill -0 "${JOB_PID}" >/dev/null 2>&1; then
			LAST_HTTP_CODE="JOB_EXITED"
			LAST_BODY="$(cat "${JOB_LOG_FILE}" 2>/dev/null || true)"
			fail_step "StartJob"
		fi
		now="$(date +%s)"
		if (( now > deadline )); then
			LAST_HTTP_CODE="JOB_START_TIMEOUT"
			LAST_BODY="$(cat "${JOB_LOG_FILE}" 2>/dev/null || true)"
			fail_step "StartJob"
		fi
		sleep 0.5
	done
}

assert_events() {
	local line_count
	line_count="$(wc -l <"${WEBHOOK_EVENTS_FILE}" | tr -d ' ')"
	if [[ "${line_count}" != "3" ]]; then
		LAST_HTTP_CODE="ASSERT_WEBHOOK_COUNT"
		LAST_BODY="expected webhook_count=3, got=${line_count}"
		fail_step "AssertWebhookCount"
	fi

	local count_5xx count_slow count_microsvc
	count_5xx="$(jq -r 'select(.annotations.rule_id=="ingress_5xx_spike") | .annotations.rule_id' "${WEBHOOK_EVENTS_FILE}" | wc -l | tr -d ' ')"
	count_slow="$(jq -r 'select(.annotations.rule_id=="ingress_slow_spike") | .annotations.rule_id' "${WEBHOOK_EVENTS_FILE}" | wc -l | tr -d ' ')"
	count_microsvc="$(jq -r 'select(.annotations.rule_id=="microsvc_error_cluster") | .annotations.rule_id' "${WEBHOOK_EVENTS_FILE}" | wc -l | tr -d ' ')"
	if [[ "${count_5xx}" != "1" ]]; then
		LAST_HTTP_CODE="ASSERT_5XX_COUNT"
		LAST_BODY="expected ingress_5xx_spike fire once, got=${count_5xx}"
		fail_step "AssertIngress5xxCount"
	fi
	if [[ "${count_slow}" != "1" ]]; then
		LAST_HTTP_CODE="ASSERT_SLOW_COUNT"
		LAST_BODY="expected ingress_slow_spike fire once, got=${count_slow}"
		fail_step "AssertIngressSlowCount"
	fi
	if [[ "${count_microsvc}" != "1" ]]; then
		LAST_HTTP_CODE="ASSERT_MICROSVC_COUNT"
		LAST_BODY="expected microsvc_error_cluster fire once, got=${count_microsvc}"
		fail_step "AssertMicrosvcCount"
	fi

	local missing_trace
	missing_trace="$(jq -r 'select((.annotations.sample_trace_ids // "") == "") | .annotations.rule_id' "${WEBHOOK_EVENTS_FILE}" | wc -l | tr -d ' ')"
	if [[ "${missing_trace}" != "0" ]]; then
		LAST_HTTP_CODE="ASSERT_SAMPLE_TRACE"
		LAST_BODY="sample_trace_ids missing in webhook payload"
		fail_step "AssertSampleTraceIDs"
	fi

	local ingress_missing_request
	ingress_missing_request="$(jq -r 'select((.annotations.rule_id=="ingress_5xx_spike" or .annotations.rule_id=="ingress_slow_spike") and (.annotations.sample_request_ids // "") == "") | .annotations.rule_id' "${WEBHOOK_EVENTS_FILE}" | wc -l | tr -d ' ')"
	if [[ "${ingress_missing_request}" != "0" ]]; then
		LAST_HTTP_CODE="ASSERT_SAMPLE_REQUEST"
		LAST_BODY="sample_request_ids missing for ingress payload"
		fail_step "AssertSampleRequestIDs"
	fi

	local microsvc_examples
	microsvc_examples="$(jq -r 'select(.annotations.rule_id=="microsvc_error_cluster") | .annotations.msg_examples // ""' "${WEBHOOK_EVENTS_FILE}" | head -n 1)"
	if [[ -z "${microsvc_examples}" ]]; then
		LAST_HTTP_CODE="ASSERT_MSG_EXAMPLES"
		LAST_BODY="msg_examples missing for microsvc payload"
		fail_step "AssertMsgExamples"
	fi

	if printf '%s' "${microsvc_examples}" | awk 'length($0) > 512 { exit 0 } END { exit 1 }'; then
		LAST_HTTP_CODE="ASSERT_MSG_EXAMPLES_LEN"
		LAST_BODY="msg_examples exceeds truncation limit"
		fail_step "AssertMsgExamplesTruncated"
	fi

	if grep -Eiq '(secret|token|authorization|headers)' "${WEBHOOK_EVENTS_FILE}"; then
		LAST_HTTP_CODE="ASSERT_REDACTION"
		LAST_BODY="sensitive words leaked in webhook payload"
		fail_step "AssertSensitiveRedaction"
	fi

	RULE_ID_SAMPLE="$(jq -r '.annotations.rule_id // empty' "${WEBHOOK_EVENTS_FILE}" | head -n 1)"
	FINGERPRINT_SAMPLE="$(jq -r '.fingerprint // empty' "${WEBHOOK_EVENTS_FILE}" | head -n 1)"
	if [[ -z "${RULE_ID_SAMPLE}" || -z "${FINGERPRINT_SAMPLE}" ]]; then
		LAST_HTTP_CODE="ASSERT_IDS"
		LAST_BODY="failed to parse rule_id/fingerprint from webhook events"
		fail_step "AssertIDs"
	fi
}

if ! need_cmd jq; then
	fail_step "Precheck.MissingJQ" "MISSING_JQ" "jq is required"
fi

MOCK_LOG_FILE="$(mktemp)"
JOB_LOG_FILE="$(mktemp)"
WEBHOOK_EVENTS_FILE="$(mktemp)"
ES_EVENTS_FILE="$(mktemp)"
JOB_CONFIG_FILE="$(mktemp)"

cat >"${JOB_CONFIG_FILE}" <<YAML
job:
  tick_seconds: 1
  metrics_addr: "127.0.0.1:${JOB_METRICS_PORT}"
  max_docs_per_rule: 100

es:
  urls:
    - "http://127.0.0.1:9"
  username: ""
  password: ""
  timeout_ms: 2000
  max_retries: 2

rca:
  base_url: "http://127.0.0.1:1"
  timeout_ms: 2000

fields:
  timestamp: "@timestamp"
  trace_id: "Trace.Id"
  request_id: "user_agent.request_id"
  msg: "Msg"
  message: "message"

indices:
  ingress: "logstash-prod-ingress*"
  microsvc: "logstash-prod-microsvc*"

rules:
  - id: ingress_5xx_spike
    enabled: true
    kind: ingress_5xx_spike
    index_ref: ingress
    window_seconds: 300
    cooldown_seconds: 60
    selector:
      query_string: "event.runenv:prod AND event.svcType:ingress AND http.response.status_code:[500 TO 599]"
    group_by:
      - destination.domain
      - http.request.uri_path
      - nginx.upstream.address
    trigger:
      type: count_gte
      value: 3
    samples: 3
    rca_event:
      severity: P1
      summary_template: "ingress5xx count={{count}} domain={{domain}}"
      hints:
        include_es_query: true

  - id: ingress_slow_spike
    enabled: true
    kind: ingress_slow_spike
    index_ref: ingress
    window_seconds: 300
    cooldown_seconds: 60
    selector:
      query_string: "event.runenv:prod AND event.svcType:ingress AND nginx.upstream.response.time:[2 TO *]"
    group_by:
      - destination.domain
      - http.request.uri_path
      - nginx.upstream.address
    trigger:
      type: count_gte
      value: 3
    samples: 3
    rca_event:
      severity: P2
      summary_template: "ingressSlow count={{count}} domain={{domain}}"
      hints:
        include_es_query: true

  - id: microsvc_error_cluster
    enabled: true
    kind: microsvc_error_cluster
    index_ref: microsvc
    window_seconds: 300
    cooldown_seconds: 60
    selector:
      query_string: "Level:ERROR AND event.runenv:green"
    group_by:
      - k8s.ns
      - event.dataset
      - msg_template_hash
    trigger:
      type: count_gte
      value: 3
    samples: 3
    rca_event:
      severity: P2
      summary_template: "microsvcError count={{count}} ns={{namespace}}"
      hints:
        include_es_query: true
YAML

start_mock_servers
start_job

deadline="$(( $(date +%s) + WAIT_TIMEOUT_SEC ))"
cooldown_total="0"
failover_total="0"
webhook_total="0"

while true; do
	if ! kill -0 "${JOB_PID}" >/dev/null 2>&1; then
		LAST_HTTP_CODE="JOB_EXITED"
		LAST_BODY="$(cat "${JOB_LOG_FILE}" 2>/dev/null || true)"
		fail_step "WaitForTickResults"
	fi

	if [[ -s "${WEBHOOK_EVENTS_FILE}" ]]; then
		webhook_total="$(wc -l <"${WEBHOOK_EVENTS_FILE}" | tr -d ' ')"
	fi

	if http_get "http://127.0.0.1:${JOB_METRICS_PORT}/metrics"; then
		if [[ "${LAST_HTTP_CODE}" == "200" ]]; then
			cooldown_total="$(metric_sum "log_alert_cooldown_suppressed_total" "${LAST_BODY}")"
			failover_total="$(metric_sum "log_alert_es_failover_total" "${LAST_BODY}")"
		fi
	fi

	debug "webhook_total=${webhook_total} cooldown_total=${cooldown_total} failover_total=${failover_total}"
	if [[ "${webhook_total}" =~ ^[0-9]+$ ]] && (( webhook_total >= 3 )) && \
		[[ "${cooldown_total}" =~ ^[0-9]+$ ]] && (( cooldown_total >= 3 )) && \
		[[ "${failover_total}" =~ ^[0-9]+$ ]] && (( failover_total >= 1 )); then
		break
	fi

	if (( $(date +%s) > deadline )); then
		LAST_HTTP_CODE="WAIT_TIMEOUT"
		LAST_BODY="webhook_total=${webhook_total} cooldown_total=${cooldown_total} failover_total=${failover_total}"
		fail_step "WaitForTickResults"
	fi
	sleep 1
done

assert_events

if ! grep -q '"server": "fail"' "${ES_EVENTS_FILE}" && ! grep -q '"server":"fail"' "${ES_EVENTS_FILE}"; then
	LAST_HTTP_CODE="ASSERT_FAILOVER_ES_FAIL_NODE"
	LAST_BODY="mock fail ES node did not receive requests"
	fail_step "AssertFailoverFailNode"
fi

if ! grep -q '"server": "ok"' "${ES_EVENTS_FILE}" && ! grep -q '"server":"ok"' "${ES_EVENTS_FILE}"; then
	LAST_HTTP_CODE="ASSERT_FAILOVER_ES_OK_NODE"
	LAST_BODY="mock success ES node did not receive requests"
	fail_step "AssertFailoverOKNode"
fi

if jq -e 'select(.auth_ok == false)' "${ES_EVENTS_FILE}" >/dev/null 2>&1; then
	LAST_HTTP_CODE="ASSERT_ES_BASIC_AUTH"
	LAST_BODY="at least one ES request missed basic auth"
	fail_step "AssertESBasicAuth"
fi

if [[ "${cooldown_total}" -lt 3 ]]; then
	LAST_HTTP_CODE="ASSERT_COOLDOWN"
	LAST_BODY="cooldown metric expected >=3, got=${cooldown_total}"
	fail_step "AssertCooldownMetric"
fi

if [[ "${failover_total}" -lt 1 ]]; then
	LAST_HTTP_CODE="ASSERT_FAILOVER_METRIC"
	LAST_BODY="failover metric expected >=1, got=${failover_total}"
	fail_step "AssertFailoverMetric"
fi

echo "PASS T8-L1 rule_5xx=1 rule_slow=1 rule_microsvc=1 failover=${failover_total} cooldown=${cooldown_total}"
echo "rule_id=${RULE_ID_SAMPLE}"
echo "fingerprint=${FINGERPRINT_SAMPLE}"
