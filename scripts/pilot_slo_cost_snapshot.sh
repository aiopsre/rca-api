#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
METRICS_URL="${METRICS_URL:-${BASE_URL}/metrics}"
WINDOW_SECONDS="${WINDOW_SECONDS:-60}"
SAMPLE_INTERVAL_SEC="${SAMPLE_INTERVAL_SEC:-1}"
TOPN="${TOPN:-5}"

CURL="${CURL:-curl}"
CURL_CONNECT_TIMEOUT="${CURL_CONNECT_TIMEOUT:-5}"
CURL_MAX_TIME="${CURL_MAX_TIME:-15}"

THRESH_REDIS_READY_MIN="${THRESH_REDIS_READY_MIN:-0}"
THRESH_LONGPOLL_FALLBACK_DEGRADED="${THRESH_LONGPOLL_FALLBACK_DEGRADED:-0}"
THRESH_LONGPOLL_FALLBACK_ALERT="${THRESH_LONGPOLL_FALLBACK_ALERT:-10}"
THRESH_MCP_INTERNAL_DEGRADED="${THRESH_MCP_INTERNAL_DEGRADED:-0}"
THRESH_MCP_INTERNAL_ALERT="${THRESH_MCP_INTERNAL_ALERT:-5}"
THRESH_MCP_RATE_LIMITED_DEGRADED="${THRESH_MCP_RATE_LIMITED_DEGRADED:-0}"
THRESH_MCP_RATE_LIMITED_ALERT="${THRESH_MCP_RATE_LIMITED_ALERT:-20}"
THRESH_MCP_SCOPE_DENIED_DEGRADED="${THRESH_MCP_SCOPE_DENIED_DEGRADED:-0}"
THRESH_MCP_SCOPE_DENIED_ALERT="${THRESH_MCP_SCOPE_DENIED_ALERT:-20}"
THRESH_MCP_TRUNCATED_DEGRADED="${THRESH_MCP_TRUNCATED_DEGRADED:-0}"
THRESH_MCP_TRUNCATED_ALERT="${THRESH_MCP_TRUNCATED_ALERT:-50}"
THRESH_NOTICE_DB_FALLBACK_DEGRADED="${THRESH_NOTICE_DB_FALLBACK_DEGRADED:-0}"
THRESH_NOTICE_DB_FALLBACK_ALERT="${THRESH_NOTICE_DB_FALLBACK_ALERT:-20}"
THRESH_NOTICE_LIMITER_FALLBACK_DEGRADED="${THRESH_NOTICE_LIMITER_FALLBACK_DEGRADED:-0}"
THRESH_NOTICE_LIMITER_FALLBACK_ALERT="${THRESH_NOTICE_LIMITER_FALLBACK_ALERT:-20}"
THRESH_NOTICE_LIMITER_DENY_DEGRADED="${THRESH_NOTICE_LIMITER_DENY_DEGRADED:-0}"
THRESH_NOTICE_LIMITER_DENY_ALERT="${THRESH_NOTICE_LIMITER_DENY_ALERT:-50}"
THRESH_NOTICE_FAILED_DEGRADED="${THRESH_NOTICE_FAILED_DEGRADED:-0}"
THRESH_NOTICE_FAILED_ALERT="${THRESH_NOTICE_FAILED_ALERT:-10}"

DB_DSN="${DB_DSN:-}"
MYSQL_HOST="${MYSQL_HOST:-${DB_HOST:-127.0.0.1}}"
MYSQL_PORT="${MYSQL_PORT:-${DB_PORT:-3306}}"
MYSQL_USER="${MYSQL_USER:-${MYSQL_USERNAME:-${DB_USER:-root}}}"
MYSQL_PASSWORD="${MYSQL_PASSWORD:-${MYSQL_PASS:-${DB_PASSWORD:-}}}"
MYSQL_DATABASE="${MYSQL_DATABASE:-${MYSQL_DB:-${DB_NAME:-}}}"
DB_ENGINE="mysql"

TMP_FILES=()
ANOMALY_FILE=""
MCP_CODE_FILE=""
ANOMALY_COUNT=0
HAS_ALERT=0
HAS_DEGRADED=0
ALERT_REASONS=""
DEGRADED_REASONS=""

need_cmd() {
	command -v "$1" >/dev/null 2>&1
}

fail() {
	echo "[FAIL] $*" >&2
	exit 1
}

is_uint() {
	[[ "$1" =~ ^[0-9]+$ ]]
}

normalize_uint() {
	local name="$1"
	local value="$2"
	local fallback="$3"
	local normalized
	if ! is_uint "${value}" || (( value <= 0 )); then
		normalized="${fallback}"
	else
		normalized="${value}"
	fi
	printf '%s' "${normalized}"
}

new_tmp_file() {
	local f
	f="$(mktemp)"
	TMP_FILES+=("${f}")
	printf '%s' "${f}"
}

cleanup() {
	local f
	for f in "${TMP_FILES[@]:-}"; do
		[[ -n "${f}" ]] && rm -f "${f}" >/dev/null 2>&1 || true
	done
}
trap cleanup EXIT

float_gt() {
	local a="$1"
	local b="$2"
	awk -v av="${a}" -v bv="${b}" 'BEGIN { exit !((av + 0) > (bv + 0)) }'
}

float_ge() {
	local a="$1"
	local b="$2"
	awk -v av="${a}" -v bv="${b}" 'BEGIN { exit !((av + 0) >= (bv + 0)) }'
}

float_le() {
	local a="$1"
	local b="$2"
	awk -v av="${a}" -v bv="${b}" 'BEGIN { exit !((av + 0) <= (bv + 0)) }'
}

calc_delta() {
	local before="$1"
	local after="$2"
	awk -v b="${before}" -v a="${after}" 'BEGIN { printf "%.6f", (a + 0) - (b + 0) }'
}

scale_delta() {
	local raw_delta="$1"
	local factor="$2"
	awk -v d="${raw_delta}" -v f="${factor}" 'BEGIN { printf "%.6f", (d + 0) * (f + 0) }'
}

metric_sum() {
	local body="$1"
	local metric="$2"
	local label_filter="${3:-}"
	printf '%s\n' "${body}" | awk -v name="${metric}" -v filter="${label_filter}" '
		$1 ~ ("^" name "(\\{|$)") {
			if (filter != "" && index($1, filter) == 0) {
				next
			}
			sum += ($NF + 0)
		}
		END { printf "%.6f", sum + 0 }
	'
}

metric_exists() {
	local body="$1"
	local metric="$2"
	printf '%s\n' "${body}" | awk -v name="${metric}" '
		$1 ~ ("^" name "(\\{|$)") { found = 1 }
		END { exit found ? 0 : 1 }
	'
}

fetch_metrics() {
	local out_file="$1"
	local rc
	set +e
	"${CURL}" -sS --connect-timeout "${CURL_CONNECT_TIMEOUT}" --max-time "${CURL_MAX_TIME}" "${METRICS_URL}" >"${out_file}"
	rc=$?
	set -e
	return "${rc}"
}

write_mcp_calls_by_code() {
	local body_file="$1"
	local out_file="$2"
	awk '
		$1 ~ /^mcp_calls_total\{/ {
			labels = $1
			sub(/^mcp_calls_total\{/, "", labels)
			sub(/\}$/, "", labels)
			code = "OK"
			n = split(labels, parts, ",")
			for (i = 1; i <= n; i++) {
				split(parts[i], kv, "=")
				key = kv[1]
				val = kv[2]
				gsub(/^[[:space:]]+|[[:space:]]+$/, "", key)
				gsub(/"/, "", val)
				if (key == "code") {
					code = toupper(val)
				}
			}
			sum[code] += ($NF + 0)
		}
		END {
			for (k in sum) {
				printf "%s\t%.6f\n", k, sum[k]
			}
		}
	' "${body_file}" | LC_ALL=C sort >"${out_file}"
}

build_mcp_code_delta() {
	local before_file="$1"
	local after_file="$2"
	local out_file="$3"
	local factor="$4"
	awk -v req="INTERNAL,RATE_LIMITED,SCOPE_DENIED,INVALID_ARGUMENT,NOT_FOUND" -v factor="${factor}" '
		BEGIN {
			n = split(req, arr, ",")
			for (i = 1; i <= n; i++) {
				required[arr[i]] = 1
			}
		}
		NR == FNR {
			before[$1] = $2 + 0
			seen[$1] = 1
			next
		}
		{
			after[$1] = $2 + 0
			seen[$1] = 1
		}
		END {
			for (k in required) {
				seen[k] = 1
			}
			for (k in seen) {
				b = (k in before) ? before[k] : 0
				a = (k in after) ? after[k] : 0
				d = a - b
				w = d * factor
				printf "%s\t%.6f\t%.6f\t%.6f\n", k, a, d, w
			}
		}
	' "${before_file}" "${after_file}" | LC_ALL=C sort >"${out_file}"
}

code_delta_window() {
	local code="$1"
	local file="$2"
	awk -F '\t' -v c="${code}" '
		$1 == c {
			print $4
			found = 1
			exit
		}
		END {
			if (!found) {
				print "0.000000"
			}
		}
	' "${file}"
}

code_value() {
	local code="$1"
	local file="$2"
	awk -F '\t' -v c="${code}" '
		$1 == c {
			print $2
			found = 1
			exit
		}
		END {
			if (!found) {
				print "0.000000"
			}
		}
	' "${file}"
}

append_reason() {
	local kind="$1"
	local reason="$2"
	if [[ "${kind}" == "ALERT" ]]; then
		if [[ -n "${ALERT_REASONS}" ]]; then
			ALERT_REASONS="${ALERT_REASONS};${reason}"
		else
			ALERT_REASONS="${reason}"
		fi
	else
		if [[ -n "${DEGRADED_REASONS}" ]]; then
			DEGRADED_REASONS="${DEGRADED_REASONS};${reason}"
		else
			DEGRADED_REASONS="${reason}"
		fi
	fi
}

record_anomaly() {
	local severity="$1"
	local metric="$2"
	local delta_window="$3"
	local detail="$4"
	local action1="$5"
	local action2="$6"
	local sort_delta

	sort_delta="${delta_window}"
	if float_le "${sort_delta}" "0"; then
		sort_delta="0.000001"
	fi

	printf '%s\t%s\t%s\t%s\t%s\t%s\n' "${sort_delta}" "${severity}" "${metric}" "${detail}" "${action1}" "${action2}" >>"${ANOMALY_FILE}"
	ANOMALY_COUNT=$((ANOMALY_COUNT + 1))
	if [[ "${severity}" == "ALERT" ]]; then
		HAS_ALERT=1
	else
		HAS_DEGRADED=1
	fi
}

evaluate_metric_threshold() {
	local metric="$1"
	local value="$2"
	local degraded="$3"
	local alert="$4"
	local reason="$5"
	local detail="$6"
	local action1="$7"
	local action2="$8"
	if float_gt "${value}" "${alert}"; then
		append_reason "ALERT" "${reason}>${alert}"
		record_anomaly "ALERT" "${metric}" "${value}" "${detail}" "${action1}" "${action2}"
		return
	fi
	if float_gt "${value}" "${degraded}"; then
		append_reason "DEGRADED" "${reason}>${degraded}"
		record_anomaly "DEGRADED" "${metric}" "${value}" "${detail}" "${action1}" "${action2}"
	fi
}

parse_dsn_if_present() {
	local dsn
	dsn="$(printf '%s' "${DB_DSN}" | xargs)"
	[[ -z "${dsn}" ]] && return 0

	if [[ "${dsn}" == sqlite:* ]] || [[ "${dsn}" == file:* ]] || [[ "${dsn}" == *.db ]]; then
		DB_ENGINE="sqlite"
		return 0
	fi

	if [[ "${dsn}" =~ ^([^:@/]+)(:([^@/]*))?@tcp\(([^:()]+)(:([0-9]+))?\)/([^?]+) ]]; then
		MYSQL_USER="${BASH_REMATCH[1]}"
		MYSQL_PASSWORD="${BASH_REMATCH[3]:-}"
		MYSQL_HOST="${BASH_REMATCH[4]}"
		MYSQL_PORT="${BASH_REMATCH[6]:-3306}"
		MYSQL_DATABASE="${BASH_REMATCH[7]}"
		DB_ENGINE="mysql"
		return 0
	fi

	if [[ "${dsn}" =~ ^mysql://([^:/@]+)(:([^@]*))?@([^:/]+)(:([0-9]+))?/([^?]+) ]]; then
		MYSQL_USER="${BASH_REMATCH[1]}"
		MYSQL_PASSWORD="${BASH_REMATCH[3]:-}"
		MYSQL_HOST="${BASH_REMATCH[4]}"
		MYSQL_PORT="${BASH_REMATCH[6]:-3306}"
		MYSQL_DATABASE="${BASH_REMATCH[7]}"
		DB_ENGINE="mysql"
		return 0
	fi
}

mysql_exec() {
	local sql="$1"
	shift || true

	local -a cmd
	cmd=(
		mysql
		--batch
		--raw
		--silent
		--host="${MYSQL_HOST}"
		--port="${MYSQL_PORT}"
		--user="${MYSQL_USER}"
	)
	if [[ -n "${MYSQL_DATABASE}" ]]; then
		cmd+=(--database="${MYSQL_DATABASE}")
	fi
	cmd+=("$@" -e "${sql}")

	if [[ -n "${MYSQL_PASSWORD}" ]]; then
		MYSQL_PWD="${MYSQL_PASSWORD}" "${cmd[@]}"
	else
		"${cmd[@]}"
	fi
}

mysql_scalar() {
	local sql="$1"
	mysql_exec "${sql}" --skip-column-names | tr -d '\r' | head -n 1
}

table_exists() {
	local table="$1"
	local count
	count="$(mysql_scalar "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = '${table}';")"
	[[ "${count}" == "1" ]]
}

resolve_tool_calls_table() {
	if table_exists "tool_calls"; then
		printf '%s' "tool_calls"
		return 0
	fi
	if table_exists "ai_tool_calls"; then
		printf '%s' "ai_tool_calls"
		return 0
	fi
	return 1
}

print_query_result() {
	local title="$1"
	local sql="$2"
	local out rc
	set +e
	out="$(mysql_exec "${sql}" 2>&1)"
	rc=$?
	set -e
	echo "${title}:"
	if (( rc != 0 )); then
		echo "  WARN query_failed detail=$(printf '%s' "${out}" | head -c 200)"
		return
	fi
	if [[ -z "${out}" ]]; then
		echo "  EMPTY"
		return
	fi
	printf '%s\n' "${out}" | awk -F '\t' '{printf "  %s\n", $0}'
}

print_db_topn() {
	parse_dsn_if_present
	if [[ -z "${DB_DSN}" && -z "${MYSQL_DATABASE}" ]]; then
		echo "DB_TOPN status=SKIP reason=db_not_configured"
		return
	fi
	if [[ "${DB_ENGINE}" == "sqlite" ]]; then
		echo "DB_TOPN status=SKIP reason=sqlite_detected"
		return
	fi
	if ! need_cmd mysql; then
		echo "DB_TOPN status=SKIP reason=mysql_client_missing"
		return
	fi
	if [[ -z "${MYSQL_DATABASE}" ]]; then
		echo "DB_TOPN status=SKIP reason=database_missing"
		return
	fi
	set +e
	mysql_exec "SELECT 1;" --skip-column-names >/dev/null 2>&1
	local rc=$?
	set -e
	if (( rc != 0 )); then
		echo "DB_TOPN status=SKIP reason=db_connect_failed host=${MYSQL_HOST} port=${MYSQL_PORT} database=${MYSQL_DATABASE}"
		return
	fi

	echo "DB_TOPN status=ENABLED database=${MYSQL_DATABASE}"

	if tool_table="$(resolve_tool_calls_table)"; then
		print_query_result \
			"tool_calls_topn_10m(group_by=tool_name,code)" \
			"SELECT tool_name, UPPER(status) AS code, COUNT(*) AS cnt FROM \`${tool_table}\` WHERE created_at >= DATE_SUB(UTC_TIMESTAMP(), INTERVAL 10 MINUTE) GROUP BY tool_name, UPPER(status) ORDER BY cnt DESC, tool_name ASC, code ASC LIMIT 10;"
	else
		echo "tool_calls_topn_10m(group_by=tool_name,code):"
		echo "  SKIP table_not_found"
	fi

	if table_exists "notice_deliveries"; then
		print_query_result \
			"notice_deliveries_failed_topn_10m(group_by=event_type,status,last_error)" \
			"SELECT COALESCE(event_type,'unknown') AS event_type, COALESCE(status,'unknown') AS status, COALESCE(NULLIF(SUBSTRING(error,1,120),''),'none') AS last_error, COUNT(*) AS cnt FROM notice_deliveries WHERE created_at >= DATE_SUB(UTC_TIMESTAMP(), INTERVAL 10 MINUTE) AND status = 'failed' GROUP BY event_type, status, last_error ORDER BY cnt DESC, event_type ASC LIMIT 10;"
	else
		echo "notice_deliveries_failed_topn_10m(group_by=event_type,status,last_error):"
		echo "  SKIP table_not_found"
	fi

	if table_exists "alert_events_history"; then
		print_query_result \
			"alert_events_topn_10m(group_by=service,fingerprint)" \
			"SELECT COALESCE(service,'unknown') AS service, fingerprint, COUNT(*) AS cnt FROM alert_events_history WHERE created_at >= DATE_SUB(UTC_TIMESTAMP(), INTERVAL 10 MINUTE) GROUP BY service, fingerprint ORDER BY cnt DESC, service ASC LIMIT 10;"
	else
		echo "alert_events_topn_10m(group_by=service,fingerprint):"
		echo "  SKIP table_not_found"
	fi
}

WINDOW_SECONDS="$(normalize_uint "WINDOW_SECONDS" "${WINDOW_SECONDS}" "60")"
SAMPLE_INTERVAL_SEC="$(normalize_uint "SAMPLE_INTERVAL_SEC" "${SAMPLE_INTERVAL_SEC}" "1")"
TOPN="$(normalize_uint "TOPN" "${TOPN}" "5")"

need_cmd "${CURL}" || fail "curl is required"
need_cmd awk || fail "awk is required"
need_cmd sed || fail "sed is required"

sample1_file="$(new_tmp_file)"
sample2_file="$(new_tmp_file)"
before_codes_file="$(new_tmp_file)"
after_codes_file="$(new_tmp_file)"
MCP_CODE_FILE="$(new_tmp_file)"
ANOMALY_FILE="$(new_tmp_file)"

fetch_metrics "${sample1_file}" || fail "cannot fetch metrics from ${METRICS_URL}"
sleep "${SAMPLE_INTERVAL_SEC}"
fetch_metrics "${sample2_file}" || fail "cannot fetch metrics from ${METRICS_URL}"

window_factor="$(awk -v w="${WINDOW_SECONDS}" -v s="${SAMPLE_INTERVAL_SEC}" 'BEGIN { if (s <= 0) s = 1; printf "%.6f", (w + 0) / (s + 0) }')"

sample1="$(cat "${sample1_file}")"
sample2="$(cat "${sample2_file}")"

redis_ready_2="$(metric_sum "${sample2}" "redis_pubsub_subscribe_ready")"

longpoll_delta_raw="$(calc_delta "$(metric_sum "${sample1}" "ai_job_longpoll_fallback_total")" "$(metric_sum "${sample2}" "ai_job_longpoll_fallback_total")")"
longpoll_delta_window="$(scale_delta "${longpoll_delta_raw}" "${window_factor}")"

mcp_rate_limited_delta_raw="$(calc_delta "$(metric_sum "${sample1}" "mcp_rate_limited_total")" "$(metric_sum "${sample2}" "mcp_rate_limited_total")")"
mcp_rate_limited_delta_window="$(scale_delta "${mcp_rate_limited_delta_raw}" "${window_factor}")"

mcp_scope_denied_delta_raw="$(calc_delta "$(metric_sum "${sample1}" "mcp_scope_denied_total")" "$(metric_sum "${sample2}" "mcp_scope_denied_total")")"
mcp_scope_denied_delta_window="$(scale_delta "${mcp_scope_denied_delta_raw}" "${window_factor}")"

mcp_truncated_delta_raw="$(calc_delta "$(metric_sum "${sample1}" "mcp_truncated_total")" "$(metric_sum "${sample2}" "mcp_truncated_total")")"
mcp_truncated_delta_window="$(scale_delta "${mcp_truncated_delta_raw}" "${window_factor}")"

notice_db_fallback_delta_raw="$(calc_delta "$(metric_sum "${sample1}" "notice_worker_claim_source_total" 'source="db_fallback"')" "$(metric_sum "${sample2}" "notice_worker_claim_source_total" 'source="db_fallback"')")"
notice_db_fallback_delta_window="$(scale_delta "${notice_db_fallback_delta_raw}" "${window_factor}")"

notice_limiter_fallback_delta_raw="$(calc_delta "$(metric_sum "${sample1}" "notice_limiter_fallback_total")" "$(metric_sum "${sample2}" "notice_limiter_fallback_total")")"
notice_limiter_fallback_delta_window="$(scale_delta "${notice_limiter_fallback_delta_raw}" "${window_factor}")"

notice_limiter_deny_delta_raw="$(calc_delta "$(metric_sum "${sample1}" "notice_limiter_deny_total")" "$(metric_sum "${sample2}" "notice_limiter_deny_total")")"
notice_limiter_deny_delta_window="$(scale_delta "${notice_limiter_deny_delta_raw}" "${window_factor}")"

notice_failed_total_exists=0
notice_send_total_exists=0
notice_failed_delta_raw="0.000000"
notice_failed_delta_window="0.000000"
notice_send_succeeded_delta_raw="0.000000"
notice_send_succeeded_delta_window="0.000000"
notice_send_failed_delta_raw="0.000000"
notice_send_failed_delta_window="0.000000"
notice_send_pending_delta_raw="0.000000"
notice_send_pending_delta_window="0.000000"

if metric_exists "${sample2}" "notice_delivery_failed_total"; then
	notice_failed_total_exists=1
	notice_failed_delta_raw="$(calc_delta "$(metric_sum "${sample1}" "notice_delivery_failed_total")" "$(metric_sum "${sample2}" "notice_delivery_failed_total")")"
	notice_failed_delta_window="$(scale_delta "${notice_failed_delta_raw}" "${window_factor}")"
fi

if metric_exists "${sample2}" "notice_delivery_send_total"; then
	notice_send_total_exists=1
	notice_send_succeeded_delta_raw="$(calc_delta "$(metric_sum "${sample1}" "notice_delivery_send_total" 'status="succeeded"')" "$(metric_sum "${sample2}" "notice_delivery_send_total" 'status="succeeded"')")"
	notice_send_failed_delta_raw="$(calc_delta "$(metric_sum "${sample1}" "notice_delivery_send_total" 'status="failed"')" "$(metric_sum "${sample2}" "notice_delivery_send_total" 'status="failed"')")"
	notice_send_pending_delta_raw="$(calc_delta "$(metric_sum "${sample1}" "notice_delivery_send_total" 'status="pending"')" "$(metric_sum "${sample2}" "notice_delivery_send_total" 'status="pending"')")"
	notice_send_succeeded_delta_window="$(scale_delta "${notice_send_succeeded_delta_raw}" "${window_factor}")"
	notice_send_failed_delta_window="$(scale_delta "${notice_send_failed_delta_raw}" "${window_factor}")"
	notice_send_pending_delta_window="$(scale_delta "${notice_send_pending_delta_raw}" "${window_factor}")"
fi

write_mcp_calls_by_code "${sample1_file}" "${before_codes_file}"
write_mcp_calls_by_code "${sample2_file}" "${after_codes_file}"
build_mcp_code_delta "${before_codes_file}" "${after_codes_file}" "${MCP_CODE_FILE}" "${window_factor}"

mcp_internal_delta_window="$(code_delta_window "INTERNAL" "${MCP_CODE_FILE}")"
mcp_rate_limited_code_delta_window="$(code_delta_window "RATE_LIMITED" "${MCP_CODE_FILE}")"
mcp_scope_denied_code_delta_window="$(code_delta_window "SCOPE_DENIED" "${MCP_CODE_FILE}")"
mcp_invalid_argument_delta_window="$(code_delta_window "INVALID_ARGUMENT" "${MCP_CODE_FILE}")"
mcp_not_found_delta_window="$(code_delta_window "NOT_FOUND" "${MCP_CODE_FILE}")"

if float_le "${redis_ready_2}" "${THRESH_REDIS_READY_MIN}"; then
	redis_ready_gap="$(awk -v t="${THRESH_REDIS_READY_MIN}" -v v="${redis_ready_2}" 'BEGIN { printf "%.6f", (t + 1) - (v + 0) }')"
	append_reason "ALERT" "redis_pubsub_subscribe_ready<=${THRESH_REDIS_READY_MIN}"
	record_anomaly \
		"ALERT" \
		"redis_pubsub_subscribe_ready" \
		"${redis_ready_gap}" \
		"value=${redis_ready_2}" \
		"检查 Redis 连接与 pubsub 订阅线程状态，确认 subscribe_ready 恢复到 1。" \
		"核对 ai_job longpoll 唤醒链路（publish/subscribe）与网络连通性。"
fi

evaluate_metric_threshold \
	"ai_job_longpoll_fallback_total" \
	"${longpoll_delta_window}" \
	"${THRESH_LONGPOLL_FALLBACK_DEGRADED}" \
	"${THRESH_LONGPOLL_FALLBACK_ALERT}" \
	"ai_job_longpoll_fallback_delta" \
	"delta_window=${longpoll_delta_window}" \
	"检查 redis_pubsub_subscribe_ready 与 ai_job 长轮询 fallback 原因标签。" \
	"定位高频 request_id/job_id，确认是否持续走 db_watermark/timeout 路径。"

evaluate_metric_threshold \
	"mcp_calls_total{code=INTERNAL}" \
	"${mcp_internal_delta_window}" \
	"${THRESH_MCP_INTERNAL_DEGRADED}" \
	"${THRESH_MCP_INTERNAL_ALERT}" \
	"mcp_internal_delta" \
	"delta_window=${mcp_internal_delta_window}" \
	"按 request_id 检索 tool_calls，优先处理 INTERNAL 高频工具与参数。" \
	"检查下游 datasource 可用性与超时设置，必要时降低调用并发。"

evaluate_metric_threshold \
	"mcp_calls_total{code=RATE_LIMITED}" \
	"${mcp_rate_limited_code_delta_window}" \
	"${THRESH_MCP_RATE_LIMITED_DEGRADED}" \
	"${THRESH_MCP_RATE_LIMITED_ALERT}" \
	"mcp_rate_limited_code_delta" \
	"delta_window=${mcp_rate_limited_code_delta_window}" \
	"收敛调用端并发与轮询频率，确认是否存在调用风暴。" \
	"复核 MCP policy 限流阈值与 tool 级 max_limit/max_time_range 配置。"

evaluate_metric_threshold \
	"mcp_calls_total{code=SCOPE_DENIED}" \
	"${mcp_scope_denied_code_delta_window}" \
	"${THRESH_MCP_SCOPE_DENIED_DEGRADED}" \
	"${THRESH_MCP_SCOPE_DENIED_ALERT}" \
	"mcp_scope_denied_code_delta" \
	"delta_window=${mcp_scope_denied_code_delta_window}" \
	"检查调用方 X-Scopes 与 required_scopes 的映射是否正确。" \
	"确认 X-Allowed-Namespaces/Services 与 isolation.mode 是否匹配预期。"

if float_gt "${mcp_invalid_argument_delta_window}" "0"; then
	append_reason "DEGRADED" "mcp_invalid_argument_delta>0"
	record_anomaly \
		"DEGRADED" \
		"mcp_calls_total{code=INVALID_ARGUMENT}" \
		"${mcp_invalid_argument_delta_window}" \
		"delta_window=${mcp_invalid_argument_delta_window}" \
		"抽样失败请求，修正调用参数类型、时间范围与 limit 边界。" \
		"优先修复重复出现的调用模板，避免无效重试放大成本。"
fi

if float_gt "${mcp_not_found_delta_window}" "0"; then
	append_reason "DEGRADED" "mcp_not_found_delta>0"
	record_anomaly \
		"DEGRADED" \
		"mcp_calls_total{code=NOT_FOUND}" \
		"${mcp_not_found_delta_window}" \
		"delta_window=${mcp_not_found_delta_window}" \
		"核对 incident/job/delivery 等资源 ID 是否来自最新上下文。" \
		"检查 isolation.mode=not_found 场景是否由 allowlist 限制触发。"
fi

evaluate_metric_threshold \
	"mcp_rate_limited_total" \
	"${mcp_rate_limited_delta_window}" \
	"${THRESH_MCP_RATE_LIMITED_DEGRADED}" \
	"${THRESH_MCP_RATE_LIMITED_ALERT}" \
	"mcp_rate_limited_total_delta" \
	"delta_window=${mcp_rate_limited_delta_window}" \
	"按 tool_name 聚合 rate_limited，先处理 TopN 调用方与高频工具。" \
	"缩小 evidence 查询窗口，减少大范围、高频重试请求。"

evaluate_metric_threshold \
	"mcp_scope_denied_total" \
	"${mcp_scope_denied_delta_window}" \
	"${THRESH_MCP_SCOPE_DENIED_DEGRADED}" \
	"${THRESH_MCP_SCOPE_DENIED_ALERT}" \
	"mcp_scope_denied_total_delta" \
	"delta_window=${mcp_scope_denied_delta_window}" \
	"校验请求头 X-Scopes 与 RBAC scopes 字典一致性。" \
	"排查新接入调用方是否遗漏 required_scopes 配置。"

evaluate_metric_threshold \
	"mcp_truncated_total" \
	"${mcp_truncated_delta_window}" \
	"${THRESH_MCP_TRUNCATED_DEGRADED}" \
	"${THRESH_MCP_TRUNCATED_ALERT}" \
	"mcp_truncated_total_delta" \
	"delta_window=${mcp_truncated_delta_window}" \
	"收紧查询参数（time range/limit），避免宽查询导致大响应裁剪。" \
	"评估是否需要提高 evidence budget 精度而非盲目扩容。"

evaluate_metric_threshold \
	"notice_worker_claim_source_total{source=db_fallback}" \
	"${notice_db_fallback_delta_window}" \
	"${THRESH_NOTICE_DB_FALLBACK_DEGRADED}" \
	"${THRESH_NOTICE_DB_FALLBACK_ALERT}" \
	"notice_db_fallback_delta" \
	"delta_window=${notice_db_fallback_delta_window}" \
	"排查 notice stream 消费链路与 Redis 连接状态。" \
	"确认 worker 是否持续回退 DB claim，必要时临时降载。"

evaluate_metric_threshold \
	"notice_limiter_fallback_total" \
	"${notice_limiter_fallback_delta_window}" \
	"${THRESH_NOTICE_LIMITER_FALLBACK_DEGRADED}" \
	"${THRESH_NOTICE_LIMITER_FALLBACK_ALERT}" \
	"notice_limiter_fallback_delta" \
	"delta_window=${notice_limiter_fallback_delta_window}" \
	"检查 limiter Redis 依赖与超时设置，确认 fallback 原因。" \
	"临时收缩通知流量，避免 fallback 期间放大重试成本。"

evaluate_metric_threshold \
	"notice_limiter_deny_total" \
	"${notice_limiter_deny_delta_window}" \
	"${THRESH_NOTICE_LIMITER_DENY_DEGRADED}" \
	"${THRESH_NOTICE_LIMITER_DENY_ALERT}" \
	"notice_limiter_deny_delta" \
	"delta_window=${notice_limiter_deny_delta_window}" \
	"检查 channel/global QPS 配置与当前告警流量是否匹配。" \
	"对高噪声事件先做去重/静默，避免 limiter 长时间拒绝。"

if (( notice_failed_total_exists == 1 )); then
	evaluate_metric_threshold \
		"notice_delivery_failed_total" \
		"${notice_failed_delta_window}" \
		"${THRESH_NOTICE_FAILED_DEGRADED}" \
		"${THRESH_NOTICE_FAILED_ALERT}" \
		"notice_failed_delta" \
		"delta_window=${notice_failed_delta_window}" \
		"检查 webhook 返回码与网络超时，优先处理高频失败通道。" \
		"按 incident/event_type 汇总失败原因，评估重放或降噪动作。"
fi

status="READY"
if (( HAS_ALERT == 1 )); then
	status="ALERT"
elif (( HAS_DEGRADED == 1 )); then
	status="DEGRADED"
fi

echo "STATUS=${status}"
echo "METRICS_URL=${METRICS_URL}"
echo "WINDOW_SECONDS=${WINDOW_SECONDS}"
echo "SAMPLE_INTERVAL_SEC=${SAMPLE_INTERVAL_SEC}"
echo "DELTA_NORMALIZATION_FACTOR=${window_factor}"
echo "redis_pubsub_subscribe_ready value=${redis_ready_2}"
echo "ai_job_longpoll_fallback_total delta_raw=${longpoll_delta_raw} delta_window=${longpoll_delta_window}"
echo "mcp_rate_limited_total delta_raw=${mcp_rate_limited_delta_raw} delta_window=${mcp_rate_limited_delta_window}"
echo "mcp_scope_denied_total delta_raw=${mcp_scope_denied_delta_raw} delta_window=${mcp_scope_denied_delta_window}"
echo "mcp_truncated_total delta_raw=${mcp_truncated_delta_raw} delta_window=${mcp_truncated_delta_window}"
echo "notice_worker_claim_source_total{source=\"db_fallback\"} delta_raw=${notice_db_fallback_delta_raw} delta_window=${notice_db_fallback_delta_window}"
echo "notice_limiter_fallback_total delta_raw=${notice_limiter_fallback_delta_raw} delta_window=${notice_limiter_fallback_delta_window}"
echo "notice_limiter_deny_total delta_raw=${notice_limiter_deny_delta_raw} delta_window=${notice_limiter_deny_delta_window}"

if (( notice_failed_total_exists == 1 )); then
	echo "notice_delivery_failed_total delta_raw=${notice_failed_delta_raw} delta_window=${notice_failed_delta_window}"
else
	echo "notice_delivery_failed_total status=NOT_PRESENT"
fi

if (( notice_send_total_exists == 1 )); then
	echo "notice_delivery_send_total{status=succeeded} delta_raw=${notice_send_succeeded_delta_raw} delta_window=${notice_send_succeeded_delta_window}"
	echo "notice_delivery_send_total{status=failed} delta_raw=${notice_send_failed_delta_raw} delta_window=${notice_send_failed_delta_window}"
	echo "notice_delivery_send_total{status=pending} delta_raw=${notice_send_pending_delta_raw} delta_window=${notice_send_pending_delta_window}"
else
	echo "notice_delivery_send_total status=NOT_PRESENT"
fi

echo "mcp_calls_total_by_code:"
awk -F '\t' '{printf "code=%s value=%.6f delta_raw=%.6f delta_window=%.6f\n", $1, $2, $3, $4}' "${MCP_CODE_FILE}" | LC_ALL=C sort

if [[ -n "${ALERT_REASONS}" ]]; then
	echo "ALERT_REASONS=${ALERT_REASONS}"
else
	echo "ALERT_REASONS=none"
fi
if [[ -n "${DEGRADED_REASONS}" ]]; then
	echo "DEGRADED_REASONS=${DEGRADED_REASONS}"
else
	echo "DEGRADED_REASONS=none"
fi

if (( ANOMALY_COUNT == 0 )); then
	echo "TOPN_ANOMALIES=none"
else
	echo "TOPN_ANOMALIES:"
	rank=0
	while IFS=$'\t' read -r delta severity metric detail action1 action2; do
		rank=$((rank + 1))
		echo "${rank}) severity=${severity} metric=${metric} delta_window=${delta} detail=${detail}"
		echo "   ACTION1=${action1}"
		if [[ -n "${action2}" ]]; then
			echo "   ACTION2=${action2}"
		fi
		if (( rank >= TOPN )); then
			break
		fi
	done < <(LC_ALL=C sort -t$'\t' -k1,1nr "${ANOMALY_FILE}")
fi

print_db_topn
