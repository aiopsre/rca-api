from __future__ import annotations

from dataclasses import dataclass
import json
from pathlib import Path
from typing import Any


def normalize_pipeline_key(pipeline: str | None) -> str:
    normalized = str(pipeline or "").strip().lower()
    if not normalized:
        return "basic_rca"
    return normalized


def normalize_tool_name(tool: str | None) -> str:
    normalized = str(tool or "").strip().lower()
    if normalized.startswith("mcp."):
        return normalized[4:]
    return normalized


@dataclass(frozen=True)
class ProviderConfig:
    provider_type: str
    allow_tools: tuple[str, ...]
    name: str = ""
    base_url: str = ""
    scopes: str = ""
    timeout_s: float = 10.0
    module: str = ""
    function: str = "call"


@dataclass(frozen=True)
class ToolsetDefinition:
    toolset_id: str
    providers: tuple[ProviderConfig, ...]


@dataclass(frozen=True)
class ToolsetConfig:
    pipelines: dict[str, tuple[str, ...]]
    toolsets: dict[str, ToolsetDefinition]

    def resolve_toolset_id(self, pipeline: str | None) -> str:
        return self.get_toolset_chain(pipeline)[0]

    def get_toolset_chain(self, pipeline: str | None) -> list[str]:
        normalized_pipeline = normalize_pipeline_key(pipeline)
        raw = self.pipelines.get(normalized_pipeline)
        if raw is None:
            raise ValueError(f"pipeline={normalized_pipeline} is not mapped to any toolset")
        if isinstance(raw, str):
            chain = [raw]
        elif isinstance(raw, (list, tuple)):
            chain = list(raw)
        else:
            raise ValueError(f"pipeline={normalized_pipeline} has invalid toolset mapping type={type(raw).__name__}")

        normalized_chain: list[str] = []
        for index, item in enumerate(chain, start=1):
            toolset_id = str(item).strip()
            if not toolset_id:
                raise ValueError(f"pipeline={normalized_pipeline} has empty toolset_id at index={index}")
            if toolset_id not in self.toolsets:
                raise ValueError(f"pipeline={normalized_pipeline} references missing toolset_id={toolset_id}")
            normalized_chain.append(toolset_id)
        if not normalized_chain:
            raise ValueError(f"pipeline={normalized_pipeline} has empty toolset chain")
        if len(normalized_chain) != len(chain):
            raise ValueError(f"pipeline={normalized_pipeline} contains invalid empty toolset_id entries")
        return normalized_chain

    def validate_references(self) -> None:
        for pipeline, chain in self.pipelines.items():
            if not chain:
                raise ValueError(f"pipeline={pipeline} has empty toolset chain")
            for index, toolset_id in enumerate(chain, start=1):
                normalized_toolset_id = str(toolset_id).strip()
                if not normalized_toolset_id:
                    raise ValueError(f"pipeline={pipeline} has empty toolset_id at index={index}")
                if normalized_toolset_id not in self.toolsets:
                    raise ValueError(f"pipeline={pipeline} references missing toolset_id={normalized_toolset_id}")
                if normalized_toolset_id != toolset_id:
                    raise ValueError(
                        f"pipeline={pipeline} has non-normalized toolset_id at index={index}: {toolset_id!r}"
                    )

    def get_toolset(self, toolset_id: str) -> ToolsetDefinition:
        normalized_id = str(toolset_id).strip()
        if not normalized_id:
            raise ValueError("toolset_id is required")
        toolset = self.toolsets.get(normalized_id)
        if toolset is None:
            raise ValueError(f"toolset={normalized_id} is not defined")
        return toolset


def load_toolset_config_from_env(settings: Any) -> ToolsetConfig:
    raw_json = str(getattr(settings, "toolset_config_json", "") or "").strip()
    path = str(getattr(settings, "toolset_config_path", "") or "").strip()

    payload: Any
    if raw_json:
        payload = _decode_json(raw_json, source="TOOLSET_CONFIG_JSON")
    elif path:
        file_content = _read_text(path)
        payload = _decode_json(file_content, source=f"TOOLSET_CONFIG_PATH={path}")
    else:
        raise ValueError("toolset config is empty: set TOOLSET_CONFIG_JSON or TOOLSET_CONFIG_PATH")
    return load_toolset_config(payload)


def load_toolset_config(payload: Any) -> ToolsetConfig:
    return _parse_toolset_config(payload)


def _read_text(path: str) -> str:
    try:
        return Path(path).expanduser().read_text(encoding="utf-8")
    except OSError as exc:
        raise ValueError(f"failed to read toolset config file: {path}: {exc}") from exc


def _decode_json(raw: str, *, source: str) -> Any:
    try:
        return json.loads(raw)
    except json.JSONDecodeError as exc:
        raise ValueError(f"invalid toolset config JSON from {source}: {exc}") from exc


def _parse_toolset_config(payload: Any) -> ToolsetConfig:
    if not isinstance(payload, dict):
        raise ValueError("toolset config must be a JSON object")

    pipelines_raw = payload.get("pipelines")
    if not isinstance(pipelines_raw, dict):
        raise ValueError("toolset config requires object field: pipelines")

    toolsets_raw = payload.get("toolsets")
    if not isinstance(toolsets_raw, dict):
        raise ValueError("toolset config requires object field: toolsets")

    pipelines: dict[str, tuple[str, ...]] = {}
    for raw_pipeline, raw_toolset_id in pipelines_raw.items():
        pipeline = normalize_pipeline_key(str(raw_pipeline))
        if isinstance(raw_toolset_id, list):
            toolset_chain: list[str] = []
            for index, item in enumerate(raw_toolset_id, start=1):
                if not isinstance(item, str):
                    raise ValueError(
                        f"pipeline={pipeline} has invalid toolset_id type at index={index}: {type(item).__name__}"
                    )
                normalized_item = item.strip()
                if not normalized_item:
                    raise ValueError(f"pipeline={pipeline} has empty toolset_id at index={index}")
                toolset_chain.append(normalized_item)
        elif isinstance(raw_toolset_id, str):
            single = raw_toolset_id.strip()
            if not single:
                raise ValueError(f"pipeline={pipeline} has empty toolset chain")
            toolset_chain = [single]
        else:
            if raw_toolset_id is None:
                raise ValueError(f"pipeline={pipeline} has empty toolset chain")
            raise ValueError(
                f"pipeline={pipeline} has invalid toolset mapping type={type(raw_toolset_id).__name__}: "
                "expected string or list[string]"
            )
        if not toolset_chain:
            raise ValueError(f"pipeline={pipeline} has empty toolset chain")
        pipelines[pipeline] = tuple(toolset_chain)

    toolsets: dict[str, ToolsetDefinition] = {}
    for raw_toolset_id, raw_toolset_payload in toolsets_raw.items():
        toolset_id = str(raw_toolset_id or "").strip()
        if not toolset_id:
            raise ValueError("toolset id must not be empty")
        if not isinstance(raw_toolset_payload, dict):
            raise ValueError(f"toolset payload must be object: toolset={toolset_id}")
        providers_raw = raw_toolset_payload.get("providers")
        if not isinstance(providers_raw, list) or not providers_raw:
            raise ValueError(f"toolset={toolset_id} requires non-empty providers list")

        providers: list[ProviderConfig] = []
        for index, provider_raw in enumerate(providers_raw, start=1):
            providers.append(_parse_provider(provider_raw, toolset_id=toolset_id, provider_index=index))
        toolsets[toolset_id] = ToolsetDefinition(toolset_id=toolset_id, providers=tuple(providers))

    config = ToolsetConfig(pipelines=pipelines, toolsets=toolsets)
    config.validate_references()
    return config


def _parse_provider(payload: Any, *, toolset_id: str, provider_index: int) -> ProviderConfig:
    if not isinstance(payload, dict):
        raise ValueError(f"provider entry must be object: toolset={toolset_id} index={provider_index}")

    provider_type = str(payload.get("type") or "").strip().lower()
    if not provider_type:
        raise ValueError(f"provider.type is required: toolset={toolset_id} index={provider_index}")

    allow_tools_raw = payload.get("allow_tools")
    if allow_tools_raw is None:
        allow_tools_raw = payload.get("allowTools")
    if not isinstance(allow_tools_raw, list) or not allow_tools_raw:
        raise ValueError(
            f"provider.allow_tools must be non-empty list: toolset={toolset_id} index={provider_index}"
        )
    allow_tools = tuple(_normalize_allow_tools(allow_tools_raw, toolset_id=toolset_id, provider_index=provider_index))

    name = str(payload.get("name") or "").strip()
    if not name:
        name = f"{provider_type}-{provider_index}"

    timeout_raw = payload.get("timeout_s")
    if timeout_raw is None:
        timeout_raw = payload.get("timeoutSeconds")
    timeout_s = _coerce_timeout(timeout_raw, default=10.0)

    base_url = str(payload.get("base_url") or payload.get("baseURL") or "").strip()
    scopes = str(payload.get("scopes") or "").strip()
    module = str(payload.get("module") or "").strip()
    function = str(payload.get("function") or "call").strip() or "call"

    if provider_type == "mcp_http":
        if not base_url:
            raise ValueError(f"provider.base_url is required for mcp_http: toolset={toolset_id} name={name}")
    elif provider_type == "skills":
        if not module:
            raise ValueError(f"provider.module is required for skills: toolset={toolset_id} name={name}")
    else:
        raise ValueError(f"unsupported provider.type={provider_type}: toolset={toolset_id} name={name}")

    return ProviderConfig(
        provider_type=provider_type,
        allow_tools=allow_tools,
        name=name,
        base_url=base_url,
        scopes=scopes,
        timeout_s=timeout_s,
        module=module,
        function=function,
    )


def _coerce_timeout(raw: Any, *, default: float) -> float:
    if raw is None:
        return default
    if isinstance(raw, bool):
        return default
    try:
        value = float(raw)
    except (TypeError, ValueError):
        return default
    if value <= 0:
        return default
    return value


def _normalize_allow_tools(raw: list[Any], *, toolset_id: str, provider_index: int) -> list[str]:
    out: list[str] = []
    seen: set[str] = set()
    for item in raw:
        normalized = normalize_tool_name(str(item))
        if not normalized:
            raise ValueError(
                "provider.allow_tools contains empty item: "
                f"toolset={toolset_id} index={provider_index}"
            )
        if normalized in seen:
            continue
        seen.add(normalized)
        out.append(normalized)
    if not out:
        raise ValueError(f"provider.allow_tools normalized to empty: toolset={toolset_id} index={provider_index}")
    return out
