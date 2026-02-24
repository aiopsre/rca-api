#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

export SKILLSET_NAME="${SKILLSET_NAME:-script_executor_evidence_plan}"
export EXECUTOR_SKILL_ID="${EXECUTOR_SKILL_ID:-claude.evidence.script_planner}"
export EXECUTOR_SKILL_VERSION="${EXECUTOR_SKILL_VERSION:-1.0.0}"
export EXECUTOR_MODE="${EXECUTOR_MODE:-script}"
export EXECUTOR_OBSERVATION_TOOL="${EXECUTOR_OBSERVATION_TOOL:-skill.execute}"
export EXECUTOR_BUNDLE_DIR="${EXECUTOR_BUNDLE_DIR:-/opt/workspace/study/rca-api/tools/ai-orchestrator/skill-bundles/evidence-script-plan}"
export SMOKE_LABEL="${SMOKE_LABEL:-script-executor evidence.plan smoke}"
export REPORT_BASENAME="${REPORT_BASENAME:-script_executor_evidence_plan_smoke_report.json}"

exec "${SCRIPT_DIR}/test_prompt_first_evidence_plan_smoke.sh"
