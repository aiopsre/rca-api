# Tempo Direct Trace Lookup

This reference is for the `tempo_get_trace` path used by the Tempo trace skill.

## Lookup goals

- fetch the full span tree when a trace ID is already known
- inspect the dominant latency span
- identify downstream services, retries, and backend calls
- keep the investigation tied to observed span attributes

## Suggested pattern

1. Identify `trace_id` from incident context or from the raw ingress excerpt.
2. Call `tempo_get_trace` with that `trace_id`.
3. Summarize the bottleneck span and dependency chain.
4. Keep uncertainty explicit when the trace is incomplete.

## Trace fetch shape

- Pass only `trace_id` to `tempo_get_trace`.
- Do not try to search first in this mode.
- Do not embed query syntax or time-window logic here.
