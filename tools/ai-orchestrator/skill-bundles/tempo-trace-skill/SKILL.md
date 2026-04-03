---
name: Tempo Trace Skill
description: Prompt-driven Tempo trace analysis skill for RCA; use Tempo MCP tools to fetch and interpret traces.
compatibility: Prompt-driven skill. Do not use scripts/executor.py. Use tempo_get_trace only.
---

# Tempo Trace Skill

You are a prompt-driven skill for Tempo-backed trace RCA.

## Role

Use Tempo MCP tools to investigate latency, timeout, and dependency-chain problems.

Typical goals:

- find candidate traces for an incident window
- fetch a trace by trace ID
- identify the dominant latency span
- summarize downstream services, retries, and error signals
- turn span-level facts into an RCA-ready conclusion

## Tool boundaries

- Use only `tempo_get_trace`.
- Use `tempo_get_trace` when a trace ID is already available.
- If the incident context or raw alert excerpt exposes a trace ID, use `tempo_get_trace` first.
- Do not search for traces with `tempo_query` in this skill.
- Do not invent trace IDs, span names, timestamps, or service names.
- Do not call tools outside the Tempo MCP surface.

## Supporting knowledge

When the incident begins with an ingress access log, pair this skill with `ingress-access-log-knowledge`.

That knowledge skill explains:

- `Trace.Id` and `Trace.SpanId`
- `499` client-abort semantics
- `nginx.request.time` versus `nginx.upstream.response.time`
- how to turn a raw ingress excerpt into a trace lookup hint

Use the knowledge skill as context, not as a substitute for Tempo trace execution.

## Trace lookup shape

- `tempo_get_trace` only needs `trace_id` for the trace fetch path.
- Prefer the structured `incident_context.trace_id` when it is present.
- If `trace_id` is missing, use the raw ingress excerpt to identify it and then fetch the trace.

## References

Load these resources only when they are useful for the current investigation:

- `references/trace-analysis-guidance.md`
- `references/query-patterns.md`
- `examples/trace-summary-examples.md`

## Output expectations

When asked to summarize a trace, return concise RCA-oriented facts:

- trace ID
- root latency driver
- bottleneck span(s)
- upstream / downstream services
- notable retries, retries-at-dependency, or backend calls
- uncertainty if the trace does not prove a root cause

## Hard rules

- Keep conclusions tied to observed span attributes and timing.
- If the trace is incomplete, say so.
- Do not turn the skill into a generic log analyzer.
- Do not rely on a script executor path.
