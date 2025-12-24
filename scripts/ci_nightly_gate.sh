#!/usr/bin/env bash
set -euo pipefail

# Nightly gate: heavier E2E-style scripts. Assumes rca-api is running.
# Recommended usage:
#   SCOPES='*' BASE_URL='http://127.0.0.1:5555' ./scripts/ci_nightly_gate.sh

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
SCOPES="${SCOPES:-*}"

if ! curl -fsS "${BASE_URL}/healthz" >/dev/null 2>&1; then
  echo "FAIL NIGHTLY-GATE step=healthz BASE_URL=${BASE_URL} (start rca-api first)" >&2
  exit 2
fi

# Optional knobs
RUN_QUERY="${RUN_QUERY:-0}"          # used by L1/L2 if invoked
N="${N:-200}"                        # storm scale
CONCURRENCY="${CONCURRENCY:-10}"    # storm concurrency

echo "==> make test"
make test

echo "==> make lint-new"
make lint-new

echo "==> scripts/test_p0_L2.sh"
SCOPES="${SCOPES}" BASE_URL="${BASE_URL}" ./scripts/test_p0_L2.sh

echo "==> scripts/test_p0_L4_2_storm.sh (N=${N} CONCURRENCY=${CONCURRENCY})"
SCOPES="${SCOPES}" BASE_URL="${BASE_URL}" N="${N}" CONCURRENCY="${CONCURRENCY}" ./scripts/test_p0_L4_2_storm.sh

echo "==> scripts/test_p1_L3_silence.sh"
SCOPES="${SCOPES}" BASE_URL="${BASE_URL}" ./scripts/test_p1_L3_silence.sh

echo "PASS NIGHTLY-GATE"
