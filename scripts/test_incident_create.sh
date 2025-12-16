#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"

resp=$(curl -sS -X POST "${BASE_URL}/v1/incidents" \
  -H 'Content-Type: application/json' \
  -d '{"namespace":"default","workloadKind":"Deployment","workloadName":"demo","service":"demo-svc","severity":"P1"}')

echo "$resp" | grep -q '"incidentID"' || (echo "missing incidentID: $resp" && exit 1)
echo "OK: $resp"
