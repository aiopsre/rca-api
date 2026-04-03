# Evidence Plan Output Rules

When you return the final executor payload:

- Keep `evidence_plan_patch` conservative and merge-friendly.
- Preserve platform guardrails for datasource, time range, limit, and step size.
- Keep `metrics_branch_meta` and `logs_branch_meta` focused on query text updates.
- If tools were used, summarize what changed in `payload.evidence_plan_patch.metadata.planning_note`.
- Prefer one coherent final plan over echoing knowledge skills separately.
