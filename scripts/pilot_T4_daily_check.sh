#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
METRICS_URL="${METRICS_URL:-${BASE_URL}/metrics}"
DB_DSN="${DB_DSN:-}"
ALLOWED_NAMESPACES="${ALLOWED_NAMESPACES:-}"
ALLOWED_SERVICES="${ALLOWED_SERVICES:-}"
RUN_EXPLAIN="${RUN_EXPLAIN:-0}"

METRICS_SCRIPT="${SCRIPT_DIR}/metrics_snapshot.sh"
TTL_PREVIEW_SCRIPT="${SCRIPT_DIR}/ttl_preview.sh"
EXPLAIN_SCRIPT="${SCRIPT_DIR}/explain_pack.sh"

LAST_COMMAND=""
LAST_OUTPUT=""
TMP_FILES=()

truncate_2kb() {
	printf '%s' "$1" | tail -c 2048
}

cleanup() {
	local f
	for f in "${TMP_FILES[@]:-}"; do
		[[ -n "${f}" ]] && rm -f "${f}" >/dev/null 2>&1 || true
	done
}
trap cleanup EXIT

fail_step() {
	local step="$1"
	local code="$2"
	local cmd="${3:-${LAST_COMMAND}}"
	local body="${4:-${LAST_OUTPUT}}"

	echo "FAIL T4-DAILY step=${step} exit_code=${code}"
	echo "command=${cmd}"
	echo "output<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	exit 1
}

need_file_or_fail() {
	local step="$1"
	local path="$2"
	if [[ ! -f "${path}" ]]; then
		fail_step "${step}" "127" "test -f ${path}" "script not found: ${path}"
	fi
}

run_step() {
	local step="$1"
	shift

	local tmp rc
	tmp="$(mktemp)"
	TMP_FILES+=("${tmp}")
	LAST_COMMAND="$(printf '%q ' "$@")"

	set +e
	"$@" >"${tmp}" 2>&1
	rc=$?
	set -e

	LAST_OUTPUT="$(cat "${tmp}")"
	if (( rc != 0 )); then
		fail_step "${step}" "${rc}" "${LAST_COMMAND}" "${LAST_OUTPUT}"
	fi

	printf '%s' "${LAST_OUTPUT}"
}

extract_metrics_state_or_fail() {
	local metrics_output="$1"
	local overall_line state

	overall_line="$(printf '%s\n' "${metrics_output}" | awk '/^overall=/{line=$0} END{print line}')"
	if [[ -z "${overall_line}" ]]; then
		fail_step "metrics_snapshot.parse_overall" "1" "${LAST_COMMAND}" "${metrics_output}"
	fi
	state="$(printf '%s' "${overall_line}" | sed -E 's/^overall=([A-Z]+).*/\1/')"
	if [[ "${state}" != "READY" && "${state}" != "DEGRADED" ]]; then
		fail_step "metrics_snapshot.invalid_overall" "1" "${LAST_COMMAND}" "${overall_line}"
	fi
	printf '%s' "${state}"
}

extract_ttl_max_table() {
	local ttl_output="$1"
	printf '%s\n' "${ttl_output}" | awk '
		$1 ~ /^target=/ {
			target = ""
			count = ""
			for (i = 1; i <= NF; i++) {
				if ($i ~ /^target=/) {
					target = $i
					sub(/^target=/, "", target)
				}
				if ($i ~ /^would_delete=/) {
					count = $i
					sub(/^would_delete=/, "", count)
				}
			}
			if (target != "" && count != "") {
				c = count + 0
				if (found == 0 || c > max_count) {
					found = 1
					max_count = c
					max_target = target
				}
			}
		}
		END {
			if (found == 0) {
				print "NONE 0"
			} else {
				print max_target " " max_count
			}
		}
	'
}

if [[ "${RUN_EXPLAIN}" != "0" && "${RUN_EXPLAIN}" != "1" ]]; then
	fail_step "Precheck.RUN_EXPLAIN" "2" "RUN_EXPLAIN=${RUN_EXPLAIN}" "RUN_EXPLAIN must be 0 or 1"
fi

need_file_or_fail "Precheck.metrics_snapshot" "${METRICS_SCRIPT}"
need_file_or_fail "Precheck.ttl_preview" "${TTL_PREVIEW_SCRIPT}"
need_file_or_fail "Precheck.explain_pack" "${EXPLAIN_SCRIPT}"

metrics_output="$(
	run_step \
		"metrics_snapshot" \
		env \
		BASE_URL="${BASE_URL}" \
		METRICS_URL="${METRICS_URL}" \
		bash "${METRICS_SCRIPT}"
)"
metrics_state="$(extract_metrics_state_or_fail "${metrics_output}")"

ttl_output="$(
	run_step \
		"ttl_preview" \
		env \
		DB_DSN="${DB_DSN}" \
		MYSQL_HOST="${MYSQL_HOST:-}" \
		MYSQL_PORT="${MYSQL_PORT:-}" \
		MYSQL_USER="${MYSQL_USER:-}" \
		MYSQL_PASSWORD="${MYSQL_PASSWORD:-}" \
		MYSQL_DATABASE="${MYSQL_DATABASE:-}" \
		bash "${TTL_PREVIEW_SCRIPT}"
)"

ttl_max_line="$(extract_ttl_max_table "${ttl_output}")"
ttl_max_table="$(printf '%s' "${ttl_max_line}" | awk '{print $1}')"
ttl_max_count="$(printf '%s' "${ttl_max_line}" | awk '{print $2}')"

if [[ -z "${ttl_max_table}" ]]; then
	ttl_max_table="NONE"
fi
if [[ -z "${ttl_max_count}" ]]; then
	ttl_max_count="0"
fi

if [[ "${RUN_EXPLAIN}" == "1" ]]; then
	explain_output="$(
		run_step \
			"explain_pack" \
			env \
			DB_DSN="${DB_DSN}" \
			MYSQL_HOST="${MYSQL_HOST:-}" \
			MYSQL_PORT="${MYSQL_PORT:-}" \
			MYSQL_USER="${MYSQL_USER:-}" \
			MYSQL_PASSWORD="${MYSQL_PASSWORD:-}" \
			MYSQL_DATABASE="${MYSQL_DATABASE:-}" \
			bash "${EXPLAIN_SCRIPT}"
	)"
	explain_tail="$(printf '%s\n' "${explain_output}" | awk 'NF{line=$0} END{print line}')"
else
	explain_tail="SKIP explain_pack RUN_EXPLAIN=0"
fi

allow_ns="${ALLOWED_NAMESPACES:-*}"
allow_svc="${ALLOWED_SERVICES:-*}"
if [[ -z "${allow_ns}" ]]; then
	allow_ns="*"
fi
if [[ -z "${allow_svc}" ]]; then
	allow_svc="*"
fi

echo "PASS T4-DAILY status=${metrics_state} ttl_max_table=${ttl_max_table} ttl_max_would_delete=${ttl_max_count} allowlist=namespaces:${allow_ns},services:${allow_svc}"
echo "metrics_overall=${metrics_state}"
echo "ttl_max_target=${ttl_max_table} ttl_max_would_delete=${ttl_max_count}"
echo "allowlist namespaces=${allow_ns} services=${allow_svc}"
echo "${explain_tail}"
