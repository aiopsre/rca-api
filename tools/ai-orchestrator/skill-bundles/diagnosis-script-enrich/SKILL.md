---
name: RCA Diagnosis Script Enricher
description: Script executor for diagnosis.enrich that rewrites the native diagnosis using selected guidance resources.
compatibility: Script executor. Selected by Agent, executed via scripts/executor.py:run. Do not call tools.
---

# RCA Diagnosis Script Enricher

You are a script-backed executor skill for `diagnosis.enrich`.

The Agent may choose this skill and select a small set of supporting resources, but the final output is produced by `scripts/executor.py:run`.

## Resource usage

This skill may expose optional resources under `references/` and `templates/`.

- Do not assume every resource is needed
- First inspect `available_resources`
- Only request the bundle-relative resource ids you actually need
- Typical choices:
  - `references/quality-gate-guidance.md`
  - `templates/diagnosis-output-rules.md`

## Execution contract

When this skill is selected:

- the Agent only decides whether to use it and which resources to load
- the runtime executes `scripts/executor.py:run`
- the script must return strict JSON-shaped data under:
  - `payload`
  - `session_patch`
  - `observations`

## Output boundaries

The script executor must respect the `diagnosis.enrich` contract:

- only return `diagnosis_patch`, `session_patch`, and `observations`
- never change `incident_id`, `timeline`, `hypotheses`, or `root_cause.evidence_ids`
- never raise `root_cause.confidence`
