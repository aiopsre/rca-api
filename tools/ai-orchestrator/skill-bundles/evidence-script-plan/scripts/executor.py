from __future__ import annotations

from typing import Any


def _resource_ids(ctx: dict[str, Any]) -> list[str]:
    raw = ctx.get("skill_resources")
    if not isinstance(raw, list):
        return []
    out: list[str] = []
    for item in raw:
        if not isinstance(item, dict):
            continue
        resource_id = str(item.get("resource_id") or "").strip()
        if resource_id:
            out.append(resource_id)
    return out


def _knowledge_skill_ids(ctx: dict[str, Any]) -> list[str]:
    raw = ctx.get("knowledge_context")
    if not isinstance(raw, list):
        return []
    out: list[str] = []
    for item in raw:
        if not isinstance(item, dict):
            continue
        skill_id = str(item.get("skill_id") or "").strip()
        if skill_id:
            out.append(skill_id)
    return out


def _metrics_tool_request(input_payload: dict[str, Any]) -> dict[str, Any] | None:
    metrics_branch_meta = input_payload.get("metrics_branch_meta")
    if not isinstance(metrics_branch_meta, dict):
        return None
    if str(metrics_branch_meta.get("mode") or "").strip() != "query":
        return None
    request_payload = metrics_branch_meta.get("request_payload")
    if not isinstance(request_payload, dict):
        return None
    datasource_id = str(request_payload.get("datasource_id") or "").strip()
    start_ts = request_payload.get("start_ts")
    end_ts = request_payload.get("end_ts")
    if not datasource_id or not isinstance(start_ts, int) or not isinstance(end_ts, int):
        return None
    service = "svc"
    incident_context = input_payload.get("incident_context")
    if isinstance(incident_context, dict):
        service = str(incident_context.get("service") or service).strip() or service
    return {
        "tool": "mcp.query_metrics",
        "input": {
            "datasource_id": datasource_id,
            "promql": f'sum(rate(http_requests_total{{service="{service}",status=~"5.."}}[5m]))',
            "start_ts": start_ts,
            "end_ts": end_ts,
            "step_seconds": 60,
        },
        "reason": "Warm error-rate metrics before finalizing the evidence plan.",
    }


def _logs_tool_request(input_payload: dict[str, Any]) -> dict[str, Any] | None:
    logs_branch_meta = input_payload.get("logs_branch_meta")
    if not isinstance(logs_branch_meta, dict):
        return None
    if str(logs_branch_meta.get("mode") or "").strip() != "query":
        return None
    request_payload = logs_branch_meta.get("request_payload")
    if not isinstance(request_payload, dict):
        return None
    datasource_id = str(request_payload.get("datasource_id") or "").strip()
    start_ts = request_payload.get("start_ts")
    end_ts = request_payload.get("end_ts")
    limit = request_payload.get("limit")
    if not datasource_id or not isinstance(start_ts, int) or not isinstance(end_ts, int) or not isinstance(limit, int):
        return None
    incident_context = input_payload.get("incident_context")
    service = "svc"
    namespace = "default"
    if isinstance(incident_context, dict):
        service = str(incident_context.get("service") or service).strip() or service
        namespace = str(incident_context.get("namespace") or namespace).strip() or namespace
    return {
        "tool": "mcp.query_logs",
        "input": {
            "datasource_id": datasource_id,
            "query": (
                f'service.name:"{service}" AND '
                f'(kubernetes.namespace_name:"{namespace}" OR service.namespace:"{namespace}") AND '
                "(log.level:(error OR fatal) OR error.type:* OR error.message:* "
                "OR message:(*exception* OR *timeout* OR *panic*))"
            ),
            "start_ts": start_ts,
            "end_ts": end_ts,
            "limit": limit,
        },
        "reason": "Warm scoped error logs before finalizing the evidence plan.",
    }


def run(input_payload: dict[str, Any], ctx: dict[str, Any]) -> dict[str, Any]:
    phase = str(ctx.get("phase") or "final").strip().lower()
    knowledge_skill_ids = _knowledge_skill_ids(ctx)
    resource_ids = _resource_ids(ctx)
    tool_calling_mode = str(ctx.get("tool_calling_mode") or "disabled").strip().lower()

    if phase == "plan_tools":
        tool_calls: list[dict[str, Any]] = []
        if tool_calling_mode in {"evidence_plan_single_hop", "evidence_plan_dual_tool"}:
            metrics_tool = _metrics_tool_request(input_payload)
            logs_tool = _logs_tool_request(input_payload)
            if tool_calling_mode == "evidence_plan_single_hop":
                if logs_tool is not None:
                    tool_calls.append(logs_tool)
            else:
                if metrics_tool is not None:
                    tool_calls.append(metrics_tool)
                if logs_tool is not None:
                    tool_calls.append(logs_tool)
        if tool_calls:
            return {
                "tool_calls": tool_calls,
                "observations": [
                    {
                        "kind": "note",
                        "message": f"script planner requested {len(tool_calls)} tool call(s)",
                    }
                ],
            }

    tool_results = ctx.get("tool_results")
    if not isinstance(tool_results, list):
        tool_results = []
    metrics_result_count = 0
    logs_result_count = 0
    for item in tool_results:
        if not isinstance(item, dict):
            continue
        tool_name = str(item.get("tool") or "").strip()
        if tool_name == "mcp.query_metrics":
            metrics_result_count += 1
        elif tool_name == "mcp.query_logs":
            logs_result_count += 1

    payload: dict[str, Any] = {
        "evidence_plan_patch": {
            "metadata": {
                "prompt_skill": "claude.evidence.script_planner",
                "planning_note": "Script executor incorporated bounded metrics/logs warmup results.",
                "knowledge_skills": knowledge_skill_ids,
                "executor_mode": "script",
                "resource_ids": resource_ids,
                "metrics_result_count": metrics_result_count,
                "logs_result_count": logs_result_count,
            }
        },
        "metrics_branch_meta": {
            "mode": "query",
            "query_type": "metrics",
            "request_payload": {
                "promql": _metrics_tool_request(input_payload)["input"]["promql"] if _metrics_tool_request(input_payload) else "sum(rate(http_requests_total[5m]))",
                "step_seconds": 60,
            },
            "query_request": {
                "queryText": _metrics_tool_request(input_payload)["input"]["promql"] if _metrics_tool_request(input_payload) else "sum(rate(http_requests_total[5m]))",
            },
        },
        "logs_branch_meta": {
            "mode": "query",
            "query_type": "logs",
            "request_payload": {
                "query": _logs_tool_request(input_payload)["input"]["query"] if _logs_tool_request(input_payload) else "message:(*error* OR *exception*)",
            },
            "query_request": {
                "queryText": _logs_tool_request(input_payload)["input"]["query"] if _logs_tool_request(input_payload) else "message:(*error* OR *exception*)",
            },
        },
    }
    return {
        "payload": payload,
        "observations": [
            {
                "kind": "note",
                "message": f"script evidence planner completed in phase={phase}",
            }
        ],
    }
