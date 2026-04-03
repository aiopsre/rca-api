#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
METRICS_URL="${METRICS_URL:-${BASE_URL}/metrics}"
CURL="${CURL:-curl}"
CURL_CONNECT_TIMEOUT="${CURL_CONNECT_TIMEOUT:-5}"
CURL_MAX_TIME="${CURL_MAX_TIME:-10}"
SAMPLE_INTERVAL_SEC="${SAMPLE_INTERVAL_SEC:-1}"

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

normalize_interval() {
	local value="$1"
	if ! is_uint "${value}" || (( value <= 0 )); then
		printf '%s' "1"
		return
	fi
	printf '%s' "${value}"
}

fetch_metrics() {
	local body rc
	set +e
	body="$(${CURL} -sS --connect-timeout "${CURL_CONNECT_TIMEOUT}" --max-time "${CURL_MAX_TIME}" "${METRICS_URL}")"
	rc=$?
	set -e
	if (( rc != 0 )); then
		return 1
	fi
	printf '%s' "${body}"
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

write_mcp_calls_by_code() {
	local body="$1"
	local out_file="$2"
	printf '%s\n' "${body}" | awk '
		$1 ~ /^mcp_calls_total\{/ {
			labels = $1
			sub(/^mcp_calls_total\{/, "", labels)
			sub(/\}$/, "", labels)
			code = "UNKNOWN"
			n = split(labels, parts, ",")
			for (i = 1; i <= n; i++) {
				split(parts[i], kv, "=")
				key = kv[1]
				val = kv[2]
				gsub(/^[[:space:]]+|[[:space:]]+$/, "", key)
				gsub(/"/, "", val)
				if (key == "code") {
					code = val
				}
			}
			sum[code] += ($NF + 0)
		}
		END {
			for (k in sum) {
				printf "%s\t%.6f\n", k, sum[k]
			}
		}
	' | LC_ALL=C sort >"${out_file}"
}

calc_delta() {
	local before="$1"
	local after="$2"
	awk -v a="${after}" -v b="${before}" 'BEGIN { printf "%.6f", (a + 0) - (b + 0) }'
}

float_gt_zero() {
	local value="$1"
	awk -v v="${value}" 'BEGIN { exit !((v + 0) > 0) }'
}

SAMPLE_INTERVAL_SEC="$(normalize_interval "${SAMPLE_INTERVAL_SEC}")"

need_cmd "${CURL}" || fail "curl is required"
need_cmd awk || fail "awk is required"

sample1="$(fetch_metrics)" || fail "cannot fetch metrics from ${METRICS_URL}"
sleep "${SAMPLE_INTERVAL_SEC}"
sample2="$(fetch_metrics)" || fail "cannot fetch metrics from ${METRICS_URL}"

tmp_codes_1="$(mktemp)"
tmp_codes_2="$(mktemp)"
trap 'rm -f "${tmp_codes_1}" "${tmp_codes_2}"' EXIT

write_mcp_calls_by_code "${sample1}" "${tmp_codes_1}"
write_mcp_calls_by_code "${sample2}" "${tmp_codes_2}"

redis_ready_1="$(metric_sum "${sample1}" "redis_pubsub_subscribe_ready")"
redis_ready_2="$(metric_sum "${sample2}" "redis_pubsub_subscribe_ready")"
redis_ready_delta="$(calc_delta "${redis_ready_1}" "${redis_ready_2}")"

longpoll_fallback_1="$(metric_sum "${sample1}" "ai_job_longpoll_fallback_total")"
longpoll_fallback_2="$(metric_sum "${sample2}" "ai_job_longpoll_fallback_total")"
longpoll_fallback_delta="$(calc_delta "${longpoll_fallback_1}" "${longpoll_fallback_2}")"

mcp_scope_denied_1="$(metric_sum "${sample1}" "mcp_scope_denied_total")"
mcp_scope_denied_2="$(metric_sum "${sample2}" "mcp_scope_denied_total")"
mcp_scope_denied_delta="$(calc_delta "${mcp_scope_denied_1}" "${mcp_scope_denied_2}")"

mcp_rate_limited_1="$(metric_sum "${sample1}" "mcp_rate_limited_total")"
mcp_rate_limited_2="$(metric_sum "${sample2}" "mcp_rate_limited_total")"
mcp_rate_limited_delta="$(calc_delta "${mcp_rate_limited_1}" "${mcp_rate_limited_2}")"

mcp_truncated_1="$(metric_sum "${sample1}" "mcp_truncated_total")"
mcp_truncated_2="$(metric_sum "${sample2}" "mcp_truncated_total")"
mcp_truncated_delta="$(calc_delta "${mcp_truncated_1}" "${mcp_truncated_2}")"

notice_db_fallback_1="$(metric_sum "${sample1}" "notice_worker_claim_source_total" 'source="db_fallback"')"
notice_db_fallback_2="$(metric_sum "${sample2}" "notice_worker_claim_source_total" 'source="db_fallback"')"
notice_db_fallback_delta="$(calc_delta "${notice_db_fallback_1}" "${notice_db_fallback_2}")"

notice_limiter_fallback_1="$(metric_sum "${sample1}" "notice_limiter_fallback_total")"
notice_limiter_fallback_2="$(metric_sum "${sample2}" "notice_limiter_fallback_total")"
notice_limiter_fallback_delta="$(calc_delta "${notice_limiter_fallback_1}" "${notice_limiter_fallback_2}")"

notice_limiter_deny_1="$(metric_sum "${sample1}" "notice_limiter_deny_total")"
notice_limiter_deny_2="$(metric_sum "${sample2}" "notice_limiter_deny_total")"
notice_limiter_deny_delta="$(calc_delta "${notice_limiter_deny_1}" "${notice_limiter_deny_2}")"

echo "metrics_url=${METRICS_URL}"
echo "sample_interval_sec=${SAMPLE_INTERVAL_SEC}"
echo "redis_pubsub_subscribe_ready value=${redis_ready_2} delta=${redis_ready_delta}"
echo "ai_job_longpoll_fallback_total value=${longpoll_fallback_2} delta=${longpoll_fallback_delta}"
echo "mcp_scope_denied_total value=${mcp_scope_denied_2} delta=${mcp_scope_denied_delta}"
echo "mcp_rate_limited_total value=${mcp_rate_limited_2} delta=${mcp_rate_limited_delta}"
echo "mcp_truncated_total value=${mcp_truncated_2} delta=${mcp_truncated_delta}"
echo "notice_worker_claim_source_total{source=\"db_fallback\"} value=${notice_db_fallback_2} delta=${notice_db_fallback_delta}"
echo "notice_limiter_fallback_total value=${notice_limiter_fallback_2} delta=${notice_limiter_fallback_delta}"
echo "notice_limiter_deny_total value=${notice_limiter_deny_2} delta=${notice_limiter_deny_delta}"

echo "mcp_calls_total_by_code:"
awk '
	NR==FNR {
		before[$1] = $2
		seen[$1] = 1
		next
	}
	{
		after[$1] = $2
		seen[$1] = 1
	}
	END {
		for (k in seen) {
			b = (k in before) ? before[k] + 0 : 0
			a = (k in after) ? after[k] + 0 : 0
			d = a - b
			printf "code=%s value=%.6f delta=%.6f\n", k, a, d
		}
	}
' "${tmp_codes_1}" "${tmp_codes_2}" | LC_ALL=C sort

status="READY"
reasons=""

if awk -v v="${redis_ready_2}" 'BEGIN { exit !((v + 0) <= 0) }'; then
	status="DEGRADED"
	reasons="${reasons};redis_pubsub_subscribe_ready==0"
fi
if float_gt_zero "${longpoll_fallback_delta}"; then
	status="DEGRADED"
	reasons="${reasons};ai_job_longpoll_fallback_total_delta>0"
fi
if float_gt_zero "${notice_db_fallback_delta}"; then
	status="DEGRADED"
	reasons="${reasons};notice_worker_claim_source_total{source=db_fallback}_delta>0"
fi
if float_gt_zero "${notice_limiter_fallback_delta}"; then
	status="DEGRADED"
	reasons="${reasons};notice_limiter_fallback_total_delta>0"
fi

if [[ -n "${reasons}" ]]; then
	reasons="${reasons#;}"
	echo "overall=${status} reasons=${reasons}"
else
	echo "overall=${status} reasons=none"
fi
