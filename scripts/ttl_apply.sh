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

BATCH_SIZE="${BATCH_SIZE:-2000}"
DRY_RUN="${DRY_RUN:-0}"

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

normalize_batch_size() {
	local value="$1"
	local out
	if ! is_uint "${value}" || (( value <= 0 )); then
		warn "BATCH_SIZE is invalid (${value}), fallback=2000"
		out=2000
	else
		out="${value}"
	fi
	if (( out < 1000 )); then
		warn "BATCH_SIZE too small (${out}), clamp to 1000"
		out=1000
	fi
	if (( out > 5000 )); then
		warn "BATCH_SIZE too large (${out}), clamp to 5000"
		out=5000
	fi
	printf '%s' "${out}"
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

build_order_expr() {
	local table="$1"
	local time_col="$2"
	if column_exists "${table}" "id"; then
		printf '%s' "${time_col} ASC, id ASC"
	else
		printf '%s' "${time_col} ASC"
	fi
}

run_delete_batches() {
	local alias="$1"
	local source_table="$2"
	local where_clause="$3"
	local order_expr="$4"
	local total batch batch_no
	total=0
	batch_no=0

	if [[ "${DRY_RUN}" == "1" ]]; then
		local total_would batch_would
		total_would="$(mysql_scalar "SELECT COUNT(*) FROM \`${source_table}\` WHERE ${where_clause};")"
		batch_would="$(mysql_scalar "SELECT COUNT(*) FROM (SELECT 1 FROM \`${source_table}\` WHERE ${where_clause} ORDER BY ${order_expr} LIMIT ${BATCH_SIZE}) t;")"
		echo "target=${alias} source_table=${source_table} dry_run=1 would_delete_total=${total_would:-0} would_delete_first_batch=${batch_would:-0} sql=\"DELETE FROM \\\`${source_table}\\\` WHERE ${where_clause} ORDER BY ${order_expr} LIMIT ${BATCH_SIZE}\""
		return 0
	fi

	while true; do
		batch="$(mysql_scalar "DELETE FROM \`${source_table}\` WHERE ${where_clause} ORDER BY ${order_expr} LIMIT ${BATCH_SIZE}; SELECT ROW_COUNT();")"
		if ! is_uint "${batch:-}"; then
			batch=0
		fi
		if (( batch == 0 )); then
			echo "target=${alias} source_table=${source_table} done total_deleted=${total}"
			break
		fi
		batch_no=$((batch_no + 1))
		total=$((total + batch))
		echo "target=${alias} source_table=${source_table} batch=${batch_no} batch_deleted=${batch} total_deleted=${total}"
		if (( batch < BATCH_SIZE )); then
			echo "target=${alias} source_table=${source_table} done total_deleted=${total}"
			break
		fi
	done
}

parse_dsn_if_present

TTL_TOOL_CALLS_DAYS="$(normalize_days "TTL_TOOL_CALLS_DAYS" "${TTL_TOOL_CALLS_DAYS}" "14")"
TTL_NOTICE_DELIVERIES_DAYS="$(normalize_days "TTL_NOTICE_DELIVERIES_DAYS" "${TTL_NOTICE_DELIVERIES_DAYS}" "30")"
TTL_ALERT_EVENTS_HISTORY_DAYS="$(normalize_days "TTL_ALERT_EVENTS_HISTORY_DAYS" "${TTL_ALERT_EVENTS_HISTORY_DAYS}" "30")"
TTL_EVIDENCE_DAYS="$(normalize_days "TTL_EVIDENCE_DAYS" "${TTL_EVIDENCE_DAYS}" "14")"
TTL_INCIDENT_TIMELINE_DAYS="$(normalize_days "TTL_INCIDENT_TIMELINE_DAYS" "${TTL_INCIDENT_TIMELINE_DAYS}" "90")"
BATCH_SIZE="$(normalize_batch_size "${BATCH_SIZE}")"

if [[ "${DRY_RUN}" != "0" && "${DRY_RUN}" != "1" ]]; then
	warn "DRY_RUN must be 0 or 1, fallback to 0"
	DRY_RUN="0"
fi

if [[ "${DB_ENGINE}" == "sqlite" ]]; then
	info "ttl_apply skipped: sqlite detected (DB_DSN=${DB_DSN})"
	exit 0
fi

need_cmd mysql || fail "mysql client not found"
[[ -n "${MYSQL_DATABASE}" ]] || fail "database name is required (MYSQL_DATABASE/MYSQL_DB/DB_NAME or DB_DSN)"

mysql_exec "SELECT 1;" --skip-column-names >/dev/null

info "ttl_apply start database=${MYSQL_DATABASE} host=${MYSQL_HOST}:${MYSQL_PORT} batch_size=${BATCH_SIZE} dry_run=${DRY_RUN}"

# tool_calls (fallback to ai_tool_calls)
if tool_table="$(resolve_tool_calls_table)"; then
	if time_col="$(pick_first_existing_column "${tool_table}" "created_at" "updated_at")"; then
		order_expr="$(build_order_expr "${tool_table}" "${time_col}")"
		run_delete_batches \
			"tool_calls" \
			"${tool_table}" \
			"${time_col} < DATE_SUB(UTC_TIMESTAMP(), INTERVAL ${TTL_TOOL_CALLS_DAYS} DAY)" \
			"${order_expr}"
	else
		warn "target=tool_calls source_table=${tool_table} skipped: no created_at/updated_at column"
	fi
else
	warn "target=tool_calls skipped: table tool_calls/ai_tool_calls not found"
fi

# notice_deliveries (never delete pending)
if table_exists "notice_deliveries"; then
	time_col="$(pick_first_existing_column "notice_deliveries" "created_at" "next_retry_at")" || time_col=""
	if [[ -n "${time_col}" ]]; then
		order_expr="$(build_order_expr "notice_deliveries" "${time_col}")"
		run_delete_batches \
			"notice_deliveries" \
			"notice_deliveries" \
			"status IN ('succeeded','failed','canceled') AND ${time_col} < DATE_SUB(UTC_TIMESTAMP(), INTERVAL ${TTL_NOTICE_DELIVERIES_DAYS} DAY)" \
			"${order_expr}"
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
		order_expr="$(build_order_expr "alert_events_history" "${time_col}")"
		run_delete_batches \
			"alert_events_history" \
			"alert_events_history" \
			"is_current = 0 AND ${time_col} < DATE_SUB(UTC_TIMESTAMP(), INTERVAL ${TTL_ALERT_EVENTS_HISTORY_DAYS} DAY)" \
			"${order_expr}"
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
		order_expr="$(build_order_expr "evidence" "${time_col}")"
		run_delete_batches \
			"evidence" \
			"evidence" \
			"${time_col} < DATE_SUB(UTC_TIMESTAMP(), INTERVAL ${TTL_EVIDENCE_DAYS} DAY)" \
			"${order_expr}"
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
		order_expr="$(build_order_expr "incident_timeline" "${time_col}")"
		run_delete_batches \
			"incident_timeline" \
			"incident_timeline" \
			"${time_col} < DATE_SUB(UTC_TIMESTAMP(), INTERVAL ${TTL_INCIDENT_TIMELINE_DAYS} DAY)" \
			"${order_expr}"
	else
		warn "target=incident_timeline skipped: no created_at/updated_at column"
	fi
else
	warn "target=incident_timeline skipped: table not found"
fi

info "ttl_apply done"
