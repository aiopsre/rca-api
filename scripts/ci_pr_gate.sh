#!/usr/bin/env bash
set -euo pipefail

# PR gate: fast, deterministic, no external dependencies by default.
#
# Optional E2E:
#   RUN_E2E=1 SCOPES='*' BASE_URL='http://127.0.0.1:5555' RUN_QUERY=0 ./scripts/ci_pr_gate.sh

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

RUN_E2E="${RUN_E2E:-0}"

echo "==> make test"
make test

echo "==> make lint-new"
make lint-new

if [[ "${RUN_E2E}" == "1" ]]; then
  BASE_URL="${BASE_URL:-http://127.0.0.1:5555}"
  RUN_QUERY="${RUN_QUERY:-0}"

  # Best-effort preflight so failures are actionable.
  if ! curl -fsS "${BASE_URL}/healthz" >/dev/null 2>&1; then
    echo "FAIL PR-GATE step=healthz BASE_URL=${BASE_URL} (start rca-api first)" >&2
    exit 2
  fi

  echo "==> scripts/test_p0_L1.sh (RUN_QUERY=${RUN_QUERY})"
  SCOPES="${SCOPES:-*}" RUN_QUERY="${RUN_QUERY}" BASE_URL="${BASE_URL}" ./scripts/test_p0_L1.sh
fi

echo "PASS PR-GATE"
