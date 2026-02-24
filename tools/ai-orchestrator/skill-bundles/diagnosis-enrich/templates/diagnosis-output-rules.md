# Diagnosis Output Rules

Return concise operator-facing language.

- Rewrite `summary` and `root_cause.statement` rather than copying native wording.
- Prefer short recommendations and next steps.
- Keep `unknowns` concrete and evidence-oriented.
- Always include a `session_patch` that marks `skills.diagnosis_enrich.applied = true`.
