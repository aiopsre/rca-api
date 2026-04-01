# Tempo Trace Analysis Guidance

Use this guidance when the investigation already has a candidate trace or a narrow time window.

## What to look for

- The top ingress span and its duration
- The first downstream service that consumes the majority of time
- Child spans that show retries, queueing, or blocking I/O
- Whether the request ultimately succeeded or failed
- Whether the trace proves a root cause or only narrows the search space

## Interpretation rules

- If ingress and application spans have similar duration, the bottleneck is usually below the edge gateway.
- If a child span dominates the tree, summarize that span first.
- If there are repeated backend calls, describe the retry pattern instead of counting only the number of spans.
- If the trace includes MySQL, Redis, or RPC spans, mention them only when they materially contribute to the latency.

## Output shape

The skill should produce RCA-ready language:

- trace id
- dominant latency span
- downstream dependencies
- observed retries or backend waits
- short conclusion
- remaining uncertainty
