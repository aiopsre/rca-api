#!/usr/bin/env bash
set -euo pipefail

DB_DSN="${DB_DSN:-}"
MYSQL_HOST="${MYSQL_HOST:-${DB_HOST:-127.0.0.1}}"
MYSQL_PORT="${MYSQL_PORT:-${DB_PORT:-3306}}"
MYSQL_USER="${MYSQL_USER:-${MYSQL_USERNAME:-${DB_USER:-root}}}"
MYSQL_PASSWORD="${MYSQL_PASSWORD:-${MYSQL_PASS:-${DB_PASSWORD:-}}}"
MYSQL_DATABASE="${MYSQL_DATABASE:-${MYSQL_DB:-${DB_NAME:-}}}"

DB_ENGINE="mysql"
WARN_COUNT=0
PASS_COUNT=0

info() {
	echo "[INFO] $*"
}

warn() {
	echo "[WARN] $*"
}

need_cmd() {
	command -v "$1" >/dev/null 2>&1
}

is_uint() {
	[[ "$1" =~ ^[0-9]+$ ]]
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
	out="$(mysql_exec "${sql}" --skip-column-names 2>/dev/null | tr -d '\r' | head -n 1)"
	printf '%s' "${out}"
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

parse_explain_first_row() {
	local raw="$1"
	printf '%s\n' "${raw}" | awk -F '\t' '
		NR==1 {
			for (i=1; i<=NF; i++) idx[$i]=i
			next
		}
		NR>=2 {
			table = (idx["table"] ? $(idx["table"]) : "")
			typev = (idx["type"] ? $(idx["type"]) : "")
			keyv = (idx["key"] ? $(idx["key"]) : "")
			rowsv = (idx["rows"] ? $(idx["rows"]) : "")
			extra = (idx["Extra"] ? $(idx["Extra"]) : "")
			gsub(/^[[:space:]]+|[[:space:]]+$/, "", keyv)
			gsub(/^[[:space:]]+|[[:space:]]+$/, "", extra)
			print table "\t" typev "\t" keyv "\t" rowsv "\t" extra
			exit
		}
	'
}

run_explain() {
	local query_name="$1"
	local sql="$2"
	local suggestion="$3"
	local raw parsed table_name typev keyv rowsv extra
	local rc

	set +e
	raw="$(mysql_exec "EXPLAIN ${sql}" 2>&1)"
	rc=$?
	set -e
	if (( rc != 0 )); then
		WARN_COUNT=$((WARN_COUNT + 1))
		warn "query=${query_name} status=WARN explain_failed detail=$(printf '%s' "${raw}" | head -c 300)"
		warn "query=${query_name} suggestion=${suggestion}"
		return 0
	fi

	parsed="$(parse_explain_first_row "${raw}")"
	if [[ -z "${parsed}" ]]; then
		WARN_COUNT=$((WARN_COUNT + 1))
		warn "query=${query_name} status=WARN explain_empty"
		warn "query=${query_name} suggestion=${suggestion}"
		return 0
	fi

	table_name="$(printf '%s' "${parsed}" | cut -f1)"
	typev="$(printf '%s' "${parsed}" | cut -f2)"
	keyv="$(printf '%s' "${parsed}" | cut -f3)"
	rowsv="$(printf '%s' "${parsed}" | cut -f4)"
	extra="$(printf '%s' "${parsed}" | cut -f5-)"

	echo "query=${query_name} table=${table_name:-unknown} type=${typev:-unknown} key=${keyv:-NONE} rows=${rowsv:-0} extra=\"${extra:-}\""

	local need_warn=0
	if [[ -z "${keyv}" || "${keyv}" == "NULL" ]]; then
		need_warn=1
	fi
	if [[ "${typev}" == "ALL" ]]; then
		need_warn=1
	fi
	if is_uint "${rowsv:-}" && (( rowsv > 20000 )); then
		need_warn=1
	fi
	if [[ "${extra}" == *"Using filesort"* ]]; then
		need_warn=1
	fi

	if (( need_warn == 1 )); then
		WARN_COUNT=$((WARN_COUNT + 1))
		warn "query=${query_name} status=WARN suggestion=${suggestion}"
	else
		PASS_COUNT=$((PASS_COUNT + 1))
		info "query=${query_name} status=PASS"
	fi
}

parse_dsn_if_present

if [[ "${DB_ENGINE}" == "sqlite" ]]; then
	echo "SKIP explain_pack reason=sqlite_detected"
	exit 0
fi

if ! need_cmd mysql; then
	echo "WARN explain_pack reason=mysql_client_missing"
	exit 0
fi

if [[ -z "${MYSQL_DATABASE}" ]]; then
	echo "WARN explain_pack reason=database_missing"
	exit 0
fi

set +e
mysql_exec "SELECT 1;" --skip-column-names >/dev/null 2>&1
rc=$?
set -e
if (( rc != 0 )); then
	echo "WARN explain_pack reason=db_connect_failed host=${MYSQL_HOST} port=${MYSQL_PORT} database=${MYSQL_DATABASE}"
	exit 0
fi

info "explain_pack start database=${MYSQL_DATABASE} host=${MYSQL_HOST}:${MYSQL_PORT}"

if tool_table="$(resolve_tool_calls_table)"; then
	sql_search_tool_calls="$(cat <<SQL
SELECT tool_call_id, tool_name, created_at
FROM \`${tool_table}\`
WHERE tool_name LIKE 'mcp.%'
  AND request_json LIKE '%"incident_id":"incident-demo"%'
  AND request_json LIKE '%"request_id":"req-demo"%'
  AND created_at >= DATE_SUB(UTC_TIMESTAMP(), INTERVAL 7 DAY)
  AND created_at <= UTC_TIMESTAMP()
ORDER BY seq ASC
LIMIT 20
SQL
)"
	run_explain \
		"search_tool_calls" \
		"${sql_search_tool_calls}" \
		"考虑维持 tool_name+created_at 过滤，必要时为 request_id/incident_id 增加可索引字段或生成列"
else
	WARN_COUNT=$((WARN_COUNT + 1))
	warn "query=search_tool_calls status=WARN reason=table tool_calls/ai_tool_calls not found"
fi

if table_exists "notice_deliveries"; then
	sql_list_notice_by_time="$(cat <<'SQL'
SELECT delivery_id, status, created_at
FROM `notice_deliveries`
WHERE created_at >= DATE_SUB(UTC_TIMESTAMP(), INTERVAL 7 DAY)
  AND created_at <= UTC_TIMESTAMP()
  AND status = 'succeeded'
ORDER BY created_at DESC, id DESC
LIMIT 20
SQL
)"
	run_explain \
		"list_notice_deliveries_by_time" \
		"${sql_list_notice_by_time}" \
		"确认 idx_notice_deliveries_status_created(status, created_at) 命中"

	sql_get_notice_by_incident="$(cat <<'SQL'
SELECT delivery_id, status, created_at
FROM `notice_deliveries`
WHERE incident_id = 'incident-demo'
ORDER BY created_at DESC, id DESC
LIMIT 20
SQL
)"
	run_explain \
		"get_notice_deliveries_by_incident" \
		"${sql_get_notice_by_incident}" \
		"确认 idx_notice_deliveries_incident_created(incident_id, created_at) 命中"
else
	WARN_COUNT=$((WARN_COUNT + 2))
	warn "query=list_notice_deliveries_by_time status=WARN reason=table notice_deliveries not found"
	warn "query=get_notice_deliveries_by_incident status=WARN reason=table notice_deliveries not found"
fi

if table_exists "alert_events_history"; then
	sql_alert_history="$(cat <<'SQL'
SELECT event_id, fingerprint, last_seen_at
FROM `alert_events_history`
WHERE is_current = 0
  AND last_seen_at >= DATE_SUB(UTC_TIMESTAMP(), INTERVAL 7 DAY)
  AND last_seen_at <= UTC_TIMESTAMP()
ORDER BY last_seen_at DESC, id DESC
LIMIT 20
SQL
)"
	run_explain \
		"alert_events_history" \
		"${sql_alert_history}" \
		"确认 idx_alert_events_current_last_seen(is_current, last_seen_at) 命中"
else
	WARN_COUNT=$((WARN_COUNT + 1))
	warn "query=alert_events_history status=WARN reason=table alert_events_history not found"
fi

if (( WARN_COUNT > 0 )); then
	echo "WARN explain_pack warnings=${WARN_COUNT} pass=${PASS_COUNT}"
else
	echo "PASS explain_pack pass=${PASS_COUNT}"
fi
