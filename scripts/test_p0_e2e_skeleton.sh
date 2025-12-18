#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
SCOPES="${SCOPES:-*}"
ACK_PATH_TEMPLATE="${ACK_PATH_TEMPLATE:-/v1/alert-events/%s/ack}"

echo "BASE_URL=${BASE_URL}"
echo "SCOPES=${SCOPES}"
echo "ACK_PATH_TEMPLATE=${ACK_PATH_TEMPLATE}"

echo "[L4] ingest idempotency + merge baseline"
BASE_URL="${BASE_URL}" SCOPES="${SCOPES}" ACK_PATH_TEMPLATE="${ACK_PATH_TEMPLATE}" ./scripts/test_alert_event_smoke.sh

echo "[L1 skeleton] incident -> evidence -> ai -> finalize"
BASE_URL="${BASE_URL}" SCOPES="${SCOPES}" RUN_QUERY="${RUN_QUERY:-0}" ./scripts/test_evidence_smoke.sh
BASE_URL="${BASE_URL}" SCOPES="${SCOPES}" ./scripts/test_ai_job_smoke.sh

echo "[L2/L3 skeleton] TODO hooks"
echo "1) L2: inject missing_evidence diagnosis and assert low-confidence guard."
echo "2) L3: ingest suppressed alerts and assert no AI run."
echo "P0 skeleton completed."
