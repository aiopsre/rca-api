"""Tests for middleware package (Phase HM2)."""
import pytest
from dataclasses import dataclass, field
from typing import Any

from orchestrator.middleware.base import AgentMiddleware, AgentRequest, AgentResponse
from orchestrator.middleware.chain import MiddlewareChain
from orchestrator.middleware.session import SessionMiddleware
from orchestrator.middleware.skills import SkillsMiddleware
from orchestrator.middleware.tool_surface import ToolSurfaceMiddleware
from orchestrator.middleware.observation import ObservationMiddleware


# Minimal mocks to avoid circular imports
@dataclass
class MockGraphState:
    """Minimal mock for GraphState used in middleware tests."""
    job_id: str
    incident_context: dict[str, Any] = field(default_factory=dict)


@dataclass
class MockToolSurface:
    """Minimal mock for ToolSurface."""
    tool_catalog_snapshot: dict[str, Any] = field(default_factory=dict)


@dataclass
class MockSkillSurface:
    """Minimal mock for SkillSurface."""
    skill_ids: list[str] = field(default_factory=list)
    capability_map: dict[str, list[str]] = field(default_factory=dict)


@dataclass
class MockResolvedAgentContext:
    """Minimal mock for ResolvedAgentContext used in middleware tests."""
    job_id: str
    pipeline: str
    template_id: str
    session_snapshot: dict[str, Any] = field(default_factory=dict)
    tool_surface: MockToolSurface = field(default_factory=MockToolSurface)
    skill_surface: MockSkillSurface = field(default_factory=MockSkillSurface)


class TestAgentRequest:
    """Tests for AgentRequest dataclass."""

    def test_default_values(self):
        """Test default values are empty."""
        request = AgentRequest(system_prompt="sys", user_prompt="user")
        assert request.system_prompt == "sys"
        assert request.user_prompt == "user"
        assert request.visible_tools == []
        assert request.metadata == {}

    def test_custom_values(self):
        """Test custom values are preserved."""
        request = AgentRequest(
            system_prompt="sys",
            user_prompt="user",
            visible_tools=[{"name": "tool1"}],
            metadata={"key": "value"},
        )
        assert request.visible_tools == [{"name": "tool1"}]
        assert request.metadata == {"key": "value"}


class TestAgentResponse:
    """Tests for AgentResponse dataclass."""

    def test_default_values(self):
        """Test default values."""
        response = AgentResponse()
        assert response.content == ""
        assert response.parsed == {}
        assert response.tool_calls == []
        assert response.metadata == {}

    def test_custom_values(self):
        """Test custom values are preserved."""
        response = AgentResponse(
            content="response content",
            parsed={"key": "value"},
            tool_calls=[{"name": "call1"}],
            metadata={"status": "ok"},
        )
        assert response.content == "response content"
        assert response.parsed == {"key": "value"}
        assert response.tool_calls == [{"name": "call1"}]


class TestMiddlewareChain:
    """Tests for MiddlewareChain."""

    def test_empty_chain_passes_through(self):
        """Test empty chain returns original request/response."""
        chain = MiddlewareChain()
        assert chain.is_empty()

        state = MockGraphState(job_id="test-job")
        context = MockResolvedAgentContext(job_id="test-job", pipeline="test", template_id="basic_rca")
        request = AgentRequest(system_prompt="sys", user_prompt="user")

        result = chain.prepare(state, context, request, {})
        assert result is request

        response = AgentResponse(content="content")
        result = chain.after_llm(state, context, response, {})
        assert result is response

    def test_chain_executes_in_order(self):
        """Test middleware executes in added order."""
        execution_order: list[str] = []

        class OrderMiddleware(AgentMiddleware):
            def __init__(self, name: str):
                self.name = name

            def prepare(self, state, context, request, config):
                execution_order.append(f"prepare:{self.name}")
                return request

            def after_llm(self, state, context, response, config):
                execution_order.append(f"after_llm:{self.name}")
                return response

        chain = MiddlewareChain()
        chain.add(OrderMiddleware("first"))
        chain.add(OrderMiddleware("second"))

        state = MockGraphState(job_id="test-job")
        context = MockResolvedAgentContext(job_id="test-job", pipeline="test", template_id="basic_rca")
        request = AgentRequest(system_prompt="sys", user_prompt="user")

        chain.prepare(state, context, request, {})
        assert execution_order == ["prepare:first", "prepare:second"]

        execution_order.clear()
        response = AgentResponse()
        chain.after_llm(state, context, response, {})
        assert execution_order == ["after_llm:first", "after_llm:second"]

    def test_chain_can_modify_request(self):
        """Test middleware can modify request."""

        class ModifyingMiddleware(AgentMiddleware):
            def prepare(self, state, context, request, config):
                request.user_prompt = f"MODIFIED: {request.user_prompt}"
                request.visible_tools = [{"name": "added_tool"}]
                return request

        chain = MiddlewareChain()
        chain.add(ModifyingMiddleware())

        state = MockGraphState(job_id="test-job")
        context = MockResolvedAgentContext(job_id="test-job", pipeline="test", template_id="basic_rca")
        request = AgentRequest(system_prompt="sys", user_prompt="original")

        result = chain.prepare(state, context, request, {})
        assert result.user_prompt == "MODIFIED: original"
        assert result.visible_tools == [{"name": "added_tool"}]


class TestSessionMiddleware:
    """Tests for SessionMiddleware."""

    @pytest.fixture
    def middleware(self):
        return SessionMiddleware()

    @pytest.fixture
    def state(self):
        return MockGraphState(
            job_id="test-job",
            incident_context={"alert_name": "HighCPU"},
        )

    @pytest.fixture
    def context(self):
        return MockResolvedAgentContext(
            job_id="test-job",
            pipeline="test",
            template_id="basic_rca",
            session_snapshot={"key": "value"},
        )

    def test_injects_session_context(self, middleware, state, context):
        """Test session context is injected into prompt."""
        request = AgentRequest(system_prompt="sys", user_prompt="original")
        result = middleware.prepare(state, context, request, {})

        assert "Session Context:" in result.user_prompt
        assert "Incident Context:" in result.user_prompt
        assert "original" in result.user_prompt

    def test_respects_include_flags(self, middleware, state, context):
        """Test include flags control injection."""
        request = AgentRequest(system_prompt="sys", user_prompt="original")

        result = middleware.prepare(state, context, request, {"include_session": False})
        assert "Session Context:" not in result.user_prompt
        assert "Incident Context:" in result.user_prompt

        request = AgentRequest(system_prompt="sys", user_prompt="original")
        result = middleware.prepare(state, context, request, {"include_incident": False})
        assert "Session Context:" in result.user_prompt
        assert "Incident Context:" not in result.user_prompt


class TestSkillsMiddleware:
    """Tests for SkillsMiddleware."""

    @pytest.fixture
    def middleware(self):
        return SkillsMiddleware()

    @pytest.fixture
    def state(self):
        return MockGraphState(job_id="test-job")

    @pytest.fixture
    def context(self):
        return MockResolvedAgentContext(
            job_id="test-job",
            pipeline="test",
            template_id="basic_rca",
            skill_surface=MockSkillSurface(
                skill_ids=["skill-1", "skill-2"],
                capability_map={
                    "evidence_plan": ["skill-1@v1:executor"],
                    "diagnosis": ["skill-2@v1:executor"],
                },
            ),
        )

    def test_injects_skill_ids(self, middleware, state, context):
        """Test skill IDs are injected."""
        request = AgentRequest(system_prompt="sys", user_prompt="original")
        result = middleware.prepare(state, context, request, {})

        assert "Available Skills:" in result.user_prompt
        assert "skill-1" in result.user_prompt
        assert "skill-2" in result.user_prompt

    def test_injects_capability_map_when_enabled(self, middleware, state, context):
        """Test capability map is injected when enabled."""
        request = AgentRequest(system_prompt="sys", user_prompt="original")
        result = middleware.prepare(
            state, context, request, {"include_capability_map": True}
        )

        assert "Capability Map:" in result.user_prompt
        assert "evidence_plan" in result.user_prompt

    def test_filters_capabilities(self, middleware, state, context):
        """Test capability filtering."""
        request = AgentRequest(system_prompt="sys", user_prompt="original")
        result = middleware.prepare(
            state,
            context,
            request,
            {
                "include_capability_map": True,
                "filter_capabilities": ["evidence_plan"],
            },
        )

        assert "Capability Map:" in result.user_prompt
        assert "evidence_plan" in result.user_prompt
        assert "diagnosis" not in result.user_prompt

    def test_handles_empty_skill_surface(self, middleware, state):
        """Test handles empty skill surface gracefully."""
        context = MockResolvedAgentContext(
            job_id="test-job",
            pipeline="test",
            template_id="basic_rca",
        )
        request = AgentRequest(system_prompt="sys", user_prompt="original")
        result = middleware.prepare(state, context, request, {})

        assert result.user_prompt == "original"


class TestToolSurfaceMiddleware:
    """Tests for ToolSurfaceMiddleware."""

    @pytest.fixture
    def middleware(self):
        return ToolSurfaceMiddleware()

    @pytest.fixture
    def state(self):
        return MockGraphState(job_id="test-job")

    @pytest.fixture
    def context_with_tools(self):
        return MockResolvedAgentContext(
            job_id="test-job",
            pipeline="test",
            template_id="basic_rca",
            tool_surface=MockToolSurface(
                tool_catalog_snapshot={
                    "tools": [
                        {"name": "tool1", "description": "desc1", "tool_class": "fc_selectable"},
                        {"name": "tool2", "description": "desc2", "tool_class": "fc_selectable"},
                        {"name": "tool3", "description": "desc3", "tool_class": "runtime_owned"},
                        {"name": "tool4", "description": "desc4", "tool_class": "fc_selectable", "allowed_for_prompt_skill": False},
                        {"name": "tool5", "description": "desc5", "tool_class": "fc_selectable", "allowed_for_graph_agent": False},
                    ]
                }
            ),
        )

    def test_skills_only_mode_empty_tools(self, middleware, state, context_with_tools):
        """Test skills_only mode returns empty tools."""
        request = AgentRequest(system_prompt="sys", user_prompt="user")
        result = middleware.prepare(state, context_with_tools, request, {"mode": "skills_only"})

        assert result.visible_tools == []

    def test_all_mode_shows_all_tools(self, middleware, state, context_with_tools):
        """Test all mode shows all tools."""
        request = AgentRequest(system_prompt="sys", user_prompt="user")
        result = middleware.prepare(state, context_with_tools, request, {"mode": "all"})

        assert len(result.visible_tools) == 5

    def test_fc_surface_mode_filters_by_class(self, middleware, state, context_with_tools):
        """Test fc_surface mode only shows fc_selectable tools."""
        request = AgentRequest(system_prompt="sys", user_prompt="user")
        result = middleware.prepare(state, context_with_tools, request, {"mode": "fc_surface"})

        tool_names = [t["name"] for t in result.visible_tools]
        assert "tool1" in tool_names
        assert "tool2" in tool_names
        assert "tool3" not in tool_names  # runtime_owned

    def test_tool_scope_filters_tools(self, middleware, state, context_with_tools):
        """Test tool_scope filters specific tools."""
        request = AgentRequest(system_prompt="sys", user_prompt="user")
        result = middleware.prepare(
            state,
            context_with_tools,
            request,
            {"tool_scope": ["tool1", "tool3"]},
        )

        tool_names = [t["name"] for t in result.visible_tools]
        assert "tool1" in tool_names
        assert "tool2" not in tool_names
        assert "tool3" in tool_names

    def test_skills_surface_uses_allowed_for_prompt_skill(self, middleware, state, context_with_tools):
        """Test skills surface uses allowed_for_prompt_skill field."""
        request = AgentRequest(system_prompt="sys", user_prompt="user")
        result = middleware.prepare(
            state,
            context_with_tools,
            request,
            {"surface": "skills"},
        )

        tool_names = [t["name"] for t in result.visible_tools]
        assert "tool4" not in tool_names  # allowed_for_prompt_skill: False
        assert "tool5" in tool_names  # allowed_for_graph_agent: False, but allowed_for_prompt_skill: True (default)

    def test_graph_surface_uses_allowed_for_graph_agent(self, middleware, state, context_with_tools):
        """Test graph surface uses allowed_for_graph_agent field."""
        request = AgentRequest(system_prompt="sys", user_prompt="user")
        result = middleware.prepare(
            state,
            context_with_tools,
            request,
            {"surface": "graph"},
        )

        tool_names = [t["name"] for t in result.visible_tools]
        assert "tool5" not in tool_names  # allowed_for_graph_agent: False
        assert "tool4" in tool_names  # allowed_for_prompt_skill: False, but allowed_for_graph_agent: True (default)

    def test_unknown_surface_rejects_all_tools(self, middleware, state, context_with_tools):
        """Test unknown surface rejects all tools for safety."""
        request = AgentRequest(system_prompt="sys", user_prompt="user")
        result = middleware.prepare(
            state,
            context_with_tools,
            request,
            {"surface": "unknown_surface"},
        )

        assert result.visible_tools == []

    def test_handles_empty_tool_surface(self, middleware, state):
        """Test handles empty tool surface gracefully."""
        context = MockResolvedAgentContext(
            job_id="test-job",
            pipeline="test",
            template_id="basic_rca",
        )
        request = AgentRequest(system_prompt="sys", user_prompt="user")
        result = middleware.prepare(state, context, request, {})

        assert result.visible_tools == []


class TestObservationMiddleware:
    """Tests for ObservationMiddleware."""

    @pytest.fixture
    def middleware(self):
        return ObservationMiddleware()

    @pytest.fixture
    def state(self):
        return MockGraphState(job_id="test-job")

    @pytest.fixture
    def context(self):
        return MockResolvedAgentContext(
            job_id="test-job",
            pipeline="test",
            template_id="basic_rca",
        )

    def test_prepare_adds_metadata(self, middleware, state, context):
        """Test prepare adds observation metadata."""
        request = AgentRequest(system_prompt="sys", user_prompt="user")
        result = middleware.prepare(
            state, context, request, {"observation_type": "test.request", "domain": "test"}
        )

        assert result.metadata["observation_type"] == "test.request"
        assert result.metadata["domain"] == "test"
        assert result.metadata["job_id"] == "test-job"

    def test_after_llm_adds_metadata(self, middleware, state, context):
        """Test after_llm adds observation metadata."""
        response = AgentResponse(content="content")
        result = middleware.after_llm(
            state, context, response, {"observation_type": "test.response", "domain": "test"}
        )

        assert result.metadata["observation_type"] == "test.response"
        assert result.metadata["domain"] == "test"
        assert result.metadata["job_id"] == "test-job"


class TestMiddlewareIntegration:
    """Integration tests for middleware in production path."""

    def test_build_middleware_chain_returns_chain_when_enabled(self):
        """Test _build_middleware_chain returns chain when enabled."""
        from orchestrator.daemon.runner import _build_middleware_chain
        from orchestrator.daemon.settings import load_settings

        settings = load_settings()
        chain = _build_middleware_chain(settings=settings)

        assert chain is not None
        assert not chain.is_empty()

    def test_build_middleware_chain_returns_none_when_disabled(self, monkeypatch):
        """Test _build_middleware_chain returns None when disabled."""
        from orchestrator.daemon.runner import _build_middleware_chain
        from orchestrator.daemon.settings import load_settings

        settings = load_settings()
        # Disable middleware
        settings.rca_hybrid_middleware_enabled = False

        chain = _build_middleware_chain(settings=settings)

        assert chain is None

    def test_config_accepts_middleware_fields(self):
        """Test OrchestratorConfig accepts middleware fields."""
        from orchestrator.langgraph.config import OrchestratorConfig

        config = OrchestratorConfig()
        assert config.middleware_chain is None
        assert config.middleware_enabled is True

        # Can set middleware_chain
        chain = MiddlewareChain()
        config.middleware_chain = chain
        config.middleware_enabled = True

        assert config.middleware_chain is chain
        assert config.middleware_enabled is True

    def test_tool_surface_middleware_uses_correct_field_names(self):
        """Test ToolSurfaceMiddleware uses correct field names for surfaces."""
        from orchestrator.middleware.tool_surface import SURFACE_TO_ALLOWED_FIELD

        # Verify explicit mapping
        assert SURFACE_TO_ALLOWED_FIELD["skills"] == "allowed_for_prompt_skill"
        assert SURFACE_TO_ALLOWED_FIELD["graph"] == "allowed_for_graph_agent"

        # Verify unknown surface is not in mapping (will be rejected)
        assert "unknown" not in SURFACE_TO_ALLOWED_FIELD