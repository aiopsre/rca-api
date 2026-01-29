from __future__ import annotations

from dataclasses import dataclass
from typing import Any, Protocol

from .providers.mcp_http import MCPHttpProvider
from .providers.skills import SkillsProvider
from .toolset_config import ToolsetConfig, normalize_tool_name

TOOLING_META_KEY = "_tooling_meta"


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

    def provider_summaries(self) -> list[dict[str, Any]]:
        return [
            {
                "provider_id": binding.name,
                "provider_type": binding.provider_type,
                "allow_tools_count": len(binding.allow_tools),
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
        for binding in self._providers:
            if not binding.allows(normalized_tool):
                continue
            result = binding.provider.call(
                tool=normalized_tool,
                input_payload=normalized_input,
                idempotency_key=idempotency_key,
            )
            if not isinstance(result, dict):
                raise RuntimeError(
                    f"tool provider must return dict: toolset={self._toolset_id} provider={binding.name}"
                )
            out = dict(result)
            out[TOOLING_META_KEY] = {
                "provider_id": binding.name,
                "provider_type": binding.provider_type,
            }
            return out

        raise ToolInvokeError(
            f"tool={normalized_tool} is not allowed in toolset={self._toolset_id}",
            retryable=False,
            reason="allow_tools_denied",
        )


def build_tool_invoker(config: ToolsetConfig, toolset_id: str) -> ToolInvoker:
    toolset = config.get_toolset(toolset_id)
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
            provider = SkillsProvider(
                module=provider_cfg.module,
                function=provider_cfg.function,
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
    return ToolInvoker(toolset_id=toolset.toolset_id, providers=providers)
