from __future__ import annotations

from dataclasses import dataclass
import json
import logging
from typing import Any, Protocol

from .mcp_server_loader import McpServerRef, build_provider_configs_from_mcpserver_refs
from .providers.mcp_http import MCPHttpProvider
from .toolset_config import ToolsetConfig, ToolsetDefinition, normalize_tool_name

TOOLING_META_KEY = "_tooling_meta"
_LOGGER = logging.getLogger(__name__)
_CHAIN_SUMMARY_MAX_LEN = 160
_MCP_SERVERS_TOOLSET_ID = "mcp_servers"


class ToolProvider(Protocol):
    def call(
        self,
        *,
        tool: str,
        input_payload: dict[str, Any] | None,
        idempotency_key: str | None = None,
    ) -> dict[str, Any]:
        ...


class ToolInvokeError(RuntimeError):
    def __init__(self, message: str, *, retryable: bool = False, reason: str = "") -> None:
        super().__init__(message)
        self.retryable = bool(retryable)
        self.reason = str(reason).strip()


@dataclass(frozen=True)
class _ProviderBinding:
    name: str
    provider_type: str
    allow_tools: frozenset[str]
    provider: ToolProvider

    def allows(self, tool: str) -> bool:
        return tool in self.allow_tools


class ToolInvoker:
    def __init__(
        self,
        *,
        toolset_id: str,
        providers: list[_ProviderBinding],
    ) -> None:
        normalized_toolset_id = str(toolset_id).strip()
        if not normalized_toolset_id:
            raise ValueError("toolset_id is required")
        if not providers:
            raise ValueError(f"toolset={normalized_toolset_id} has no providers")
        self._toolset_id = normalized_toolset_id
        self._providers = tuple(providers)

    @property
    def toolset_id(self) -> str:
        return self._toolset_id

    def allowed_tools(self) -> list[str]:
        names: set[str] = set()
        for binding in self._providers:
            for tool in binding.allow_tools:
                normalized = str(tool).strip()
                if normalized:
                    names.add(normalized)
        return sorted(names)

    def provider_summaries(self) -> list[dict[str, Any]]:
        return [
            {
                "toolset_id": self._toolset_id,
                "provider_id": binding.name,
                "provider_type": binding.provider_type,
                "allow_tools_count": len(binding.allow_tools),
                "allow_tools": sorted(binding.allow_tools),
            }
            for binding in self._providers
        ]

    def call(
        self,
        *,
        tool: str,
        input_payload: dict[str, Any] | None,
        idempotency_key: str | None = None,
    ) -> dict[str, Any]:
        normalized_tool = normalize_tool_name(tool)
        if not normalized_tool:
            raise ToolInvokeError("tool name is required")

        normalized_input = input_payload if isinstance(input_payload, dict) else {}
        result = _call_with_bindings(
            toolset_id=self._toolset_id,
            providers=self._providers,
            tool=normalized_tool,
            input_payload=normalized_input,
            idempotency_key=idempotency_key,
        )
        if result is not None:
            return result

        raise ToolInvokeError(
            f"tool={normalized_tool} is not allowed in toolset={self._toolset_id}",
            retryable=False,
            reason="allow_tools_denied",
        )


class ToolInvokerChain:
    def __init__(self, *, toolset_invokers: list[ToolInvoker]) -> None:
        if not toolset_invokers:
            raise ValueError("toolset_invokers is required")
        self._toolset_invokers = tuple(toolset_invokers)

    @property
    def toolset_ids(self) -> list[str]:
        return [invoker.toolset_id for invoker in self._toolset_invokers]

    def allowed_tools(self) -> list[str]:
        names: set[str] = set()
        for invoker in self._toolset_invokers:
            for tool in invoker.allowed_tools():
                normalized = str(tool).strip()
                if normalized:
                    names.add(normalized)
        return sorted(names)

    def provider_summaries(self) -> list[dict[str, Any]]:
        out: list[dict[str, Any]] = []
        for invoker in self._toolset_invokers:
            for item in invoker.provider_summaries():
                if isinstance(item, dict):
                    out.append(dict(item))
        return out

    def call(
        self,
        *,
        tool: str,
        input_payload: dict[str, Any] | None,
        idempotency_key: str | None = None,
    ) -> dict[str, Any]:
        normalized_tool = normalize_tool_name(tool)
        if not normalized_tool:
            raise ToolInvokeError("tool name is required")
        normalized_input = input_payload if isinstance(input_payload, dict) else {}
        total = len(self._toolset_invokers)
        for index, invoker in enumerate(self._toolset_invokers, start=1):
            try:
                return invoker.call(
                    tool=normalized_tool,
                    input_payload=normalized_input,
                    idempotency_key=idempotency_key,
                )
            except ToolInvokeError as exc:
                if exc.reason == "allow_tools_denied":
                    _LOGGER.debug(
                        "toolset chain denied tool=%s toolset=%s hop=%d/%d reason=%s",
                        normalized_tool,
                        invoker.toolset_id,
                        index,
                        total,
                        exc.reason or "allow_tools_denied",
                    )
                    continue
                raise
        chain = _summarize_toolset_chain(self.toolset_ids)
        raise ToolInvokeError(
            f"tool={normalized_tool} denied by toolset_chain={chain}",
            retryable=False,
            reason="allow_tools_denied",
        )


def build_tool_invoker(config: ToolsetConfig, toolset_id: str) -> ToolInvoker:
    toolset = config.get_toolset(toolset_id)
    providers = _build_provider_bindings(toolset)
    return ToolInvoker(toolset_id=toolset.toolset_id, providers=providers)


def build_tool_invoker_chain(config: ToolsetConfig, toolset_ids: list[str]) -> ToolInvokerChain:
    normalized_ids = [str(item).strip() for item in toolset_ids if str(item).strip()]
    if not normalized_ids:
        raise ValueError("toolset_ids is required")
    invokers = [build_tool_invoker(config, toolset_id) for toolset_id in normalized_ids]
    return ToolInvokerChain(toolset_invokers=invokers)


def _build_provider_bindings(toolset: ToolsetDefinition) -> list[_ProviderBinding]:
    providers: list[_ProviderBinding] = []
    for provider_cfg in toolset.providers:
        provider_type = provider_cfg.provider_type
        if provider_type == "mcp_http":
            provider: ToolProvider = MCPHttpProvider(
                base_url=provider_cfg.base_url,
                scopes=provider_cfg.scopes,
                timeout_s=provider_cfg.timeout_s,
            )
        elif provider_type == "skills":
            raise ValueError(
                "provider.type=skills is deprecated: "
                f"toolset={toolset.toolset_id} provider={provider_cfg.name}; "
                "migrate to skill releases/skillsets or mcp_http"
            )
        else:
            raise ValueError(
                f"unsupported provider.type={provider_type}: "
                f"toolset={toolset.toolset_id} provider={provider_cfg.name}"
            )
        providers.append(
            _ProviderBinding(
                name=provider_cfg.name,
                provider_type=provider_type,
                allow_tools=frozenset(provider_cfg.allow_tools),
                provider=provider,
            )
        )
    return providers


def _call_with_bindings(
    *,
    toolset_id: str,
    providers: tuple[_ProviderBinding, ...] | list[_ProviderBinding],
    tool: str,
    input_payload: dict[str, Any],
    idempotency_key: str | None,
) -> dict[str, Any] | None:
    for binding in providers:
        if not binding.allows(tool):
            continue
        result = binding.provider.call(
            tool=tool,
            input_payload=input_payload,
            idempotency_key=idempotency_key,
        )
        if not isinstance(result, dict):
            raise RuntimeError(f"tool provider must return dict: toolset={toolset_id} provider={binding.name}")
        out = dict(result)
        out[TOOLING_META_KEY] = {
            "provider_id": binding.name,
            "provider_type": binding.provider_type,
            "resolved_from_toolset_id": toolset_id,
        }
        return out
    return None


def _summarize_toolset_chain(toolset_ids: list[str]) -> str:
    normalized = [str(item).strip() for item in toolset_ids if str(item).strip()]
    if not normalized:
        return "<empty>"
    if len(normalized) <= 4:
        summary = ",".join(normalized)
    else:
        head = ",".join(normalized[:4])
        summary = f"{head},...(+{len(normalized) - 4})"
    if len(summary) <= _CHAIN_SUMMARY_MAX_LEN:
        return summary
    return f"{summary[: _CHAIN_SUMMARY_MAX_LEN - 3]}..."


def build_tool_invoker_from_mcpserver_refs(
    refs: list[McpServerRef],
    *,
    toolset_id: str = _MCP_SERVERS_TOOLSET_ID,
) -> ToolInvoker | None:
    """Build a ToolInvoker from McpServerRefs resolved from the platform.

    This creates a toolset that routes tool calls to external MCP servers
    configured via the platform's McpServer management.

    Args:
        refs: List of McpServerRef objects from claim response.
        toolset_id: Toolset ID to use for this invoker.

    Returns:
        ToolInvoker if refs is non-empty, None otherwise.
    """
    if not refs:
        return None

    configs = build_provider_configs_from_mcpserver_refs(refs)
    if not configs:
        return None

    providers: list[_ProviderBinding] = []
    for cfg in configs:
        provider = MCPHttpProvider(
            base_url=cfg.base_url,
            scopes=cfg.scopes,
            timeout_s=cfg.timeout_s,
        )
        providers.append(
            _ProviderBinding(
                name=cfg.name,
                provider_type="mcp_http",
                allow_tools=frozenset(cfg.allow_tools),
                provider=provider,
            )
        )

    return ToolInvoker(toolset_id=toolset_id, providers=providers)


def merge_tool_invokers(
    primary: ToolInvoker,
    secondary: ToolInvoker | None,
) -> ToolInvoker | ToolInvokerChain:
    """Merge two tool invokers into a chain.

    If secondary is None, returns primary unchanged.
    If secondary is present, returns a chain that tries primary first.

    Args:
        primary: Primary invoker (tried first).
        secondary: Secondary invoker (tried if primary denies tool).

    Returns:
        ToolInvoker if secondary is None, ToolInvokerChain otherwise.
    """
    if secondary is None:
        return primary
    return ToolInvokerChain(toolset_invokers=[primary, secondary])


def build_tool_invoker_from_mcpserver_refs_json(
    mcpserver_refs_json: str,
    *,
    toolset_id: str = _MCP_SERVERS_TOOLSET_ID,
) -> ToolInvoker | None:
    """Build a ToolInvoker from mcpserver_refs_json string.

    Convenience function that parses JSON and builds invoker.
    Also registers tool metadata from the platform.

    Args:
        mcpserver_refs_json: JSON string from claim response.
        toolset_id: Toolset ID to use for this invoker.

    Returns:
        ToolInvoker if valid refs, None otherwise.
    """
    from .mcp_server_loader import parse_mcpserver_refs

    refs = parse_mcpserver_refs(mcpserver_refs_json)

    # Register tool metadata from platform
    if refs:
        from ..runtime.tool_registry import register_tools_from_mcpserver_refs
        register_tools_from_mcpserver_refs(refs)

    return build_tool_invoker_from_mcpserver_refs(refs, toolset_id=toolset_id)


def build_tool_invoker_from_resolved_providers(
    resolved_providers: list[dict[str, Any]],
    *,
    toolset_id: str = "resolved_providers",
    platform_base_url: str | None = None,
) -> ToolInvoker | None:
    """Build a ToolInvoker from resolved_tool_providers list.

    This is the new canonical path for claim provider snapshot.
    Prefers structured resolved_tool_providers over legacy mcpServersJSON.

    Args:
        resolved_providers: List of ResolvedToolProvider dicts from claim response.
        toolset_id: Toolset ID to use for this invoker.
        platform_base_url: Base URL for builtin providers (RCA API base URL).
            Required for builtin providers that have empty base_url.

    Returns:
        ToolInvoker if valid providers, None otherwise.
    """
    from .mcp_server_loader import (
        build_provider_configs_from_resolved_providers,
        parse_resolved_tool_providers,
    )

    providers = parse_resolved_tool_providers(resolved_providers)
    if not providers:
        return None

    # Register tool metadata from platform
    from ..runtime.tool_registry import register_tools_from_resolved_providers
    register_tools_from_resolved_providers(providers)

    # Process providers - builtin providers use platform_base_url
    provider_bindings: list[_ProviderBinding] = []
    for provider in providers:
        # Determine base_url for this provider
        if provider.server_kind == "builtin":
            # Builtin providers are served by the platform itself
            base_url = provider.base_url
            if not base_url:
                # Use platform base URL if builtin provider has empty base_url
                if not platform_base_url:
                    _LOGGER.warning(
                        "builtin provider %s has no base_url and platform_base_url not provided, skipping",
                        provider.name,
                    )
                    continue
                base_url = platform_base_url
        else:
            # External providers must have base_url
            base_url = provider.base_url
            if not base_url:
                _LOGGER.warning(
                    "external provider %s has no base_url, skipping",
                    provider.name,
                )
                continue

        # Create MCP HTTP provider
        provider_client = MCPHttpProvider(
            base_url=base_url,
            scopes=provider.scopes,
            timeout_s=provider.timeout_sec,
        )
        provider_bindings.append(
            _ProviderBinding(
                name=provider.name,
                provider_type=provider.provider_type,
                allow_tools=frozenset(provider.allowed_tools),
                provider=provider_client,
            )
        )

    if not provider_bindings:
        return None

    return ToolInvoker(toolset_id=toolset_id, providers=provider_bindings)
