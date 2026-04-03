#!/usr/bin/env python3
"""Generate a minimal complete OpenAPI document and frontend API mapping from handler route registrations.

This script scans Go handler registration blocks and emits:
- OpenAPI 3.0 JSON (`rca-complete.swagger.json`)
- CSV mapping (`docs/swagger/rca-complete.csv`)
- Markdown mapping (`docs/swagger/rca-complete.md`)
"""

from __future__ import annotations

import argparse
import csv
import json
import re
from dataclasses import dataclass
from pathlib import Path
from typing import Dict, List, Tuple

GROUP_RE = re.compile(r'\b(?P<var>[A-Za-z_][A-Za-z0-9_]*)\s*:=\s*v1\.Group\("(?P<base>[^"]+)"')
ROUTE_RE = re.compile(
    r'\b(?P<var>[A-Za-z_][A-Za-z0-9_]*)\.(?P<method>GET|POST|PUT|DELETE)\("(?P<path>[^"]*)"(?:,[^)]*)?\)'
)
V1_ROUTE_RE = re.compile(r'\bv1\.(?P<method>GET|POST|PUT|DELETE)\("(?P<path>[^"]*)"(?:,[^)]*)?\)')
HANDLER_NAME_RE = re.compile(r'handler\.([A-Za-z_][A-Za-z0-9_]*)')


@dataclass
class Route:
    path: str
    method: str
    handler: str
    file: str
    scope: str
    tag: str


def normalize_join(base: str, sub: str) -> str:
    if not base:
        return sub
    if sub == "":
        return base
    if base.endswith("/"):
        base = base[:-1]
    if not sub.startswith("/"):
        sub = "/" + sub
    out = base + sub
    out = out.replace("//", "/")
    return out


def with_v1_prefix(path: str) -> str:
    if not path.startswith("/"):
        path = "/" + path
    if path.startswith("/v1/") or path == "/v1":
        out = path
    else:
        out = "/v1" + path
    if out != "/" and out.endswith("/"):
        out = out.rstrip("/")
    return out


def path_tag(path: str) -> str:
    if path.startswith("/v1/operator"):
        return "Operator"
    if path.startswith("/v1/sessions"):
        return "Session"
    if path.startswith("/v1/session"):
        return "SessionConfig"
    if path.startswith("/v1/ai/jobs"):
        return "AIJob"
    if path.startswith("/v1/incidents"):
        return "Incident"
    if path.startswith("/v1/config"):
        return "Config"
    if path.startswith("/v1/users"):
        return "RBAC"
    if path.startswith("/v1/roles"):
        return "RBAC"
    if path.startswith("/v1/permissions"):
        return "RBAC"
    if path.startswith("/v1/auth"):
        return "Auth"
    if path.startswith("/v1/notice"):
        return "Notice"
    if path.startswith("/v1/silences"):
        return "Silence"
    if path.startswith("/v1/datasources"):
        return "Datasource"
    if path.startswith("/v1/orchestrator"):
        return "Orchestrator"
    if path.startswith("/v1/mcp"):
        return "MCP"
    if path.startswith("/v1/alert") or path.startswith("/v1/alerts"):
        return "Alert"
    return "RCA"


def infer_scope(path: str, method: str) -> str:
    if path.startswith("/v1/config/"):
        return "config.admin"
    if path.startswith("/v1/users") or path.startswith("/v1/roles") or path.startswith("/v1/permissions"):
        return "rbac.admin"
    if path.startswith("/v1/operator/"):
        if path.endswith("/sla/escalation-sync"):
            return "ai.run"
        return "ai.read"
    if "/actions/replay" in path or "/actions/follow-up" in path:
        return "ai.run"
    if "/actions/review-" in path:
        return "session.review"
    if "/actions/assign" in path or "/actions/reassign" in path or path.endswith("/assign"):
        return "session.assignment"
    if path.startswith("/v1/sessions/"):
        return "ai.read"
    if path.startswith("/v1/session/"):
        if method == "GET":
            return "ai.read|session.assignment"
        return "session.assignment"
    if path.startswith("/v1/auth/"):
        return "public"
    if path.startswith("/v1/incidents") and method == "POST" and "/ai:run" in path:
        return "ai.run"
    if method == "GET":
        return "ai.read"
    return "ai.run"


def extract_handler_name(line: str) -> str:
    matches = HANDLER_NAME_RE.findall(line)
    if not matches:
        return ""
    return matches[-1]


def iter_go_files(search_roots: List[Path]) -> List[Path]:
    files: Dict[str, Path] = {}
    for root in search_roots:
        if not root.exists():
            continue
        if root.is_file() and root.suffix == ".go":
            files[str(root.resolve())] = root
            continue
        for go_file in root.rglob("*.go"):
            if go_file.name.endswith("_test.go"):
                continue
            files[str(go_file.resolve())] = go_file
    return sorted(files.values(), key=lambda p: str(p))


def scan_routes(search_roots: List[Path]) -> List[Route]:
    routes: List[Route] = []
    for go_file in iter_go_files(search_roots):
        lines = go_file.read_text(encoding="utf-8").splitlines()
        groups: Dict[str, str] = {}
        for line in lines:
            m_group = GROUP_RE.search(line)
            if m_group:
                groups[m_group.group("var")] = m_group.group("base")
                continue

            m_route = ROUTE_RE.search(line)
            if m_route:
                var = m_route.group("var")
                method = m_route.group("method")
                sub_path = m_route.group("path")
                base = groups.get(var, "")
                path = with_v1_prefix(normalize_join(base, sub_path))
                handler = extract_handler_name(line)
                scope = infer_scope(path, method)
                routes.append(Route(path=path, method=method, handler=handler, file=str(go_file), scope=scope, tag=path_tag(path)))
                continue

            m_v1 = V1_ROUTE_RE.search(line)
            if m_v1:
                method = m_v1.group("method")
                path = with_v1_prefix(m_v1.group("path"))
                handler = extract_handler_name(line)
                scope = infer_scope(path, method)
                routes.append(Route(path=path, method=method, handler=handler, file=str(go_file), scope=scope, tag=path_tag(path)))
    dedup: Dict[Tuple[str, str], Route] = {}
    for r in routes:
        dedup[(r.path, r.method)] = r
    return sorted(dedup.values(), key=lambda x: (x.path, x.method))


def path_params(path: str) -> List[str]:
    return [seg[1:] for seg in path.split("/") if seg.startswith(":")]


def to_openapi_path(path: str) -> str:
    out = []
    for seg in path.split("/"):
        if seg.startswith(":"):
            out.append("{" + seg[1:] + "}")
        else:
            out.append(seg)
    return "/".join(out)


def query_parameters(route: Route) -> List[dict]:
    params: List[dict] = []
    if route.method != "GET":
        return params

    def add(name: str, description: str, schema: dict) -> None:
        params.append(
            {
                "name": name,
                "in": "query",
                "required": False,
                "description": description,
                "schema": schema,
            }
        )

    if route.path == "/v1/operator/inbox":
        add("assignee", "Filter by assignee operator ID.", {"type": "string"})
        add("review_state", "Filter by review state.", {"type": "string"})
        add("needs_review", "Filter by needs_review flag.", {"type": "boolean"})
        add("escalation_state", "Filter by escalation state.", {"type": "string"})
        add("session_type", "Filter by session_type.", {"type": "string"})
        add("trigger_type", "Filter by latest trigger_type.", {"type": "string"})
        add("offset", "Pagination offset.", {"type": "integer", "minimum": 0, "default": 0})
        add("limit", "Pagination limit.", {"type": "integer", "minimum": 1, "maximum": 200, "default": 20})
        add("order", "Sort order by activity time.", {"type": "string", "enum": ["desc", "asc"], "default": "desc"})
        return params

    if route.path == "/v1/operator/dashboard":
        add("team_id", "Team scope for dashboard aggregation.", {"type": "string"})
        return params

    if route.path == "/v1/operator/team_dashboard":
        add("team_id", "Team ID for owner/team aggregation.", {"type": "string"})
        add("offset", "Pagination offset.", {"type": "integer", "minimum": 0, "default": 0})
        add("limit", "Pagination limit.", {"type": "integer", "minimum": 1, "maximum": 200, "default": 20})
        add("order", "Sort order by risk/activity.", {"type": "string", "enum": ["desc", "asc"], "default": "desc"})
        return params

    if route.path == "/v1/operator/dashboard/trends":
        add("window", "Trend window.", {"type": "string", "enum": ["7d", "30d"], "default": "7d"})
        add("operator_id", "Operator scope for aggregation.", {"type": "string"})
        add("team_id", "Team scope for aggregation.", {"type": "string"})
        add("session_type", "Optional session_type filter.", {"type": "string"})
        return params

    if route.path == "/v1/ai/jobs:trace-compare":
        add("left_job_id", "Left job ID.", {"type": "string"})
        add("right_job_id", "Right job ID.", {"type": "string"})
        add("session_id", "Optional session ID for compare context.", {"type": "string"})
        return params

    if route.path.endswith("/history") or route.path.endswith("/assignment_history"):
        add("offset", "Pagination offset.", {"type": "integer", "minimum": 0, "default": 0})
        add("limit", "Pagination limit.", {"type": "integer", "minimum": 1, "maximum": 200, "default": 20})
        add("order", "Sort by created time.", {"type": "string", "enum": ["desc", "asc"], "default": "desc"})
        return params

    if route.path.endswith("/workbench/viewer"):
        add("tab", "Viewer tab selector.", {"type": "string"})
        add("offset", "Pagination offset for long lists in viewer.", {"type": "integer", "minimum": 0, "default": 0})
        add("limit", "Pagination limit for long lists in viewer.", {"type": "integer", "minimum": 1, "maximum": 200, "default": 20})
        return params

    if route.path.endswith("/ai/traces"):
        add("offset", "Pagination offset.", {"type": "integer", "minimum": 0, "default": 0})
        add("limit", "Pagination limit.", {"type": "integer", "minimum": 1, "maximum": 200, "default": 20})
        return params

    return params


def request_body_schema(route: Route) -> dict | None:
    if route.method not in {"POST", "PUT"}:
        return None
    if route.path.endswith("/actions/replay") or route.path.endswith("/actions/follow-up"):
        return {
            "type": "object",
            "properties": {
                "reason": {"type": "string"},
                "operator_note": {"type": "string"},
                "source": {"type": "string"},
            },
        }
    if "/actions/review-" in route.path:
        return {
            "type": "object",
            "properties": {
                "note": {"type": "string"},
                "reason_code": {"type": "string"},
            },
        }
    if route.path.endswith("/actions/assign") or route.path.endswith("/actions/reassign") or route.path.endswith("/assign"):
        return {
            "type": "object",
            "required": ["assignee"],
            "properties": {
                "assignee": {"type": "string"},
                "assigned_by": {"type": "string"},
                "note": {"type": "string"},
            },
        }
    if route.path.startswith("/v1/auth/login"):
        return {
            "type": "object",
            "properties": {
                "operator_id": {"type": "string"},
                "username": {"type": "string"},
                "password": {"type": "string"},
                "team_ids": {"type": "array", "items": {"type": "string"}},
                "scopes": {"type": "array", "items": {"type": "string"}},
                "ttl_seconds": {"type": "integer"},
            },
        }
    if route.path.startswith("/v1/users"):
        if route.path.endswith("/roles"):
            return {"type": "object", "properties": {"role_ids": {"type": "array", "items": {"type": "string"}}}}
        return {
            "type": "object",
            "properties": {
                "user_id": {"type": "string"},
                "username": {"type": "string"},
                "password": {"type": "string"},
                "team_id": {"type": "string"},
                "status": {"type": "string"},
            },
        }
    if route.path.startswith("/v1/roles"):
        if route.path.endswith("/permissions"):
            return {"type": "object", "properties": {"permission_ids": {"type": "array", "items": {"type": "string"}}}}
        return {
            "type": "object",
            "properties": {
                "role_id": {"type": "string"},
                "display_name": {"type": "string"},
                "description": {"type": "string"},
                "status": {"type": "string"},
            },
        }
    if route.path.startswith("/v1/permissions"):
        return {
            "type": "object",
            "properties": {
                "permission_id": {"type": "string"},
                "resource": {"type": "string"},
                "action": {"type": "string"},
                "description": {"type": "string"},
                "status": {"type": "string"},
            },
        }
    if route.path.startswith("/v1/config/"):
        if "/pipeline/" in route.path:
            return {
                "type": "object",
                "properties": {
                    "alert_source": {"type": "string"},
                    "service": {"type": "string"},
                    "namespace": {"type": "string"},
                    "pipeline_id": {"type": "string"},
                    "graph_id": {"type": "string"},
                },
            }
        if "/trigger/" in route.path:
            return {
                "type": "object",
                "properties": {
                    "trigger_type": {"type": "string"},
                    "pipeline_id": {"type": "string"},
                    "session_type": {"type": "string"},
                    "fallback": {"type": "boolean"},
                },
            }
        if "/toolset/" in route.path:
            return {
                "type": "object",
                "properties": {
                    "pipeline_id": {"type": "string"},
                    "toolset_name": {"type": "string"},
                    "allowed_tools": {"type": "array", "items": {"type": "string"}},
                },
            }
        if "/sla/" in route.path:
            return {
                "type": "object",
                "properties": {
                    "session_type": {"type": "string"},
                    "due_seconds": {"type": "integer"},
                    "escalation_thresholds": {"type": "array", "items": {"type": "integer"}},
                },
            }
    return {"type": "object", "additionalProperties": True}


def operation_summary(route: Route) -> str:
    if route.handler:
        return route.handler
    return f"{route.method} {route.path}"


def operation_description(route: Route) -> str:
    return (
        f"Auto-generated from handler route registration. "
        f"RBAC scope/action hint: `{route.scope}`. Handler: `{route.handler}`."
    )


def operation_id(route: Route) -> str:
    base = route.handler or "route"
    suffix = f"{route.method}_{route.path}"
    raw = f"{base}_{suffix}"
    sanitized = (
        raw.replace("/", "_")
        .replace(":", "")
        .replace("-", "_")
        .replace("{", "")
        .replace("}", "")
    )
    while "__" in sanitized:
        sanitized = sanitized.replace("__", "_")
    return sanitized.strip("_")


def build_openapi(routes: List[Route], title: str = "RCA API Complete") -> dict:
    openapi = {
        "openapi": "3.0.3",
        "info": {
            "title": title,
            "version": "v1",
            "description": "Complete API surface scanned from Gin route registrations with RBAC hints.",
        },
        "servers": [{"url": "/"}],
        "tags": [],
        "paths": {},
        "components": {
            "securitySchemes": {
                "bearerAuth": {
                    "type": "http",
                    "scheme": "bearer",
                    "bearerFormat": "JWT",
                }
            },
            "schemas": {
                "ApiEnvelope": {
                    "type": "object",
                    "properties": {
                        "code": {"type": "integer"},
                        "reason": {"type": "string"},
                        "message": {"type": "string"},
                        "data": {"type": "object", "additionalProperties": True},
                    },
                }
            },
        },
    }
    tags = sorted({r.tag for r in routes})
    openapi["tags"] = [{"name": t} for t in tags]

    for r in routes:
        oas_path = to_openapi_path(r.path)
        if oas_path not in openapi["paths"]:
            openapi["paths"][oas_path] = {}

        params = []
        for name in path_params(r.path):
            params.append(
                {
                    "name": name,
                    "in": "path",
                    "required": True,
                    "schema": {"type": "string"},
                }
            )
        params.extend(query_parameters(r))

        security = [] if r.scope == "public" else [{"bearerAuth": []}]
        op = {
            "tags": [r.tag],
            "summary": operation_summary(r),
            "description": operation_description(r),
            "operationId": operation_id(r),
            "parameters": params,
            "responses": {
                "200": {
                    "description": "OK",
                    "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ApiEnvelope"}}},
                },
                "401": {"description": "Unauthorized"},
                "403": {"description": "Forbidden"},
                "500": {"description": "Internal Error"},
            },
            "security": security,
            "x-rbac-scope": r.scope,
            "x-handler": r.handler,
            "x-source-file": r.file,
        }

        body = request_body_schema(r)
        if body is not None:
            op["requestBody"] = {
                "required": False,
                "content": {
                    "application/json": {
                        "schema": body,
                    }
                },
            }

        openapi["paths"][oas_path][r.method.lower()] = op

    return openapi


def button_mapping(routes: List[Route]) -> List[dict]:
    wanted = [
        ("Replay", "/v1/sessions/:sessionID/actions/replay", "POST"),
        ("Follow-up", "/v1/sessions/:sessionID/actions/follow-up", "POST"),
        ("Review Start", "/v1/sessions/:sessionID/actions/review-start", "POST"),
        ("Review Confirm", "/v1/sessions/:sessionID/actions/review-confirm", "POST"),
        ("Review Reject", "/v1/sessions/:sessionID/actions/review-reject", "POST"),
        ("Assign", "/v1/sessions/:sessionID/actions/assign", "POST"),
        ("Reassign", "/v1/sessions/:sessionID/actions/reassign", "POST"),
        ("Inbox", "/v1/operator/inbox", "GET"),
        ("Dashboard", "/v1/operator/dashboard", "GET"),
        ("Dashboard Trends", "/v1/operator/dashboard/trends", "GET"),
        ("Team Dashboard", "/v1/operator/team_dashboard", "GET"),
        ("SLA Escalation Sync", "/v1/operator/sla/escalation-sync", "POST"),
        ("Assignment History", "/v1/sessions/:sessionID/assignment_history", "GET"),
        ("Session History", "/v1/sessions/:sessionID/history", "GET"),
        ("Workbench", "/v1/sessions/:sessionID/workbench", "GET"),
        ("Workbench Viewer", "/v1/sessions/:sessionID/workbench/viewer", "GET"),
        ("Trace", "/v1/ai/jobs/:jobID/trace", "GET"),
        ("Trace Compare", "/v1/ai/jobs:trace-compare", "GET"),
    ]
    idx = {(r.path, r.method): r for r in routes}
    rows: List[dict] = []
    for button, path, method in wanted:
        r = idx.get((path, method))
        rows.append(
            {
                "button": button,
                "endpoint": path,
                "method": method,
                "request_overview": "path/query/body see OpenAPI",
                "response_overview": "ApiEnvelope(data)",
                "rbac_scope": r.scope if r else "N/A",
                "handler": r.handler if r else "N/A",
            }
        )
    return rows


def write_csv(path: Path, rows: List[dict]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fields = ["button", "endpoint", "method", "request_overview", "response_overview", "rbac_scope", "handler"]
    with path.open("w", encoding="utf-8", newline="") as f:
        w = csv.DictWriter(f, fieldnames=fields)
        w.writeheader()
        w.writerows(rows)


def write_markdown(path: Path, rows: List[dict]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    header = "| Frontend Button | API Endpoint | Method | Request | Response | RBAC Scope | Handler |\n"
    sep = "|---|---|---|---|---|---|---|\n"
    lines = ["# RCA Frontend -> API Mapping\n", "\n", header, sep]
    for r in rows:
        lines.append(
            f"| {r['button']} | `{r['endpoint']}` | `{r['method']}` | {r['request_overview']} | {r['response_overview']} | `{r['rbac_scope']}` | `{r['handler']}` |\n"
        )
    path.write_text("".join(lines), encoding="utf-8")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--source-dir", required=False, default="cmd/rca-apiserver/app/")
    parser.add_argument("--internal-dir", required=False, default="internal/apiserver/")
    parser.add_argument("--swagger-output", required=True)
    parser.add_argument("--generate-mapping", required=False, default="docs/swagger/rca-complete.csv")
    parser.add_argument("--mapping-md", required=False, default="docs/swagger/rca-complete.md")
    args = parser.parse_args()

    source_dir = Path(args.source_dir)
    internal_dir = Path(args.internal_dir)
    search_roots: List[Path] = [source_dir, internal_dir]
    handler_dir = internal_dir / "handler"
    if handler_dir.exists():
        search_roots.append(handler_dir)
    routes = scan_routes(search_roots)

    openapi = build_openapi(routes)
    out = Path(args.swagger_output)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(openapi, ensure_ascii=False, indent=2), encoding="utf-8")

    mapping_rows = button_mapping(routes)
    write_csv(Path(args.generate_mapping), mapping_rows)
    write_markdown(Path(args.mapping_md), mapping_rows)

    print(f"routes_scanned={len(routes)}")
    print(f"swagger_output={out}")
    print(f"mapping_csv={args.generate_mapping}")
    print(f"mapping_md={args.mapping_md}")


if __name__ == "__main__":
    main()
