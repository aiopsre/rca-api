#!/usr/bin/env bash
set -euo pipefail

LAST_HTTP_CODE=""
LAST_BODY=""
KEY_IDS="NONE"

truncate_2kb() {
	printf '%s' "${1:-}" | head -c 2048
}

fail_step() {
	local step="$1"
	local code="${2:-${LAST_HTTP_CODE:-UNKNOWN}}"
	local body="${3:-${LAST_BODY:-}}"
	echo "FAIL R1_L3 step=${step}"
	echo "http_code=${code}"
	echo "response_body<<EOF"
	truncate_2kb "${body}"
	echo
	echo "EOF"
	echo "key_ids=${KEY_IDS}"
	exit 1
}

run_go_test_or_fail() {
	local step="$1"
	local pattern="$2"
	local tmp rc
	tmp="$(mktemp)"
	set +e
	go test ./internal/apiserver/pkg/queue -run "${pattern}" -count=1 >"${tmp}" 2>&1
	rc=$?
	set -e
	LAST_BODY="$(cat "${tmp}")"
	rm -f "${tmp}"
	if (( rc != 0 )); then
		LAST_HTTP_CODE="GO_TEST_${rc}"
		fail_step "${step}"
	fi
}

run_go_test_or_fail "A.DefaultUnchanged" '^TestResolveAdaptiveWaiterOptions_DefaultsUnchanged$'
echo "PASS R1_L3 step=A.DefaultUnchanged"

run_go_test_or_fail "B.YAMLOverridesEnv" '^TestResolveAdaptiveWaiterOptions_YAMLOverridesEnv$'
echo "PASS R1_L3 step=B.YAMLOverridesEnv"

run_go_test_or_fail "C.CLIOverridesYAML" '^TestResolveAdaptiveWaiterOptions_CLIOverridesYAML$'
echo "PASS R1_L3 step=C.CLIOverridesYAML"

echo "PASS R1_L3 adaptive longpoll config cli"
