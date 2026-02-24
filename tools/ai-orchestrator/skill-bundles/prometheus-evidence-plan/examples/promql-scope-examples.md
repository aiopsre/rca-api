# PromQL Scope Examples

Service-scoped request rate:

```text
sum(rate(http_requests_total{service="checkout"}[5m]))
```

Namespace-scoped error rate:

```text
sum(rate(http_requests_total{namespace="prod",status=~"5.."}[5m]))
```

Latency percentile pattern:

```text
histogram_quantile(0.99, sum by (le) (rate(http_request_duration_seconds_bucket{service="checkout"}[5m])))
```
