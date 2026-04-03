# Evidence Script Output Rules

Keep the final `evidence.plan` payload conservative.

- Preserve native datasource and time-range guardrails.
- Return at most one metrics branch patch and one logs branch patch.
- Keep metadata compact and operator-friendly.
- Use tool results to tighten the plan, not to invent new evidence.
