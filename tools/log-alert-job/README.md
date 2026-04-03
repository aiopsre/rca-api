# log-alert-job

T8 log alert worker for Elasticsearch ingress/microsvc rules.

## Run

```bash
CONFIG_PATH=configs/log_alert_rules.yaml \
ES_URLS="http://es1:9200,http://es2:9200" \
ES_USER="" ES_PASS="" \
RCA_BASE_URL="http://127.0.0.1:5655" \
go run ./tools/log-alert-job --config configs/log_alert_rules.yaml
```

## Overrides

- `CONFIG_PATH`: config file path (default `configs/log_alert_rules.yaml`)
- `ES_URLS`: comma separated ES URL list, overrides `es.urls`
- `ES_USER` / `ES_PASS`: overrides `es.username` / `es.password`
- `RCA_BASE_URL`: overrides `rca.base_url`

## Endpoints

- `/healthz`
- `/metrics`
