#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# shellcheck source=./lib/skills_smoke_env.sh
source "${SCRIPT_DIR}/lib/skills_smoke_env.sh"

SMOKE_CASES="${SMOKE_CASES:-prompt_diagnosis,script_diagnosis,prompt_evidence,script_evidence}"
SUITE_REPORT_INPUT_PATH="${SUITE_REPORT_PATH:-}"
SUITE_REPORT_PATH=""
SUITE_REPORTS=()
SUITE_REPORT_STAGE_DIR=""
SUITE_REPORT_STAGE_PATH=""
SUITE_REPORT_STAGE_REPORTS_DIR=""
SUITE_REPORT_FINAL_REPORTS_DIR=""

cleanup() {
  local status=$?
  trap - EXIT
  skills_smoke_env_stop
  if [[ -n "${SUITE_REPORT_STAGE_PATH}" && -f "${SUITE_REPORT_STAGE_PATH}" && "${SUITE_REPORT_PATH}" != "${SUITE_REPORT_STAGE_PATH}" ]]; then
    mkdir -p "$(dirname "${SUITE_REPORT_PATH}")"
    mkdir -p "${SUITE_REPORT_FINAL_REPORTS_DIR}"
    cp "${SUITE_REPORT_STAGE_PATH}" "${SUITE_REPORT_PATH}"
    if [[ -d "${SUITE_REPORT_STAGE_REPORTS_DIR}" ]]; then
      cp -R "${SUITE_REPORT_STAGE_REPORTS_DIR}/." "${SUITE_REPORT_FINAL_REPORTS_DIR}/"
    fi
  fi
  if [[ -n "${SUITE_REPORT_STAGE_DIR}" && "${SUITE_REPORT_PATH}" != "${SUITE_REPORT_STAGE_PATH}" ]]; then
    rm -rf "${SUITE_REPORT_STAGE_DIR}"
  fi
  exit "${status}"
}
trap cleanup EXIT

resolve_output_path() {
  local target_path="$1"
  local target_dir target_name
  target_dir="$(dirname "${target_path}")"
  target_name="$(basename "${target_path}")"
  mkdir -p "${target_dir}"
  target_dir="$(cd "${target_dir}" && pwd -P)"
  printf '%s/%s\n' "${target_dir}" "${target_name}"
}

prepare_suite_report_export() {
  local report_base_name report_stem
  SUITE_REPORT_STAGE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/skills-smoke-suite.XXXXXX")"
  SUITE_REPORT_STAGE_PATH="${SUITE_REPORT_STAGE_DIR}/skills_smoke_suite_report.json"
  SUITE_REPORT_STAGE_REPORTS_DIR="${SUITE_REPORT_STAGE_DIR}/reports"
  mkdir -p "${SUITE_REPORT_STAGE_REPORTS_DIR}"

  if [[ -n "${SUITE_REPORT_INPUT_PATH}" ]]; then
    SUITE_REPORT_PATH="$(resolve_output_path "${SUITE_REPORT_INPUT_PATH}")"
    report_base_name="$(basename "${SUITE_REPORT_PATH}")"
    report_stem="${report_base_name%.*}"
    if [[ "${report_stem}" == "${report_base_name}" ]]; then
      report_stem="${report_base_name}"
    fi
    SUITE_REPORT_FINAL_REPORTS_DIR="$(dirname "${SUITE_REPORT_PATH}")/${report_stem}_reports"
  else
    SUITE_REPORT_PATH="${SUITE_REPORT_STAGE_PATH}"
    SUITE_REPORT_FINAL_REPORTS_DIR="${SUITE_REPORT_STAGE_REPORTS_DIR}"
  fi
}

stage_case_report() {
  local case_name="$1"
  local report_path="$2"
  local report_name stage_path final_path
  report_name="$(basename "${report_path}")"
  stage_path="${SUITE_REPORT_STAGE_REPORTS_DIR}/${report_name}"
  final_path="${SUITE_REPORT_FINAL_REPORTS_DIR}/${report_name}"
  if [[ ! -f "${report_path}" ]]; then
    echo "missing case report: ${report_path}" >&2
    return 1
  fi
  cp "${report_path}" "${stage_path}"
  SUITE_REPORTS+=("${case_name}:${final_path}")
}

run_case() {
  local case_name="$1"
  local case_workdir="${SKILLS_SMOKE_WORKDIR}/${case_name}"
  local report_path=""
  mkdir -p "${case_workdir}"
  local -a env_cmd
  env_cmd=(
    env
    KEEP_WORKDIR=1
    WORKDIR="${case_workdir}"
    BASE_URL="${BASE_URL}"
    SCOPES="${SCOPES}"
    CONFIG_SCOPES="${CONFIG_SCOPES}"
    AGENT_MODEL="${AGENT_MODEL}"
    AGENT_BASE_URL="${AGENT_BASE_URL}"
    AGENT_API_KEY="${AGENT_API_KEY}"
    DS_BASE_URL="${DS_BASE_URL}"
    SKILL_RELEASE_MODE="${SKILL_RELEASE_MODE}"
    ARTIFACT_BASE_URL="${ARTIFACT_BASE_URL}"
    ARTIFACT_DIR="${ARTIFACT_DIR}"
  )
  case "${case_name}" in
    prompt_diagnosis)
      "${env_cmd[@]}" bash "${SCRIPT_DIR}/test_prompt_first_diagnosis_enrich_smoke.sh"
      report_path="${case_workdir}/prompt_first_diagnosis_enrich_smoke_report.json"
      ;;
    script_diagnosis)
      "${env_cmd[@]}" bash "${SCRIPT_DIR}/test_script_executor_diagnosis_enrich_smoke.sh"
      report_path="${case_workdir}/script_executor_diagnosis_enrich_smoke_report.json"
      ;;
    prompt_evidence)
      "${env_cmd[@]}" bash "${SCRIPT_DIR}/test_prompt_first_evidence_plan_smoke.sh"
      report_path="${case_workdir}/prompt_first_evidence_plan_smoke_report.json"
      ;;
    script_evidence)
      "${env_cmd[@]}" bash "${SCRIPT_DIR}/test_script_executor_evidence_plan_smoke.sh"
      report_path="${case_workdir}/script_executor_evidence_plan_smoke_report.json"
      ;;
    elastic_evidence)
      "${env_cmd[@]}" USE_MOCK_AGENT=0 USE_MOCK_DATASOURCE=0 bash "${SCRIPT_DIR}/test_prompt_first_elasticsearch_evidence_plan_smoke.sh"
      report_path="${case_workdir}/prompt_first_elasticsearch_evidence_plan_smoke_report.json"
      ;;
    *)
      echo "unknown smoke case: ${case_name}" >&2
      return 1
      ;;
  esac
  stage_case_report "${case_name}" "${report_path}"
}

prepare_suite_report_export
skills_smoke_env_start
skills_smoke_env_export

IFS=',' read -r -a CASE_LIST <<<"${SMOKE_CASES}"
for case_name in "${CASE_LIST[@]}"; do
  run_case "${case_name}"
done

{
  printf '{\n'
  printf '  "base_url": "%s",\n' "${BASE_URL}"
  printf '  "workdir": "%s",\n' "${SKILLS_SMOKE_WORKDIR}"
  printf '  "reports": [\n'
  first=1
  for item in "${SUITE_REPORTS[@]}"; do
    case_name="${item%%:*}"
    report_path="${item#*:}"
    if [[ ${first} -eq 0 ]]; then
      printf ',\n'
    fi
    first=0
    printf '    {"case":"%s","report_path":"%s"}' "${case_name}" "${report_path}"
  done
  printf '\n  ]\n'
  printf '}\n'
} >"${SUITE_REPORT_STAGE_PATH}"

echo "PASS skills smoke suite"
echo "base_url=${BASE_URL}"
echo "workdir=${SKILLS_SMOKE_WORKDIR}"
echo "suite_report=${SUITE_REPORT_PATH}"
