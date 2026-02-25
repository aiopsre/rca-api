#!/usr/bin/env bash

skills_release_sha256() {
  local python_bin="$1"
  local file_path="$2"
  "${python_bin}" - "${file_path}" <<'PY'
import hashlib
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
print(hashlib.sha256(path.read_bytes()).hexdigest())
PY
}

skills_release_summary_json() {
  local python_bin="$1"
  local bundle_dir="$2"
  "${python_bin}" - "${bundle_dir}" <<'PY'
import json
import pathlib
import sys

bundle_dir = pathlib.Path(sys.argv[1])
skill_md = bundle_dir / "SKILL.md"
if not skill_md.is_file():
    raise SystemExit(f"bundle missing SKILL.md: {bundle_dir}")
raw = skill_md.read_text(encoding="utf-8")
normalized = raw.replace("\r\n", "\n")
if not normalized.startswith("---\n"):
    raise SystemExit("SKILL.md missing frontmatter")
rest = normalized[len("---\n") :]
end = rest.find("\n---\n")
if end < 0:
    raise SystemExit("SKILL.md missing closing frontmatter delimiter")
fields: dict[str, str] = {}
for line in rest[:end].split("\n"):
    stripped = line.strip()
    if not stripped:
        continue
    if line.startswith((" ", "\t")):
        raise SystemExit("SKILL.md frontmatter only supports flat scalar fields")
    if ":" not in line:
        raise SystemExit(f"invalid frontmatter line: {line}")
    key, value = line.split(":", 1)
    normalized_key = key.strip()
    normalized_value = value.strip()
    if len(normalized_value) >= 2 and (
        (normalized_value.startswith('"') and normalized_value.endswith('"'))
        or (normalized_value.startswith("'") and normalized_value.endswith("'"))
    ):
        normalized_value = normalized_value[1:-1]
    fields[normalized_key] = normalized_value

payload = {
    "bundle_format": "claude_skill_v1",
    "instruction_file": "SKILL.md",
    "name": fields.get("name", "").strip(),
    "description": fields.get("description", "").strip(),
}
compatibility = fields.get("compatibility", "").strip()
if compatibility:
    payload["compatibility"] = compatibility
if not payload["name"] or not payload["description"]:
    raise SystemExit("SKILL.md frontmatter requires name and description")
print(json.dumps(payload, ensure_ascii=False, separators=(",", ":")))
PY
}

skills_release_publish() {
  local mode="$1"
  local base_url="$2"
  local token="$3"
  local config_scopes="$4"
  local curl_bin="$5"
  local jq_bin="$6"
  local python_bin="$7"
  local bundle_dir="$8"
  local bundle_path="$9"
  local skill_id="${10}"
  local version="${11}"
  local artifact_base_url="${12:-}"
  local artifact_dir="${13:-}"
  local status="${14:-active}"

  case "${mode}" in
    upload)
      "${curl_bin}" -sS -X POST "${base_url}/v1/config/skill-release/upload" \
        -H "Authorization: Bearer ${token}" \
        -H "X-Scopes: ${config_scopes}" \
        -H 'Accept: application/json' \
        -F "bundle=@${bundle_path};type=application/zip" \
        -F "skill_id=${skill_id}" \
        -F "version=${version}" \
        -F "status=${status}"
      ;;
    register)
      if [[ -z "${artifact_base_url}" ]]; then
        echo "skills_release_publish(register): ARTIFACT_BASE_URL is required" >&2
        return 1
      fi
      local digest summary_json artifact_name artifact_url register_body target_path
      digest="$(skills_release_sha256 "${python_bin}" "${bundle_path}")"
      summary_json="$(skills_release_summary_json "${python_bin}" "${bundle_dir}")"
      artifact_name="$(basename "${bundle_path}")"
      if [[ -n "${artifact_dir}" ]]; then
        mkdir -p "${artifact_dir}"
        target_path="${artifact_dir}/${artifact_name}"
        cp "${bundle_path}" "${target_path}"
      fi
      artifact_url="${artifact_base_url%/}/${artifact_name}"
      register_body="$("${jq_bin}" -nc \
        --arg skill_id "${skill_id}" \
        --arg version "${version}" \
        --arg bundle_digest "${digest}" \
        --arg artifact_url "${artifact_url}" \
        --arg manifest_json "${summary_json}" \
        --arg status "${status}" \
        '{
          skill_id: $skill_id,
          version: $version,
          bundle_digest: $bundle_digest,
          artifact_url: $artifact_url,
          manifest_json: $manifest_json,
          status: $status
        }')"
      "${curl_bin}" -sS -X POST "${base_url}/v1/config/skill-release/register" \
        -H "Authorization: Bearer ${token}" \
        -H "X-Scopes: ${config_scopes}" \
        -H 'Accept: application/json' \
        -H 'Content-Type: application/json' \
        -d "${register_body}"
      ;;
    *)
      echo "skills_release_publish: unsupported mode=${mode}" >&2
      return 1
      ;;
  esac
}
