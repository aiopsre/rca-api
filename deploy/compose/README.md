# Redis Ops Compose (O1)

`deploy/compose/docker-compose.redis.yaml` 固化了 O1 最小运行拓扑：
- `mysql`
- `redis`
- `rca-apiserver`
- `notice-worker`
- `ai-orchestrator`
- `mock-webhook`（可选，`--profile mock`）

## 一键启动

```bash
docker compose -f deploy/compose/docker-compose.redis.yaml up -d --build
```

查看状态：

```bash
docker compose -f deploy/compose/docker-compose.redis.yaml ps
```

停止并清理：

```bash
docker compose -f deploy/compose/docker-compose.redis.yaml down -v
```

## 常用环境变量

- `MYSQL_ROOT_PASSWORD`（默认 `Az123456_`）
- `MYSQL_DATABASE`（默认 `rca`）
- `REDIS_PASSWORD`（默认 `Az123456_`）
- `REDIS_ENABLED`（默认 `true`）
- `REDIS_FAIL_OPEN`（默认 `true`）
- `REDIS_PUBSUB_ENABLED`（默认 `true`）
- `REDIS_TOPIC_AI_JOB_SIGNAL`（默认 `rca:ai_job_queue_signal`）
- `REDIS_LIMITER_ENABLED`（默认 `true`）
- `REDIS_LIMITER_MODE`（默认 `both`）
- `REDIS_LIMITER_GLOBAL_QPS`（默认 `20`）
- `REDIS_LIMITER_CHANNEL_QPS`（默认 `0`）
- `REDIS_LIMITER_BURST`（默认 `20`）
- `REDIS_STREAMS_ENABLED`（默认 `true`）
- `REDIS_NOTICE_DELIVERY_STREAM`（默认 `rca:notice:delivery_stream`）
- `REDIS_STREAMS_CONSUMER_GROUP`（默认 `notice_delivery_workers`）
- `REDIS_STREAMS_RECLAIM_IDLE_SECONDS`（默认 `60`）
- `REDIS_ALERTING_ENABLED`（默认 `true`）

## 最小验证步骤

1. 健康检查：

```bash
curl -fsS http://127.0.0.1:5555/healthz
```

2. 指标检查：

```bash
curl -fsS http://127.0.0.1:5555/metrics | rg "redis_pubsub_subscribe_ready|ai_job_longpoll_fallback_total|redis_stream_consume_total"
```

3. 运行 O1 回归脚本：

```bash
scripts/test_o1_L1_redis_ops_profile_and_metrics.sh
```

## 常见问题

- `rca-apiserver` 启动失败且提示 DB 连不上：
  - 检查 `docker compose ... ps` 中 `mysql` 是否 `healthy`。
- `redis_pubsub_subscribe_ready` 一直为 `0`：
  - 检查 `redis` 服务健康状态、`REDIS_PASSWORD` 与 `redis.password` 是否一致。
- `ai-orchestrator` 未拉到 job：
  - 确认 `SCOPES` / `RCA_API_SCOPES` 非空（compose 默认 `*`）。
