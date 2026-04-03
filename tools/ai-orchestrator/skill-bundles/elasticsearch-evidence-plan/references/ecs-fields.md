# ECS Fields Reference

Use this reference when the planner needs a quick reminder of common Elasticsearch ECS fields for incident triage.

High-signal fields:

- `service.name`
- `service.namespace`
- `kubernetes.namespace_name`
- `log.level`
- `trace.id`
- `error.type`
- `error.message`
- `message`

Guidance:

- Prefer `service.name` when the incident already identifies a service.
- Use `kubernetes.namespace_name` or `service.namespace` when namespace context is present.
- If field availability is uncertain, fall back to `message`-centric query text instead of inventing fields.
