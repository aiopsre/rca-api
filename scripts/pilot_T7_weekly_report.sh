#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
METRICS_URL="${METRICS_URL:-${BASE_URL}/metrics}"
FROM="${FROM:-}"
TO="${TO:-}"
WINDOW_DAYS="${WINDOW_DAYS:-7}"
TOPN="${TOPN:-5}"

DB_DSN="${DB_DSN:-}"
MYSQL_HOST="${MYSQL_HOST:-${DB_HOST:-127.0.0.1}}"
MYSQL_PORT="${MYSQL_PORT:-${DB_PORT:-3306}}"
MYSQL_USER="${MYSQL_USER:-${MYSQL_USERNAME:-${DB_USER:-root}}}"
MYSQL_PASSWORD="${MYSQL_PASSWORD:-${MYSQL_PASS:-${DB_PASSWORD:-}}}"
MYSQL_DATABASE="${MYSQL_DATABASE:-${MYSQL_DB:-${DB_NAME:-}}}"
SQLITE_DSN=""

DB_ENGINE="mysql"
DB_AVAILABLE=0

INGEST_SOURCE="none"
INGEST_TOTAL=0
INGEST_SILENCED=0
INGEST_PROGRESSED=0
INGEST_NEW_INCIDENT=0
INGEST_MERGED=0

NOTICE_SUCCEEDED=0
NOTICE_FAILED=0
NOTICE_PENDING=0

PLAYBOOK_TOTAL=0
PLAYBOOK_HIT=0
PLAYBOOK_HIT_RATE="0.00"

VERIFICATION_TRUE=0
VERIFICATION_FALSE=0

TOOL_TOP="[]"
TOP_SERVICES="[]"
TOP_FINGERPRINTS="[]"
TOP_ROOT_CAUSE_TYPES="[]"

info() {
	echo "[INFO] $*" >&2
}

warn() {
	echo "[WARN] $*" >&2
}

need_cmd() {
	command -v "$1" >/dev/null 2>&1
}

is_uint() {
	[[ "$1" =~ ^[0-9]+$ ]]
}

normalize_uint() {
	local value="$1"
	local fallback="$2"
	if ! is_uint "${value}" || (( value <= 0 )); then
		printf '%s' "${fallback}"
		return
	fi
	printf '%s' "${value}"
}

normalize_count() {
	local value
	value="$(printf '%s' "${1:-}" | tr -d '\r' | xargs)"
	if [[ "${value}" =~ ^-?[0-9]+$ ]]; then
		printf '%s' "${value}"
		return
	fi
	printf '%s' "0"
}

parse_dsn_if_present() {
	local dsn
	dsn="$(printf '%s' "${DB_DSN}" | xargs)"
	[[ -z "${dsn}" ]] && return 0

	if [[ "${dsn}" == sqlite:* ]] || [[ "${dsn}" == file:* ]] || [[ "${dsn}" == *.db ]]; then
		DB_ENGINE="sqlite"
		SQLITE_DSN="${dsn}"
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
	local out=""
	set +e
	out="$(mysql_exec "${sql}" --skip-column-names 2>/dev/null | head -n 1)"
	set -e
	printf '%s' "$(normalize_count "${out}")"
}

mysql_rows() {
	local sql="$1"
	set +e
	mysql_exec "${sql}" --skip-column-names 2>/dev/null
	set -e
}

sqlite_exec() {
	local sql="$1"
	sqlite3 "${SQLITE_DSN}" "${sql}"
}

sqlite_scalar() {
	local sql="$1"
	local out=""
	set +e
	out="$(sqlite_exec "${sql}" 2>/dev/null | head -n 1)"
	set -e
	printf '%s' "$(normalize_count "${out}")"
}

sqlite_rows() {
	local sql="$1"
	set +e
	sqlite_exec "${sql}" 2>/dev/null
	set -e
}

table_exists() {
	local table="$1"
	if [[ "${DB_AVAILABLE}" != "1" ]]; then
		return 1
	fi
	if [[ "${DB_ENGINE}" == "mysql" ]]; then
		local count
		count="$(mysql_scalar "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = '${table}';")"
		[[ "${count}" == "1" ]]
		return
	fi
	local count
	count="$(sqlite_scalar "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='${table}';")"
	[[ "${count}" == "1" ]]
}

time_predicate() {
	local column="$1"
	if [[ -n "${FROM}" && -n "${TO}" ]]; then
		printf "%s >= '%s' AND %s <= '%s'" "${column}" "${FROM}" "${column}" "${TO}"
		return
	fi
	if [[ -n "${FROM}" ]]; then
		printf "%s >= '%s'" "${column}" "${FROM}"
		return
	fi
	if [[ -n "${TO}" ]]; then
		printf "%s <= '%s'" "${column}" "${TO}"
		return
	fi
	if [[ "${DB_ENGINE}" == "mysql" ]]; then
		printf "%s >= DATE_SUB(UTC_TIMESTAMP(), INTERVAL %s DAY) AND %s <= UTC_TIMESTAMP()" "${column}" "${WINDOW_DAYS}" "${column}"
		return
	fi
	printf "%s >= DATETIME('now', '-%s day') AND %s <= DATETIME('now')" "${column}" "${WINDOW_DAYS}" "${column}"
}

format_top_list() {
	local rows="$1"
	if [[ -z "${rows}" ]]; then
		printf '[]'
		return
	fi
	local out=""
	while IFS=$'\t' read -r value count; do
		[[ -z "${value}" ]] && continue
		count="$(normalize_count "${count}")"
		value="${value//$'\r'/}"
		value="${value//,/;}"
		value="${value//$'\n'/ }"
		if [[ -n "${out}" ]]; then
			out="${out},${value}:${count}"
		else
			out="${value}:${count}"
		fi
	done <<<"${rows}"

	if [[ -z "${out}" ]]; then
		printf '[]'
		return
	fi
	printf '[%s]' "${out}"
}

metric_sum() {
	local body="$1"
	local metric="$2"
	printf '%s\n' "${body}" | awk -v name="${metric}" '
		$1 ~ ("^" name "(\\{|$)") {
			sum += ($NF + 0)
		}
		END { printf "%.0f", sum + 0 }
	'
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

WINDOW_DAYS="$(normalize_uint "${WINDOW_DAYS}" "7")"
TOPN="$(normalize_uint "${TOPN}" "5")"
parse_dsn_if_present

if [[ "${DB_ENGINE}" == "mysql" ]]; then
	if need_cmd mysql && [[ -n "${MYSQL_DATABASE}" ]]; then
		set +e
		mysql_exec "SELECT 1;" --skip-column-names >/dev/null 2>&1
		rc=$?
		set -e
		if (( rc == 0 )); then
			DB_AVAILABLE=1
		else
			warn "mysql connect failed: host=${MYSQL_HOST} port=${MYSQL_PORT} database=${MYSQL_DATABASE}"
		fi
	else
		warn "mysql client or MYSQL_DATABASE missing; fallback to metrics/zero"
	fi
else
	if need_cmd sqlite3 && [[ -n "${SQLITE_DSN}" ]]; then
		set +e
		sqlite_exec "SELECT 1;" >/dev/null 2>&1
		rc=$?
		set -e
		if (( rc == 0 )); then
			DB_AVAILABLE=1
		else
			warn "sqlite connect failed: dsn=${SQLITE_DSN}"
		fi
	else
		warn "sqlite3 missing or sqlite dsn empty; fallback to metrics/zero"
	fi
fi

if [[ "${DB_AVAILABLE}" == "1" ]] && table_exists "alert_events_history"; then
	pred_alert="$(time_predicate "created_at")"
	pred_incident="$(time_predicate "created_at")"
	if [[ "${DB_ENGINE}" == "mysql" ]]; then
		INGEST_TOTAL="$(mysql_scalar "SELECT COUNT(*) FROM alert_events_history WHERE ${pred_alert};")"
		INGEST_SILENCED="$(mysql_scalar "SELECT COUNT(*) FROM alert_events_history WHERE ${pred_alert} AND is_silenced = 1;")"
		INGEST_PROGRESSED="$(mysql_scalar "SELECT COUNT(*) FROM alert_events_history WHERE ${pred_alert} AND incident_id IS NOT NULL AND incident_id <> '';")"
		if table_exists "incidents"; then
			INGEST_NEW_INCIDENT="$(mysql_scalar "SELECT COUNT(*) FROM incidents WHERE ${pred_incident};")"
		fi
	else
		INGEST_TOTAL="$(sqlite_scalar "SELECT COUNT(*) FROM alert_events_history WHERE ${pred_alert};")"
		INGEST_SILENCED="$(sqlite_scalar "SELECT COUNT(*) FROM alert_events_history WHERE ${pred_alert} AND is_silenced = 1;")"
		INGEST_PROGRESSED="$(sqlite_scalar "SELECT COUNT(*) FROM alert_events_history WHERE ${pred_alert} AND incident_id IS NOT NULL AND incident_id <> '';")"
		if table_exists "incidents"; then
			INGEST_NEW_INCIDENT="$(sqlite_scalar "SELECT COUNT(*) FROM incidents WHERE ${pred_incident};")"
		fi
	fi
	INGEST_SOURCE="db"
fi

if [[ "${INGEST_SOURCE}" == "none" ]]; then
	set +e
	metrics_body="$(curl -sS --connect-timeout 5 --max-time 10 "${METRICS_URL}")"
	rc=$?
	set -e
	if (( rc == 0 )) && [[ -n "${metrics_body}" ]]; then
		INGEST_TOTAL="$(normalize_count "$(metric_sum "${metrics_body}" "alert_ingest_total")")"
		INGEST_SILENCED="$(normalize_count "$(metric_sum "${metrics_body}" "alert_ingest_silenced_total")")"
		INGEST_PROGRESSED="$(normalize_count "$(metric_sum "${metrics_body}" "alert_ingest_progressed_total")")"
		INGEST_MERGED="$(normalize_count "$(metric_sum "${metrics_body}" "alert_ingest_merged_total")")"
		INGEST_NEW_INCIDENT="$(normalize_count "$(metric_sum "${metrics_body}" "alert_ingest_new_incident_total")")"
		INGEST_SOURCE="metrics"
	else
		INGEST_SOURCE="none"
	fi
fi

if [[ "${INGEST_SOURCE}" == "db" ]]; then
	merged_calc=$(( INGEST_PROGRESSED - INGEST_NEW_INCIDENT ))
	if (( merged_calc < 0 )); then
		merged_calc=0
	fi
	INGEST_MERGED="${merged_calc}"
fi

if [[ "${DB_AVAILABLE}" == "1" ]] && table_exists "notice_deliveries"; then
	pred_notice="$(time_predicate "created_at")"
	if [[ "${DB_ENGINE}" == "mysql" ]]; then
		NOTICE_SUCCEEDED="$(mysql_scalar "SELECT COUNT(*) FROM notice_deliveries WHERE ${pred_notice} AND status = 'succeeded';")"
		NOTICE_FAILED="$(mysql_scalar "SELECT COUNT(*) FROM notice_deliveries WHERE ${pred_notice} AND status = 'failed';")"
		NOTICE_PENDING="$(mysql_scalar "SELECT COUNT(*) FROM notice_deliveries WHERE ${pred_notice} AND status = 'pending';")"
	else
		NOTICE_SUCCEEDED="$(sqlite_scalar "SELECT COUNT(*) FROM notice_deliveries WHERE ${pred_notice} AND status = 'succeeded';")"
		NOTICE_FAILED="$(sqlite_scalar "SELECT COUNT(*) FROM notice_deliveries WHERE ${pred_notice} AND status = 'failed';")"
		NOTICE_PENDING="$(sqlite_scalar "SELECT COUNT(*) FROM notice_deliveries WHERE ${pred_notice} AND status = 'pending';")"
	fi
fi

if [[ "${DB_AVAILABLE}" == "1" ]] && table_exists "incidents"; then
	pred_incident="$(time_predicate "created_at")"
	if [[ "${DB_ENGINE}" == "mysql" ]]; then
		PLAYBOOK_TOTAL="$(mysql_scalar "SELECT COUNT(*) FROM incidents WHERE ${pred_incident};")"
		PLAYBOOK_HIT="$(mysql_scalar "SELECT COUNT(*) FROM incidents WHERE ${pred_incident} AND diagnosis_json IS NOT NULL AND diagnosis_json <> '' AND diagnosis_json LIKE '%\"playbook\"%';")"
		TOP_SERVICES="$(format_top_list "$(mysql_rows "SELECT COALESCE(NULLIF(service,''),'unknown') AS v, COUNT(*) AS c FROM incidents WHERE ${pred_incident} GROUP BY v ORDER BY c DESC LIMIT ${TOPN};")")"
		TOP_FINGERPRINTS="$(format_top_list "$(mysql_rows "SELECT COALESCE(NULLIF(fingerprint,''),'unknown') AS v, COUNT(*) AS c FROM incidents WHERE ${pred_incident} GROUP BY v ORDER BY c DESC LIMIT ${TOPN};")")"
		TOP_ROOT_CAUSE_TYPES="$(format_top_list "$(mysql_rows "SELECT COALESCE(NULLIF(root_cause_type,''),'unknown') AS v, COUNT(*) AS c FROM incidents WHERE ${pred_incident} GROUP BY v ORDER BY c DESC LIMIT ${TOPN};")")"
	else
		PLAYBOOK_TOTAL="$(sqlite_scalar "SELECT COUNT(*) FROM incidents WHERE ${pred_incident};")"
		PLAYBOOK_HIT="$(sqlite_scalar "SELECT COUNT(*) FROM incidents WHERE ${pred_incident} AND diagnosis_json IS NOT NULL AND diagnosis_json <> '' AND diagnosis_json LIKE '%\"playbook\"%';")"
		TOP_SERVICES="$(format_top_list "$(sqlite_rows "SELECT COALESCE(NULLIF(service,''),'unknown') AS v, COUNT(*) AS c FROM incidents WHERE ${pred_incident} GROUP BY v ORDER BY c DESC LIMIT ${TOPN};")")"
		TOP_FINGERPRINTS="$(format_top_list "$(sqlite_rows "SELECT COALESCE(NULLIF(fingerprint,''),'unknown') AS v, COUNT(*) AS c FROM incidents WHERE ${pred_incident} GROUP BY v ORDER BY c DESC LIMIT ${TOPN};")")"
		TOP_ROOT_CAUSE_TYPES="$(format_top_list "$(sqlite_rows "SELECT COALESCE(NULLIF(root_cause_type,''),'unknown') AS v, COUNT(*) AS c FROM incidents WHERE ${pred_incident} GROUP BY v ORDER BY c DESC LIMIT ${TOPN};")")"
	fi
fi

if (( PLAYBOOK_TOTAL > 0 )); then
	PLAYBOOK_HIT_RATE="$(awk -v h="${PLAYBOOK_HIT}" -v t="${PLAYBOOK_TOTAL}" 'BEGIN { printf "%.2f", (h + 0) * 100 / (t + 0) }')"
fi

if [[ "${DB_AVAILABLE}" == "1" ]] && table_exists "incident_verification_runs"; then
	pred_verification="$(time_predicate "created_at")"
	if [[ "${DB_ENGINE}" == "mysql" ]]; then
		VERIFICATION_TRUE="$(mysql_scalar "SELECT COUNT(*) FROM incident_verification_runs WHERE ${pred_verification} AND meets_expectation = 1;")"
		VERIFICATION_FALSE="$(mysql_scalar "SELECT COUNT(*) FROM incident_verification_runs WHERE ${pred_verification} AND meets_expectation = 0;")"
	else
		VERIFICATION_TRUE="$(sqlite_scalar "SELECT COUNT(*) FROM incident_verification_runs WHERE ${pred_verification} AND meets_expectation = 1;")"
		VERIFICATION_FALSE="$(sqlite_scalar "SELECT COUNT(*) FROM incident_verification_runs WHERE ${pred_verification} AND meets_expectation = 0;")"
	fi
fi

if [[ "${DB_AVAILABLE}" == "1" ]]; then
	if tool_table="$(resolve_tool_calls_table)"; then
		pred_tool="$(time_predicate "created_at")"
		if [[ "${DB_ENGINE}" == "mysql" ]]; then
			TOOL_TOP="$(format_top_list "$(mysql_rows "SELECT CONCAT(COALESCE(NULLIF(tool_name,''),'unknown'),'|',CASE WHEN response_json IS NOT NULL AND JSON_VALID(response_json) THEN COALESCE(NULLIF(JSON_UNQUOTE(JSON_EXTRACT(response_json,'$.code')),''), COALESCE(NULLIF(status,''),'unknown')) ELSE COALESCE(NULLIF(status,''),'unknown') END) AS v, COUNT(*) AS c FROM ${tool_table} WHERE ${pred_tool} GROUP BY v ORDER BY c DESC LIMIT ${TOPN};")")"
		else
			TOOL_TOP="$(format_top_list "$(sqlite_rows "SELECT (COALESCE(NULLIF(tool_name,''),'unknown') || '|' || CASE WHEN response_json IS NOT NULL AND json_valid(response_json) THEN COALESCE(NULLIF(json_extract(response_json,'$.code'),''), COALESCE(NULLIF(status,''),'unknown')) ELSE COALESCE(NULLIF(status,''),'unknown') END) AS v, COUNT(*) AS c FROM ${tool_table} WHERE ${pred_tool} GROUP BY v ORDER BY c DESC LIMIT ${TOPN};")")"
		fi
	fi
fi

echo "report=T7_WEEKLY"
echo "window.from=${FROM:-AUTO_${WINDOW_DAYS}D}"
echo "window.to=${TO:-NOW_UTC}"
echo "db.engine=${DB_ENGINE}"
echo "db.available=${DB_AVAILABLE}"

echo "ingest.source=${INGEST_SOURCE}"
echo "ingest.total=${INGEST_TOTAL}"
echo "ingest.silenced=${INGEST_SILENCED}"
echo "ingest.progressed=${INGEST_PROGRESSED}"
echo "ingest.new_incident=${INGEST_NEW_INCIDENT}"
echo "ingest.merged=${INGEST_MERGED}"

echo "notice.succeeded=${NOTICE_SUCCEEDED}"
echo "notice.failed=${NOTICE_FAILED}"
echo "notice.pending=${NOTICE_PENDING}"

echo "tool_calls.top=${TOOL_TOP}"
echo "playbook.total_incidents=${PLAYBOOK_TOTAL}"
echo "playbook.hit=${PLAYBOOK_HIT}"
echo "playbook.hit_rate_pct=${PLAYBOOK_HIT_RATE}"

echo "verification_runs.true=${VERIFICATION_TRUE}"
echo "verification_runs.false=${VERIFICATION_FALSE}"

echo "top.services=${TOP_SERVICES}"
echo "top.fingerprints=${TOP_FINGERPRINTS}"
echo "top.root_cause_types=${TOP_ROOT_CAUSE_TYPES}"
