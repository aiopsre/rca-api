#!/usr/bin/env python3
from __future__ import annotations

import json
import sys
import time
from http.server import BaseHTTPRequestHandler, HTTPServer
from typing import Any


def _trim(value: Any) -> str:
    return str(value or "").strip()


def _write(handler: BaseHTTPRequestHandler, status: int, payload: dict[str, object]) -> None:
    raw = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    handler.send_response(status)
    handler.send_header("Content-Type", "application/json")
    handler.send_header("Content-Length", str(len(raw)))
    handler.end_headers()
    handler.wfile.write(raw)


def _extract_payload(body: dict[str, Any]) -> dict[str, Any]:
    messages = body.get("messages")
    if not isinstance(messages, list) or not messages:
        return {}
    content = messages[-1].get("content")
    if isinstance(content, str):
        try:
            parsed = json.loads(content)
        except json.JSONDecodeError:
            return {}
        return parsed if isinstance(parsed, dict) else {}
    return {}


def _first_resource_ids(available_resources: list[dict[str, Any]]) -> list[str]:
    selected: list[str] = []
    for item in available_resources:
        if not isinstance(item, dict):
            continue
        resource_id = _trim(item.get("resource_id"))
        if resource_id:
            selected.append(resource_id)
        if len(selected) >= 1:
            break
    return selected


def _ecs_log_query(service: str, namespace: str) -> str:
    clauses: list[str] = []
    if service:
        clauses.append(f'service.name:"{service}"')
    if namespace:
        clauses.append(f'(kubernetes.namespace_name:"{namespace}" OR service.namespace:"{namespace}")')
    clauses.append('(log.level:(error OR fatal) OR error.type:* OR error.message:* OR message:(*exception* OR *timeout* OR *panic*))')
    return " AND ".join(clauses)


def _promql(service: str, namespace: str) -> str:
    labels: list[str] = []
    if service:
        labels.append(f'service="{service}"')
    if namespace:
        labels.append(f'namespace="{namespace}"')
    selector = "{" + ",".join(labels) + "}" if labels else ""
    return f'sum(rate(http_requests_total{selector}[5m]))'


def _normalize_tools(available_tools: list[Any]) -> list[str]:
    normalized: list[str] = []
    for item in available_tools:
        tool = _trim(item)
        if tool:
            normalized.append(tool)
    return normalized


def _choose_knowledge(candidates: list[dict[str, Any]]) -> dict[str, Any]:
    selected = []
    for item in candidates:
        if not isinstance(item, dict):
            continue
        role = _trim(item.get("role")) or "executor"
        if role == "knowledge":
            binding_key = _trim(item.get("binding_key"))
            if binding_key:
                selected.append(binding_key)
    return {
        "selected_binding_keys": selected,
        "reason": "load all knowledge skills for cross-signal planning",
    }


def _choose_executor(candidates: list[dict[str, Any]]) -> dict[str, Any]:
    for item in candidates:
        if not isinstance(item, dict):
            continue
        role = _trim(item.get("role")) or "executor"
        if role != "executor":
            continue
        binding_key = _trim(item.get("binding_key"))
        if binding_key:
            return {
                "selected_binding_key": binding_key,
                "reason": "choose the only executor candidate",
            }
    first = next((item for item in candidates if isinstance(item, dict)), {})
    return {
        "selected_binding_key": _trim(first.get("binding_key")),
        "reason": "fallback to first candidate",
    }


def _diagnosis_payload(skill_id: str) -> dict[str, Any]:
    return {
        "payload": {
            "diagnosis_patch": {
                "summary": "Prompt-guided diagnosis summary refined with selected skill resources.",
                "root_cause": {
                    "summary": "Evidence points to the same degraded service path.",
                    "statement": "Selected diagnosis skill resources narrowed the root cause to a service-level degradation with corroborating metrics and logs.",
                },
                "recommendations": ["Roll back the recent change and verify error-rate recovery."],
                "unknowns": ["The precise triggering deploy still needs confirmation."],
                "next_steps": ["Confirm the rollout diff during the incident window."],
            }
        },
        "session_patch": {
            "context_state_patch": {
                "skills": {
                    "diagnosis_enrich": {
                        "applied": True,
                        "skill_id": skill_id,
                    }
                }
            },
            "latest_summary": {
                "kind": "diagnosis.enrich",
                "skill_id": skill_id,
            },
        },
        "observations": [
            {"kind": "note", "message": "diagnosis prompt skill applied"},
        ],
    }


def _build_tool_calls(payload: dict[str, Any]) -> dict[str, Any]:
    input_payload = payload.get("input") if isinstance(payload.get("input"), dict) else {}
    incident_context = input_payload.get("incident_context") if isinstance(input_payload.get("incident_context"), dict) else {}
    service = _trim(incident_context.get("service"))
    namespace = _trim(incident_context.get("namespace"))
    available_tools = _normalize_tools(payload.get("available_tools") if isinstance(payload.get("available_tools"), list) else [])
    max_tool_calls = int(payload.get("max_tool_calls") or 1)
    tool_calls: list[dict[str, Any]] = []

    if "mcp.query_metrics" in available_tools and len(tool_calls) < max_tool_calls:
        metrics_meta = input_payload.get("metrics_branch_meta") if isinstance(input_payload.get("metrics_branch_meta"), dict) else {}
        request_payload = metrics_meta.get("request_payload") if isinstance(metrics_meta.get("request_payload"), dict) else {}
        tool_calls.append(
            {
                "tool": "mcp.query_metrics",
                "input": {
                    "datasource_id": _trim(request_payload.get("datasource_id")),
                    "promql": _promql(service, namespace),
                    "start_ts": int(request_payload.get("start_ts") or 0),
                    "end_ts": int(request_payload.get("end_ts") or 0),
                    "step_seconds": int(request_payload.get("step_seconds") or 60),
                },
                "reason": "warm metrics evidence with a scoped 5xx rate query",
            }
        )

    if "mcp.query_logs" in available_tools and len(tool_calls) < max_tool_calls:
        logs_meta = input_payload.get("logs_branch_meta") if isinstance(input_payload.get("logs_branch_meta"), dict) else {}
        request_payload = logs_meta.get("request_payload") if isinstance(logs_meta.get("request_payload"), dict) else {}
        tool_calls.append(
            {
                "tool": "mcp.query_logs",
                "input": {
                    "datasource_id": _trim(request_payload.get("datasource_id")),
                    "query": _ecs_log_query(service, namespace),
                    "start_ts": int(request_payload.get("start_ts") or 0),
                    "end_ts": int(request_payload.get("end_ts") or 0),
                    "limit": int(request_payload.get("limit") or 200),
                },
                "reason": "warm logs evidence with ECS-shaped error filters",
            }
        )

    return {
        "tool_calls": tool_calls,
        "reason": "run bounded planning queries before final evidence plan synthesis",
    }


def _evidence_payload(payload: dict[str, Any]) -> dict[str, Any]:
    skill_id = _trim(payload.get("skill_id")) or "claude.evidence.prompt_planner"
    input_payload = payload.get("input") if isinstance(payload.get("input"), dict) else {}
    tool_results = payload.get("tool_results") if isinstance(payload.get("tool_results"), list) else []
    logs_meta = input_payload.get("logs_branch_meta") if isinstance(input_payload.get("logs_branch_meta"), dict) else {}
    logs_request = logs_meta.get("request_payload") if isinstance(logs_meta.get("request_payload"), dict) else {}
    metrics_meta = input_payload.get("metrics_branch_meta") if isinstance(input_payload.get("metrics_branch_meta"), dict) else {}
    metrics_request = metrics_meta.get("request_payload") if isinstance(metrics_meta.get("request_payload"), dict) else {}

    query_text = _trim(logs_request.get("query"))
    promql = _trim(metrics_request.get("promql"))
    for item in tool_results:
        if not isinstance(item, dict):
            continue
        tool_request = item.get("tool_request") if isinstance(item.get("tool_request"), dict) else {}
        tool_name = _trim(item.get("tool")) or _trim(tool_request.get("tool"))
        input_obj = tool_request.get("input") if isinstance(tool_request.get("input"), dict) else {}
        if tool_name == "mcp.query_logs":
            query_text = _trim(input_obj.get("query")) or query_text
        if tool_name == "mcp.query_metrics":
            promql = _trim(input_obj.get("promql")) or promql

    response_payload: dict[str, Any] = {
        "payload": {
            "evidence_plan_patch": {
                "metadata": {
                    "prompt_skill": skill_id,
                    "tool_result_count": len(tool_results),
                    "query_style": "ecs_query_string",
                }
            },
            "logs_branch_meta": {
                "mode": "query",
                "query_type": "logs",
                "request_payload": {"query": query_text},
                "query_request": {"queryText": query_text},
            },
        },
        "observations": [
            {"kind": "note", "message": "evidence planning prompt skill applied"},
        ],
    }
    if promql:
        response_payload["payload"]["metrics_branch_meta"] = {
            "mode": "query",
            "query_type": "metrics",
            "request_payload": {"promql": promql},
            "query_request": {"promql": promql},
        }
    return response_payload


class Handler(BaseHTTPRequestHandler):
    def log_message(self, format: str, *args: object) -> None:  # noqa: A003
        return

    def do_GET(self) -> None:  # noqa: N802
        if self.path == "/healthz":
            _write(self, 200, {"ok": True})
            return
        _write(self, 404, {"error": "not_found"})

    def do_POST(self) -> None:  # noqa: N802
        if not self.path.endswith("/chat/completions"):
            _write(self, 404, {"error": "not_found"})
            return
        length = int(self.headers.get("Content-Length", "0") or "0")
        body = json.loads(self.rfile.read(length) or b"{}")
        payload = _extract_payload(body)

        response_obj: dict[str, Any]
        candidates = payload.get("candidates")
        if isinstance(candidates, list):
            if isinstance(payload.get("output_contract"), dict) and "selected_binding_keys" in payload["output_contract"]:
                response_obj = _choose_knowledge([item for item in candidates if isinstance(item, dict)])
            else:
                response_obj = _choose_executor([item for item in candidates if isinstance(item, dict)])
        elif isinstance(payload.get("available_resources"), list):
            selected = _first_resource_ids([item for item in payload["available_resources"] if isinstance(item, dict)])
            response_obj = {
                "selected_resource_ids": selected,
                "reason": "load the first clearly relevant resource only",
            }
        elif isinstance(payload.get("available_tools"), list):
            response_obj = _build_tool_calls(payload)
        elif isinstance(payload.get("tool_results"), list):
            if _trim(payload.get("capability")) == "diagnosis.enrich":
                response_obj = _diagnosis_payload(_trim(payload.get("skill_id")))
            else:
                response_obj = _evidence_payload(payload)
        else:
            capability = _trim(payload.get("capability"))
            if capability == "diagnosis.enrich":
                response_obj = _diagnosis_payload(_trim(payload.get("skill_id")))
            else:
                response_obj = _evidence_payload(payload)

        response = {
            "id": f"chatcmpl-mock-{int(time.time())}",
            "object": "chat.completion",
            "created": int(time.time()),
            "model": str(body.get("model") or "mock-skill-agent"),
            "choices": [
                {
                    "index": 0,
                    "message": {
                        "role": "assistant",
                        "content": json.dumps(response_obj, ensure_ascii=False),
                    },
                    "finish_reason": "stop",
                }
            ],
            "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
        }
        _write(self, 200, response)


def main() -> None:
    host = sys.argv[1]
    port = int(sys.argv[2])
    HTTPServer((host, port), Handler).serve_forever()


if __name__ == "__main__":
    main()
