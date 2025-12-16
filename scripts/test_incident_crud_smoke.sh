#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"

create_resp=$(curl -sS -X POST "${BASE_URL}/v1/incidents" \
  -H 'Content-Type: application/json' \
  -d '{"namespace":"default","workloadKind":"Deployment","workloadName":"demo","service":"demo-svc","severity":"P1"}')

incident_id=$(echo "$create_resp" | sed -n 's/.*"incidentID":"\([^"]*\)".*/\1/p')

if [[ -z "${incident_id}" ]]; then
  echo "Create failed: ${create_resp}"
  exit 1
fi

get_resp=$(curl -sS "${BASE_URL}/v1/incidents/${incident_id}")

echo "$get_resp" | grep -q "\"incidentID\":\"${incident_id}\"" || (echo "Get mismatch: ${get_resp}" && exit 1)

echo "OK: ${incident_id}"
