#!/usr/bin/env bash
set -euo pipefail

SCOPES="${SCOPES:-*}"
CURL="${CURL:-curl}"
DEBUG="${DEBUG:-0}"
WAIT_SECONDS="${WAIT_SECONDS:-8}"
POLL_TIMEOUT_SEC="${POLL_TIMEOUT_SEC:-20}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
APISERVER_CONFIG="${APISERVER_CONFIG:-${REPO_ROOT}/configs/rca-apiserver.yaml}"
APISERVER_CMD_BASE="${APISERVER_CMD_BASE:-GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn go run ./cmd/rca-apiserver}"

REDIS_ADDR="${REDIS_ADDR:-192.168.39.2:6379}"
REDIS_DB="${REDIS_DB:-0}"
REDIS_PASSWORD="${REDIS_PASSWORD:-Az123456_}"
REDIS_FAIL_OPEN="${REDIS_FAIL_OPEN:-true}"
REDIS_TOPIC_PREFIX="${REDIS_TOPIC_PREFIX:-rca:ai_job_queue_signal:r1}"

MYSQL_ADDR="${MYSQL_ADDR:-192.168.39.1:3306}"
MYSQL_USERNAME="${MYSQL_USERNAME:-root}"
MYSQL_PASSWORD="${MYSQL_PASSWORD:-Az123456_}"
MYSQL_DATABASE_PREFIX="${MYSQL_DATABASE_PREFIX:-rca_r1_lp}"

PORT_A_REDIS="${PORT_A_REDIS:-16555}"
PORT_B_REDIS="${PORT_B_REDIS:-16556}"
PORT_A_FALLBACK="${PORT_A_FALLBACK:-16655}"
PORT_B_FALLBACK="${PORT_B_FALLBACK:-16656}"

LAST_HTTP_CODE=""
LAST_BODY=""
INCIDENT_ID=""
JOB_ID=""
SERVER_A_PID=""
SERVER_B_PID=""
SERVER_A_LOG=""
SERVER_B_LOG=""
SERVER_A_CONFIG=""
SERVER_B_CONFIG=""
BASE_URL_A=""
BASE_URL_B=""
CURRENT_SCENARIO=""
LAST_ELAPSED_MS=""
TEMP_DATABASES=()

debug() {
	if [[ "${DEBUG}" == "1" ]]; then
		echo "[DEBUG] $*" >&2
	fi
}

now_ms() {
	local raw
	raw="$(date +%s%3N 2>/dev/null || true)"
	if [[ "${raw}" =~ ^[0-9]+$ ]]; then
		printf '%s' "${raw}"
		return 0
	fi
	printf '%s' "$(( $(date +%s) * 1000 ))"
}

truncate_2kb() {
	printf '%s' "$1" | head -c 2048
}

fail_step() {
	local step="$1"
	local code="${2:-${LAST_HTTP_CODE}}"
	local body="${3:-${LAST_BODY}}"

	echo "FAIL R1 step=${step}"
	echo "scenario=${CURRENT_SCENARIO:-UNKNOWN}"
	echo "http_code=${code:-UNKNOWN}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "incident_id=${INCIDENT_ID:-NONE}"
	echo "job_id=${JOB_ID:-NONE}"
	echo "base_url_a=${BASE_URL_A:-NONE}"
	echo "base_url_b=${BASE_URL_B:-NONE}"
	if [[ -n "${SERVER_A_LOG:-}" ]]; then
		echo "server_a_log_tail<<EOF"
		tail -n 40 "${SERVER_A_LOG}" 2>/dev/null | head -c 2048
		echo
		echo "EOF"
		echo "server_a_log_errors<<EOF"
		rg -n "AIJobListFailed|no such table|doesn't exist|record not found|error|failed|panic" "${SERVER_A_LOG}" 2>/dev/null | tail -n 40 | head -c 2048
		echo
		echo "EOF"
	fi
	if [[ -n "${SERVER_B_LOG:-}" ]]; then
		echo "server_b_log_tail<<EOF"
		tail -n 40 "${SERVER_B_LOG}" 2>/dev/null | head -c 2048
		echo
		echo "EOF"
		echo "server_b_log_errors<<EOF"
		rg -n "AIJobListFailed|no such table|doesn't exist|record not found|error|failed|panic" "${SERVER_B_LOG}" 2>/dev/null | tail -n 40 | head -c 2048
		echo
		echo "EOF"
	fi
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
		fail_step "${step}"
	fi
	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		fail_step "${step}"
	fi
	debug "${step} code=${LAST_HTTP_CODE}"
}

extract_field() {
	local json="$1"
	shift
	local keys=("$@")
	local key value

	if command -v jq >/dev/null 2>&1; then
		for key in "${keys[@]}"; do
			value="$(
				printf '%s' "${json}" | jq -r --arg k "${key}" '
					(.[$k] // .data[$k] // .incident[$k] // .data.incident[$k] // .job[$k] // .data.job[$k]) |
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

extract_total_count() {
	local json="$1"
	local value
	if command -v jq >/dev/null 2>&1; then
		value="$(
			printf '%s' "${json}" | jq -r '
				(.totalCount // .total_count // .data.totalCount // .data.total_count) |
				if . == null then empty
				elif (type == "number") then tostring
				elif (type == "string") then .
				else empty
				end
			' 2>/dev/null
		)"
	else
		value="$(printf '%s' "${json}" | sed -n 's/.*"totalCount":[[:space:]]*\([0-9][0-9]*\).*/\1/p' | head -n 1)"
		if [[ -z "${value}" ]]; then
			value="$(printf '%s' "${json}" | sed -n 's/.*"total_count":[[:space:]]*\([0-9][0-9]*\).*/\1/p' | head -n 1)"
		fi
	fi
	if [[ "${value:-}" =~ ^[0-9]+$ ]]; then
		printf '%s' "${value}"
		return 0
	fi
	return 1
}

response_has_job_id() {
	local json="$1"
	local target_job_id="$2"
	if [[ -z "${target_job_id}" ]]; then
		return 1
	fi
	if command -v jq >/dev/null 2>&1; then
		printf '%s' "${json}" | jq -e --arg id "${target_job_id}" '
			((.jobs // .data.jobs // []) | any((.jobID // .job_id // "") == $id))
		' >/dev/null 2>&1
		return $?
	fi
	printf '%s' "${json}" | grep -F "\"jobID\":\"${target_job_id}\"" >/dev/null 2>&1 && return 0
	printf '%s' "${json}" | grep -F "\"job_id\":\"${target_job_id}\"" >/dev/null 2>&1 && return 0
	return 1
}

sanitize_db_name() {
	local raw="$1"
	printf '%s' "${raw}" | tr -c 'A-Za-z0-9_' '_' | cut -c1-60
}

new_temp_database_name() {
	local scenario="$1"
	local lower
	lower="$(printf '%s' "${scenario}" | tr '[:upper:]' '[:lower:]')"
	sanitize_db_name "${MYSQL_DATABASE_PREFIX}_${lower}_${RANDOM}"
}

run_mysql_sql() {
	local step="$1"
	local sql_stmt="$2"
	local go_file
	go_file="$(mktemp).go"
	cat >"${go_file}" <<'EOF'
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

func main() {
	addr := os.Getenv("R1_MYSQL_ADDR")
	user := os.Getenv("R1_MYSQL_USER")
	pass := os.Getenv("R1_MYSQL_PASS")
	stmt := os.Getenv("R1_MYSQL_SQL")

	dsn := fmt.Sprintf("%s:%s@tcp(%s)/?charset=utf8mb4&parseTime=true&loc=Local", user, pass, addr)
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
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}
EOF

	set +e
	local out rc
	out="$(
		cd "${REPO_ROOT}" && \
			R1_MYSQL_ADDR="${MYSQL_ADDR}" \
			R1_MYSQL_USER="${MYSQL_USERNAME}" \
			R1_MYSQL_PASS="${MYSQL_PASSWORD}" \
			R1_MYSQL_SQL="${sql_stmt}" \
			GOLANG_PROTOBUF_REGISTRATION_CONFLICT=warn go run "${go_file}" 2>&1
	)"
	rc=$?
	set -e
	rm -f "${go_file}"

	if (( rc != 0 )); then
		LAST_HTTP_CODE="MYSQL_SQL_FAILED"
		LAST_BODY="${out}"
		debug "mysql sql failed step=${step} err=${out}"
		return 1
	fi
	return 0
}

create_temp_database() {
	local db_name="$1"
	if [[ ! "${db_name}" =~ ^[A-Za-z0-9_]+$ ]]; then
		fail_step "CreateTempDB.InvalidName" "INVALID_DB_NAME" "${db_name}"
	fi
	if ! run_mysql_sql "CreateTempDB.${db_name}" "CREATE DATABASE IF NOT EXISTS \`${db_name}\` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"; then
		fail_step "CreateTempDB.${db_name}" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
	TEMP_DATABASES+=("${db_name}")
}

drop_temp_database() {
	local db_name="$1"
	if [[ -z "${db_name}" ]]; then
		return 0
	fi
	if [[ ! "${db_name}" =~ ^[A-Za-z0-9_]+$ ]]; then
		return 0
	fi
	run_mysql_sql "DropTempDB.${db_name}" "DROP DATABASE IF EXISTS \`${db_name}\`" >/dev/null 2>&1 || true
}

kill_port_listener() {
	local port="$1"
	if ! command -v lsof >/dev/null 2>&1; then
		return 0
	fi
	local pids
	pids="$(lsof -tiTCP:"${port}" -sTCP:LISTEN 2>/dev/null || true)"
	if [[ -z "${pids}" ]]; then
		return 0
	fi
	for pid in ${pids}; do
		kill "${pid}" >/dev/null 2>&1 || true
	done
}

stop_servers() {
	if [[ -n "${SERVER_A_PID}" ]]; then
		kill "${SERVER_A_PID}" >/dev/null 2>&1 || true
		wait "${SERVER_A_PID}" >/dev/null 2>&1 || true
		SERVER_A_PID=""
	fi
	if [[ -n "${SERVER_B_PID}" ]]; then
		kill "${SERVER_B_PID}" >/dev/null 2>&1 || true
		wait "${SERVER_B_PID}" >/dev/null 2>&1 || true
		SERVER_B_PID=""
	fi
	kill_port_listener "${PORT_A_REDIS}"
	kill_port_listener "${PORT_B_REDIS}"
	kill_port_listener "${PORT_A_FALLBACK}"
	kill_port_listener "${PORT_B_FALLBACK}"
}

cleanup() {
	stop_servers
	for db_name in "${TEMP_DATABASES[@]:-}"; do
		drop_temp_database "${db_name}" || true
	done
	rm -f "${SERVER_A_LOG:-}" "${SERVER_B_LOG:-}" "${SERVER_A_CONFIG:-}" "${SERVER_B_CONFIG:-}"
}
trap cleanup EXIT

render_apiserver_config() {
	local port="$1"
	local redis_enabled="$2"
	local redis_topic="$3"
	local coredb_database="$4"
	local tmp_cfg
	tmp_cfg="$(mktemp).yaml"

	if ! awk \
		-v port="${port}" \
		-v coredb_addr="${MYSQL_ADDR}" \
		-v coredb_username="${MYSQL_USERNAME}" \
		-v coredb_password="${MYSQL_PASSWORD}" \
		-v coredb_database="${coredb_database}" \
		-v redis_enabled="${redis_enabled}" \
		-v redis_addr="${REDIS_ADDR}" \
		-v redis_db="${REDIS_DB}" \
		-v redis_password="${REDIS_PASSWORD}" \
		-v redis_fail_open="${REDIS_FAIL_OPEN}" \
		-v redis_topic="${redis_topic}" \
		'
		BEGIN {
			in_http = 0
			in_coredb = 0
			in_redis = 0
			http_addr_done = 0
		}
		{
			if ($0 ~ /^http:[[:space:]]*$/) {
				in_http = 1
				in_coredb = 0
				in_redis = 0
				print
				next
			}
			if ($0 ~ /^coredb:[[:space:]]*$/) {
				in_coredb = 1
				in_http = 0
				in_redis = 0
				print
				next
			}
			if ($0 ~ /^redis:[[:space:]]*$/) {
				in_redis = 1
				in_http = 0
				in_coredb = 0
				print
				next
			}
			if ($0 ~ /^[^[:space:]]/ && $0 !~ /^http:[[:space:]]*$/ && $0 !~ /^coredb:[[:space:]]*$/ && $0 !~ /^redis:[[:space:]]*$/) {
				in_http = 0
				in_coredb = 0
				in_redis = 0
			}
			if (in_http && $0 ~ /^[[:space:]]*addr:[[:space:]]*/) {
				print "  addr: 127.0.0.1:" port " # script override"
				http_addr_done = 1
				next
			}
			if (in_coredb) {
				if ($0 ~ /^[[:space:]]*addr:[[:space:]]*/) {
					print "  addr: " coredb_addr
					next
				}
				if ($0 ~ /^[[:space:]]*username:[[:space:]]*/) {
					print "  username: " coredb_username
					next
				}
				if ($0 ~ /^[[:space:]]*password:[[:space:]]*/) {
					print "  password: \"" coredb_password "\""
					next
				}
				if ($0 ~ /^[[:space:]]*database:[[:space:]]*/) {
					print "  database: " coredb_database
					next
				}
			}
			if (in_redis) {
				if ($0 ~ /^[[:space:]]*enabled:[[:space:]]*/) {
					print "  enabled: " redis_enabled
					next
				}
				if ($0 ~ /^[[:space:]]*addr:[[:space:]]*/) {
					print "  addr: \"" redis_addr "\""
					next
				}
				if ($0 ~ /^[[:space:]]*db:[[:space:]]*/) {
					print "  db: " redis_db
					next
				}
				if ($0 ~ /^[[:space:]]*password:[[:space:]]*/) {
					print "  password: \"" redis_password "\""
					next
				}
				if ($0 ~ /^[[:space:]]*fail_open:[[:space:]]*/) {
					print "  fail_open: " redis_fail_open
					next
				}
				if ($0 ~ /^[[:space:]]*ai_job_queue_signal:[[:space:]]*/) {
					print "    ai_job_queue_signal: \"" redis_topic "\""
					next
				}
			}
			print
		}
		END {
			if (!http_addr_done) {
				exit 2
			}
		}
	' "${APISERVER_CONFIG}" >"${tmp_cfg}"; then
		rm -f "${tmp_cfg}"
		fail_step "RenderConfig.${port}" "CONFIG_RENDER_FAILED" "${APISERVER_CONFIG}"
	fi

	printf '%s' "${tmp_cfg}"
}

start_apiserver() {
	local name="$1"
	local port="$2"
	local redis_enabled="$3"
	local redis_topic="$4"
	local coredb_database="$5"
	local logf cfg

	logf="$(mktemp)"
	cfg="$(render_apiserver_config "${port}" "${redis_enabled}" "${redis_topic}" "${coredb_database}")"
	(
		cd "${REPO_ROOT}" && \
			bash -lc "${APISERVER_CMD_BASE} --config '${cfg}'"
	) >"${logf}" 2>&1 &
	local pid="$!"
	sleep 1

	if ! kill -0 "${pid}" >/dev/null 2>&1; then
		LAST_HTTP_CODE="APISERVER_EXITED"
		LAST_BODY="$(cat "${logf}" 2>/dev/null || true)"
		fail_step "StartAPIServer.${name}"
	fi

	if [[ "${name}" == "A" ]]; then
		SERVER_A_PID="${pid}"
		SERVER_A_LOG="${logf}"
		SERVER_A_CONFIG="${cfg}"
	else
		SERVER_B_PID="${pid}"
		SERVER_B_LOG="${logf}"
		SERVER_B_CONFIG="${cfg}"
	fi
}

wait_server_ready() {
	local name="$1"
	local base_url="$2"
	local deadline now
	deadline="$(( $(date +%s) + 90 ))"
	while true; do
		if http_json GET "${base_url}/v1/ai/jobs?status=queued&offset=0&limit=1"; then
			if [[ "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
				debug "server ${name} ready base_url=${base_url}"
				return 0
			fi
		fi
		now="$(date +%s)"
		if (( now > deadline )); then
			fail_step "WaitServerReady.${name}" "${LAST_HTTP_CODE}" "${LAST_BODY}"
		fi
		sleep 1
	done
}

wait_redis_ready() {
	local name="$1"
	local base_url="$2"
	local topic="$3"
	local deadline metrics_body
	deadline="$(( $(date +%s) + 60 ))"
	while true; do
		if http_json GET "${base_url}/metrics"; then
			if [[ "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
				metrics_body="${LAST_BODY}"
				if printf '%s\n' "${metrics_body}" | grep -F "redis_pubsub_subscribe_state{topic=\"${topic}\"} 1" >/dev/null 2>&1; then
					debug "redis ready ${name} topic=${topic}"
					return 0
				fi
			fi
		fi
		if (( $(date +%s) > deadline )); then
			fail_step "WaitRedisReady.${name}" "${LAST_HTTP_CODE}" "${LAST_BODY}"
		fi
		sleep 1
	done
}

start_pair() {
	local port_a="$1"
	local port_b="$2"
	local redis_enabled="$3"
	local topic="$4"
	local coredb_database="$5"

	BASE_URL_A="http://127.0.0.1:${port_a}"
	BASE_URL_B="http://127.0.0.1:${port_b}"

	start_apiserver "A" "${port_a}" "${redis_enabled}" "${topic}" "${coredb_database}"
	start_apiserver "B" "${port_b}" "${redis_enabled}" "${topic}" "${coredb_database}"
	wait_server_ready "A" "${BASE_URL_A}"
	wait_server_ready "B" "${BASE_URL_B}"
	if [[ "${redis_enabled}" == "true" ]]; then
		wait_redis_ready "A" "${BASE_URL_A}" "${topic}"
		wait_redis_ready "B" "${BASE_URL_B}" "${topic}"
	fi
}

assert_log_has_wakeup_source() {
	local expected_source="$1"
	if [[ -z "${SERVER_B_LOG}" ]]; then
		fail_step "AssertWakeupLog.NoServerBLog" "NO_LOG_FILE" ""
	fi
	if ! grep -q "wakeup_source" "${SERVER_B_LOG}" || ! grep -q "${expected_source}" "${SERVER_B_LOG}"; then
		LAST_HTTP_CODE="LOG_ASSERT_FAILED"
		LAST_BODY="$(cat "${SERVER_B_LOG}" 2>/dev/null || true)"
		fail_step "AssertWakeupLog.${expected_source}" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
}

run_cross_instance_case() {
	local case_name="$1"
	local expected_source="$2"
	local poll_body_file poll_code_file poll_err_file
	local poll_pid poll_started_s poll_finished_s elapsed_ms max_expected_ms
	local returned_job_id
	local baseline_total poll_offset
	local longpoll_body longpoll_code

	CURRENT_SCENARIO="${case_name}"
	INCIDENT_ID=""
	JOB_ID=""

	call_or_fail "${case_name}.BaselineListB" GET "${BASE_URL_B}/v1/ai/jobs?status=queued&offset=0&limit=1"
	baseline_total="$(extract_total_count "${LAST_BODY}" || true)"
	if [[ -z "${baseline_total}" ]]; then
		baseline_total="0"
	fi
	if [[ ! "${baseline_total}" =~ ^[0-9]+$ ]]; then
		fail_step "${case_name}.BaselineTotalParse" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
	poll_offset="${baseline_total}"

	poll_body_file="$(mktemp)"
	poll_code_file="$(mktemp)"
	poll_err_file="$(mktemp)"

	poll_started_s="$(date +%s)"
	(
		set +e
		code="$("${CURL}" -sS -o "${poll_body_file}" -w "%{http_code}" \
			-H "Accept: application/json" \
			-H "X-Scopes: ${SCOPES}" \
			"${BASE_URL_B}/v1/ai/jobs?status=queued&offset=${poll_offset}&limit=10&wait_seconds=${WAIT_SECONDS}" \
			2>"${poll_err_file}")"
		rc=$?
		set -e
		if (( rc != 0 )); then
			echo "CURL_${rc}" >"${poll_code_file}"
		else
			echo "${code}" >"${poll_code_file}"
		fi
	) &
	poll_pid="$!"

	sleep 1

	local rand now_epoch start_epoch fingerprint ingest_body run_body
	rand="${RANDOM}"
	now_epoch="$(date -u +%s)"
	start_epoch="$((now_epoch - 1800))"
	fingerprint="r1-${case_name}-fp-${rand}"

	ingest_body=$(cat <<EOF
{"idempotencyKey":"idem-r1-${case_name}-ingest-${rand}","fingerprint":"${fingerprint}","status":"firing","severity":"P1","service":"r1-svc","cluster":"prod-r1","namespace":"default","workload":"r1-workload","lastSeenAt":{"seconds":${now_epoch},"nanos":0}}
EOF
)
	call_or_fail "${case_name}.IngestAlertEventA" POST "${BASE_URL_A}/v1/alert-events:ingest" "${ingest_body}"
	INCIDENT_ID="$(extract_field "${LAST_BODY}" "incidentID" "incident_id" || true)"
	if [[ -z "${INCIDENT_ID}" ]]; then
		fail_step "${case_name}.ParseIncidentIDA" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi

	run_body=$(cat <<EOF
{"incidentID":"${INCIDENT_ID}","idempotencyKey":"idem-r1-${case_name}-run-${rand}","pipeline":"basic_rca","trigger":"manual","timeRangeStart":{"seconds":${start_epoch},"nanos":0},"timeRangeEnd":{"seconds":${now_epoch},"nanos":0},"inputHintsJSON":"{\"scenario\":\"R1_${case_name}\"}","createdBy":"system"}
EOF
)
	call_or_fail "${case_name}.RunAIJobA" POST "${BASE_URL_A}/v1/incidents/${INCIDENT_ID}/ai:run" "${run_body}"
	JOB_ID="$(extract_field "${LAST_BODY}" "jobID" "job_id" || true)"
	if [[ -z "${JOB_ID}" ]]; then
		fail_step "${case_name}.ParseJobIDA" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi

	local deadline
	deadline="$(( $(date +%s) + POLL_TIMEOUT_SEC ))"
	while kill -0 "${poll_pid}" >/dev/null 2>&1; do
		if (( $(date +%s) > deadline )); then
			kill "${poll_pid}" >/dev/null 2>&1 || true
			wait "${poll_pid}" >/dev/null 2>&1 || true
			LAST_HTTP_CODE="TIMEOUT"
			LAST_BODY="$(cat "${poll_body_file}" 2>/dev/null || true)"
			fail_step "${case_name}.LongPollTimeout" "${LAST_HTTP_CODE}" "${LAST_BODY}"
		fi
		sleep 0.2
	done
	wait "${poll_pid}" >/dev/null 2>&1 || true

	LAST_HTTP_CODE="$(cat "${poll_code_file}" 2>/dev/null || true)"
	LAST_BODY="$(cat "${poll_body_file}" 2>/dev/null || true)"
	local poll_err
	poll_err="$(cat "${poll_err_file}" 2>/dev/null || true)"
	if [[ -n "${poll_err}" ]]; then
		if [[ -n "${LAST_BODY}" ]]; then
			LAST_BODY="${LAST_BODY}"$'\n'"${poll_err}"
		else
			LAST_BODY="${poll_err}"
		fi
	fi

	if [[ ! "${LAST_HTTP_CODE}" =~ ^2[0-9][0-9]$ ]]; then
		fail_step "${case_name}.LongPollResultCode" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi
	longpoll_code="${LAST_HTTP_CODE}"
	longpoll_body="${LAST_BODY}"
	if command -v jq >/dev/null 2>&1; then
		returned_job_id="$(
			printf '%s' "${LAST_BODY}" | jq -r '
				((.jobs // .data.jobs // []) | map(.jobID // .job_id) | map(select(type == "string" and length > 0)) | .[0]) // empty
			' 2>/dev/null
		)"
	else
		returned_job_id="$(printf '%s' "${LAST_BODY}" | sed -n 's/.*"jobID":"\([^"]*\)".*/\1/p' | head -n 1)"
		if [[ -z "${returned_job_id}" ]]; then
			returned_job_id="$(printf '%s' "${LAST_BODY}" | sed -n 's/.*"job_id":"\([^"]*\)".*/\1/p' | head -n 1)"
		fi
	fi
	if [[ -z "${returned_job_id}" ]]; then
		fail_step "${case_name}.LongPollMissingJobID" "${LAST_HTTP_CODE}" "${LAST_BODY}"
	fi

	call_or_fail "${case_name}.GetCreatedJob" GET "${BASE_URL_A}/v1/ai/jobs/${JOB_ID}"
	if ! response_has_job_id "${longpoll_body}" "${JOB_ID}"; then
		fail_step "${case_name}.LongPollMissingCreatedJob" "${longpoll_code}" "${longpoll_body}"
	fi

	poll_finished_s="$(date +%s)"
	elapsed_ms="$(((poll_finished_s - poll_started_s) * 1000))"
	max_expected_ms="$((WAIT_SECONDS * 1000 + 1500))"
	if (( elapsed_ms > max_expected_ms )); then
		fail_step "${case_name}.LongPollWakeTooLate" "${LAST_HTTP_CODE}" "elapsed_ms=${elapsed_ms}"
	fi

	sleep 1
	assert_log_has_wakeup_source "${expected_source}"

	rm -f "${poll_body_file}" "${poll_code_file}" "${poll_err_file}"
	LAST_ELAPSED_MS="${elapsed_ms}"
}

run_scenario() {
	local scenario_name="$1"
	local port_a="$2"
	local port_b="$3"
	local redis_enabled="$4"
	local expected_source="$5"
	local topic="$6"
	local temp_db

	stop_servers
	temp_db="$(new_temp_database_name "${scenario_name}")"
	create_temp_database "${temp_db}"
	start_pair "${port_a}" "${port_b}" "${redis_enabled}" "${topic}" "${temp_db}"
	run_cross_instance_case "${scenario_name}" "${expected_source}"
	stop_servers
}

rand="${RANDOM}"
topic_redis="${REDIS_TOPIC_PREFIX}:redis:${rand}"
topic_fallback="${REDIS_TOPIC_PREFIX}:fallback:${rand}"

run_scenario "RedisWakeup" "${PORT_A_REDIS}" "${PORT_B_REDIS}" "true" "redis" "${topic_redis}"
elapsed_redis="${LAST_ELAPSED_MS}"
run_scenario "FallbackDBWatermark" "${PORT_A_FALLBACK}" "${PORT_B_FALLBACK}" "false" "db_watermark" "${topic_fallback}"
elapsed_fallback="${LAST_ELAPSED_MS}"

echo "PASS R1"
echo "redis_topic=${topic_redis}"
echo "redis_wakeup_elapsed_ms=${elapsed_redis}"
echo "fallback_wakeup_elapsed_ms=${elapsed_fallback}"
echo "redis_addr=${REDIS_ADDR}"
