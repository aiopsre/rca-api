# Tempo Trace Summary Examples

## Example 1: Slow ingress request

Trace shows APISIX taking roughly the same time as the downstream application span. The application performs Redis authentication checks, a Dubbo lookup, and then a MySQL query. The bottleneck is below the ingress layer, most likely in the downstream lookup chain.

## Example 2: Downstream dependency dominates

Trace shows the application span is short until a child span calls an external dependency. That child span accounts for most of the wall-clock time, so the latency driver is the dependency call, not the caller.

## Example 3: Incomplete evidence

Trace contains only a partial span tree. Summarize the observed spans, identify what is missing, and state that the trace does not prove the root cause.
