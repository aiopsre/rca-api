# Quality Gate Guidance

Use the quality gate decision to calibrate tone and certainty.

- `success`: tighten the diagnosis, but keep claims anchored to the existing evidence.
- `missing`: emphasize what evidence is still missing before operators should trust the conclusion.
- `conflict`: lower certainty, surface contradictory signals explicitly, and ask for reconciliation steps.

Never increase `root_cause.confidence` or add new `root_cause.evidence_ids`.
