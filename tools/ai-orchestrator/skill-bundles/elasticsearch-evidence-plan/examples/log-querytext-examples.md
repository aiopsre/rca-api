# Logs QueryText Examples

Prefer query text that narrows scope before searching for generic failures.

Scoped ECS example:

```text
service.name:"checkout" AND (kubernetes.namespace_name:"prod" OR service.namespace:"prod") AND (log.level:(error OR fatal) OR error.type:* OR error.message:* OR message:(*exception* OR *timeout* OR *panic*))
```

Conservative fallback:

```text
message:(*error* OR *exception* OR *timeout* OR *panic*)
```
