#!/usr/bin/env bash
set -euo pipefail

DB_DSN="${DB_DSN:-}"
MYSQL_HOST="${MYSQL_HOST:-${DB_HOST:-127.0.0.1}}"
MYSQL_PORT="${MYSQL_PORT:-${DB_PORT:-3306}}"
MYSQL_USER="${MYSQL_USER:-${MYSQL_USERNAME:-${DB_USER:-root}}}"
MYSQL_PASSWORD="${MYSQL_PASSWORD:-${MYSQL_PASS:-${DB_PASSWORD:-}}}"
MYSQL_DATABASE="${MYSQL_DATABASE:-${MYSQL_DB:-${DB_NAME:-}}}"

TTL_TOOL_CALLS_DAYS="${TTL_TOOL_CALLS_DAYS:-14}"
TTL_NOTICE_DELIVERIES_DAYS="${TTL_NOTICE_DELIVERIES_DAYS:-30}"
TTL_ALERT_EVENTS_HISTORY_DAYS="${TTL_ALERT_EVENTS_HISTORY_DAYS:-30}"
TTL_EVIDENCE_DAYS="${TTL_EVIDENCE_DAYS:-14}"
TTL_INCIDENT_TIMELINE_DAYS="${TTL_INCIDENT_TIMELINE_DAYS:-90}"

DB_ENGINE="mysql"

info() {
	echo "[INFO] $*"
}

warn() {
	echo "[WARN] $*"
}

fail() {
	echo "[FAIL] $*" >&2
	exit 1
}

need_cmd() {
	command -v "$1" >/dev/null 2>&1
}

is_uint() {
	[[ "$1" =~ ^[0-9]+$ ]]
}

normalize_days() {
	local name="$1"
	local value="$2"
	local fallback="$3"
	if ! is_uint "${value}" || (( value <= 0 )); then
		warn "${name} is invalid (${value}), fallback=${fallback}"
		printf '%s' "${fallback}"
		return
	fi
	printf '%s' "${value}"
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
	local out
	out="$(mysql_exec "${sql}" --skip-column-names | tr -d '\r' | head -n 1)"
	printf '%s' "${out}"
}

table_exists() {
	local table="$1"
	local count
	count="$(mysql_scalar "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = '${table}';")"
	[[ "${count}" == "1" ]]
}

column_exists() {
	local table="$1"
	local col="$2"
	local count
	count="$(mysql_scalar "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = '${table}' AND column_name = '${col}';")"
	[[ "${count}" == "1" ]]
}

pick_first_existing_column() {
	local table="$1"
	shift
	local col
	for col in "$@"; do
		if column_exists "${table}" "${col}"; then
			printf '%s' "${col}"
			return 0
		fi
	done
	return 1
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

preview_target() {
	local alias="$1"
	local source_table="$2"
	local where_clause="$3"
	local time_col="$4"
	local cond_summary="$5"
	local count oldest

	count="$(mysql_scalar "SELECT COUNT(*) FROM \`${source_table}\` WHERE ${where_clause};")"
	oldest="$(mysql_scalar "SELECT DATE_FORMAT(MIN(${time_col}), '%Y-%m-%dT%H:%i:%sZ') FROM \`${source_table}\` WHERE ${where_clause};")"
	if [[ -z "${oldest}" || "${oldest}" == "NULL" ]]; then
		oldest="NONE"
	fi

	echo "target=${alias} source_table=${source_table} would_delete=${count:-0} oldest=${oldest} condition=\"${cond_summary}\""
}

parse_dsn_if_present

TTL_TOOL_CALLS_DAYS="$(normalize_days "TTL_TOOL_CALLS_DAYS" "${TTL_TOOL_CALLS_DAYS}" "14")"
TTL_NOTICE_DELIVERIES_DAYS="$(normalize_days "TTL_NOTICE_DELIVERIES_DAYS" "${TTL_NOTICE_DELIVERIES_DAYS}" "30")"
TTL_ALERT_EVENTS_HISTORY_DAYS="$(normalize_days "TTL_ALERT_EVENTS_HISTORY_DAYS" "${TTL_ALERT_EVENTS_HISTORY_DAYS}" "30")"
TTL_EVIDENCE_DAYS="$(normalize_days "TTL_EVIDENCE_DAYS" "${TTL_EVIDENCE_DAYS}" "14")"
TTL_INCIDENT_TIMELINE_DAYS="$(normalize_days "TTL_INCIDENT_TIMELINE_DAYS" "${TTL_INCIDENT_TIMELINE_DAYS}" "90")"

if [[ "${DB_ENGINE}" == "sqlite" ]]; then
	info "ttl_preview skipped: sqlite detected (DB_DSN=${DB_DSN})"
	exit 0
fi

need_cmd mysql || fail "mysql client not found"
[[ -n "${MYSQL_DATABASE}" ]] || fail "database name is required (MYSQL_DATABASE/MYSQL_DB/DB_NAME or DB_DSN)"

mysql_exec "SELECT 1;" --skip-column-names >/dev/null

info "ttl_preview start database=${MYSQL_DATABASE} host=${MYSQL_HOST}:${MYSQL_PORT}"

# tool_calls (fallback to ai_tool_calls when needed)
if tool_table="$(resolve_tool_calls_table)"; then
	if time_col="$(pick_first_existing_column "${tool_table}" "created_at" "updated_at")"; then
		preview_target \
			"tool_calls" \
			"${tool_table}" \
			"${time_col} < DATE_SUB(UTC_TIMESTAMP(), INTERVAL ${TTL_TOOL_CALLS_DAYS} DAY)" \
			"${time_col}" \
			"${time_col} < now-${TTL_TOOL_CALLS_DAYS}d"
	else
		warn "target=tool_calls source_table=${tool_table} skipped: no created_at/updated_at column"
	fi
else
	warn "target=tool_calls skipped: table tool_calls/ai_tool_calls not found"
fi

# notice_deliveries
if table_exists "notice_deliveries"; then
	time_col="$(pick_first_existing_column "notice_deliveries" "created_at" "next_retry_at")" || time_col=""
	if [[ -n "${time_col}" ]]; then
		preview_target \
			"notice_deliveries" \
			"notice_deliveries" \
			"status IN ('succeeded','failed','canceled') AND ${time_col} < DATE_SUB(UTC_TIMESTAMP(), INTERVAL ${TTL_NOTICE_DELIVERIES_DAYS} DAY)" \
			"${time_col}" \
			"status in terminal and ${time_col} < now-${TTL_NOTICE_DELIVERIES_DAYS}d"
	else
		warn "target=notice_deliveries skipped: no created_at/next_retry_at column"
	fi
else
	warn "target=notice_deliveries skipped: table not found"
fi

# alert_events_history
if table_exists "alert_events_history"; then
	time_col="$(pick_first_existing_column "alert_events_history" "last_seen_at" "created_at")" || time_col=""
	if [[ -n "${time_col}" ]]; then
		preview_target \
			"alert_events_history" \
			"alert_events_history" \
			"is_current = 0 AND ${time_col} < DATE_SUB(UTC_TIMESTAMP(), INTERVAL ${TTL_ALERT_EVENTS_HISTORY_DAYS} DAY)" \
			"${time_col}" \
			"is_current=0 and ${time_col} < now-${TTL_ALERT_EVENTS_HISTORY_DAYS}d"
	else
		warn "target=alert_events_history skipped: no last_seen_at/created_at column"
	fi
else
	warn "target=alert_events_history skipped: table not found"
fi

# evidence
if table_exists "evidence"; then
	time_col="$(pick_first_existing_column "evidence" "created_at" "updated_at")" || time_col=""
	if [[ -n "${time_col}" ]]; then
		preview_target \
			"evidence" \
			"evidence" \
			"${time_col} < DATE_SUB(UTC_TIMESTAMP(), INTERVAL ${TTL_EVIDENCE_DAYS} DAY)" \
			"${time_col}" \
			"${time_col} < now-${TTL_EVIDENCE_DAYS}d"
	else
		warn "target=evidence skipped: no created_at/updated_at column"
	fi
else
	warn "target=evidence skipped: table not found"
fi

# incident_timeline (optional)
if table_exists "incident_timeline"; then
	time_col="$(pick_first_existing_column "incident_timeline" "created_at" "updated_at")" || time_col=""
	if [[ -n "${time_col}" ]]; then
		preview_target \
			"incident_timeline" \
			"incident_timeline" \
			"${time_col} < DATE_SUB(UTC_TIMESTAMP(), INTERVAL ${TTL_INCIDENT_TIMELINE_DAYS} DAY)" \
			"${time_col}" \
			"${time_col} < now-${TTL_INCIDENT_TIMELINE_DAYS}d"
	else
		warn "target=incident_timeline skipped: no created_at/updated_at column"
	fi
else
	warn "target=incident_timeline skipped: table not found"
fi

info "ttl_preview done"
