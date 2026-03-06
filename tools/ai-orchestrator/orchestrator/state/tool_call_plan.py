"""Tool call plan data structures for dynamic tool execution.

This module defines the data structures used to represent a plan for
executing multiple tool calls, including parallel execution groups and
dependency tracking.
"""
from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any


@dataclass
class ToolCallItem:
    """Represents a single tool call in a plan.

    Attributes:
        tool: The name of the tool to call (e.g., "prometheus_query").
        params: Parameters to pass to the tool.
        query_type: Classification of the tool (e.g., "metrics", "logs", "traces").
        purpose: Human-readable explanation of why this tool is being called.
        evidence_kind: Type of evidence this tool produces (default: "query").
        optional: Whether this tool call is optional (failure won't stop the plan).
        depends_on: List of other tool call IDs this call depends on.
        call_id: Optional unique identifier for this call (auto-generated if not set).
    """
    tool: str
    params: dict[str, Any] = field(default_factory=dict)
    query_type: str = ""
    purpose: str = ""
    evidence_kind: str = "query"
    optional: bool = False
    depends_on: list[str] = field(default_factory=list)
    call_id: str = ""

    def to_dict(self) -> dict[str, Any]:
        """Serialize this item to a dictionary.

        Returns:
            Dictionary representation of this tool call item.
        """
        result: dict[str, Any] = {
            "tool": self.tool,
            "params": self.params,
        }
        if self.query_type:
            result["query_type"] = self.query_type
        if self.purpose:
            result["purpose"] = self.purpose
        if self.evidence_kind != "query":
            result["evidence_kind"] = self.evidence_kind
        if self.optional:
            result["optional"] = self.optional
        if self.depends_on:
            result["depends_on"] = list(self.depends_on)
        if self.call_id:
            result["call_id"] = self.call_id
        return result

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "ToolCallItem":
        """Create a ToolCallItem from a dictionary.

        Args:
            data: Dictionary containing tool call item data.

        Returns:
            A new ToolCallItem instance.
        """
        return cls(
            tool=str(data.get("tool") or ""),
            params=data.get("params") if isinstance(data.get("params"), dict) else {},
            query_type=str(data.get("query_type") or ""),
            purpose=str(data.get("purpose") or ""),
            evidence_kind=str(data.get("evidence_kind") or "query"),
            optional=bool(data.get("optional")),
            depends_on=list(data.get("depends_on") or []) if isinstance(data.get("depends_on"), list) else [],
            call_id=str(data.get("call_id") or ""),
        )


@dataclass
class ToolCallPlan:
    """A plan for executing multiple tool calls.

    Supports parallel execution groups where items in the same group
    can be executed concurrently.

    Attributes:
        items: List of tool call items to execute.
        parallel_groups: List of index groups for parallel execution.
                        Example: [[0, 1], [2]] means items[0] and items[1]
                        can run in parallel, then items[2] runs after.
    """
    items: list[ToolCallItem] = field(default_factory=list)
    parallel_groups: list[list[int]] = field(default_factory=list)

    def to_dict(self) -> dict[str, Any]:
        """Serialize this plan to a dictionary.

        Returns:
            Dictionary representation suitable for storage in GraphState.
        """
        return {
            "items": [item.to_dict() for item in self.items],
            "parallel_groups": [list(group) for group in self.parallel_groups],
        }

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "ToolCallPlan":
        """Create a ToolCallPlan from a dictionary.

        Args:
            data: Dictionary containing plan data.

        Returns:
            A new ToolCallPlan instance.
        """
        items_data = data.get("items")
        items = []
        if isinstance(items_data, list):
            for item_data in items_data:
                if isinstance(item_data, dict):
                    items.append(ToolCallItem.from_dict(item_data))

        groups_data = data.get("parallel_groups")
        parallel_groups = []
        if isinstance(groups_data, list):
            for group in groups_data:
                if isinstance(group, list):
                    parallel_groups.append([int(idx) for idx in group if isinstance(idx, int)])

        return cls(items=items, parallel_groups=parallel_groups)

    def add_item(self, item: ToolCallItem) -> int:
        """Add a tool call item to the plan.

        Args:
            item: The tool call item to add.

        Returns:
            The index of the added item.
        """
        idx = len(self.items)
        self.items.append(item)
        return idx

    def add_parallel_group(self, indices: list[int]) -> None:
        """Add a group of indices for parallel execution.

        Args:
            indices: List of item indices that can run in parallel.
        """
        self.parallel_groups.append(list(indices))

    def get_execution_order(self) -> list[list[int]]:
        """Get the execution order as a list of index groups.

        If no parallel groups are defined, returns all items as a single
        sequential group.

        Returns:
            List of index groups to execute in order.
        """
        if self.parallel_groups:
            return self.parallel_groups

        # Default: all items can run in parallel
        if self.items:
            return [list(range(len(self.items)))]
        return []

    def validate(self) -> list[str]:
        """Validate the plan for common issues.

        Returns:
            List of validation error messages (empty if valid).
        """
        errors: list[str] = []

        # Check for empty tool names
        for i, item in enumerate(self.items):
            if not item.tool.strip():
                errors.append(f"items[{i}].tool is empty")

        # Check for valid indices in parallel groups
        max_idx = len(self.items) - 1
        seen_indices: set[int] = set()

        for group_idx, group in enumerate(self.parallel_groups):
            for idx in group:
                if idx < 0:
                    errors.append(f"parallel_groups[{group_idx}] contains negative index {idx}")
                elif idx > max_idx:
                    errors.append(f"parallel_groups[{group_idx}] contains out-of-range index {idx}")
                elif idx in seen_indices:
                    errors.append(f"parallel_groups[{group_idx}] contains duplicate index {idx}")
                else:
                    seen_indices.add(idx)

        # Check for uncovered indices
        uncovered = set(range(len(self.items))) - seen_indices
        if uncovered and self.parallel_groups:
            errors.append(f"items with indices {sorted(uncovered)} are not covered by parallel_groups")

        return errors

    def is_empty(self) -> bool:
        """Check if the plan has no items.

        Returns:
            True if the plan has no items.
        """
        return len(self.items) == 0

    def item_count(self) -> int:
        """Get the number of items in the plan.

        Returns:
            Number of tool call items.
        """
        return len(self.items)


def build_default_tool_call_plan(
    available_tools: list[dict[str, Any]],
    incident_context: dict[str, Any],
    *,
    max_parallel: int = 5,
) -> ToolCallPlan:
    """Build a default tool call plan from available tools.

    Creates a plan that queries metrics and logs tools if available.

    Args:
        available_tools: List of tool descriptors with 'name' and 'tags' keys.
        incident_context: Context about the incident (service, namespace, etc.).
        max_parallel: Maximum number of tools to run in parallel.

    Returns:
        A ToolCallPlan with default queries for available tools.
    """
    plan = ToolCallPlan()
    parallel_indices: list[int] = []

    # Extract context for default queries
    service = str(incident_context.get("service") or "")
    namespace = str(incident_context.get("namespace") or "")

    for tool_info in available_tools:
        if not isinstance(tool_info, dict):
            continue

        name = str(tool_info.get("name") or "")
        if not name:
            continue

        tags = tool_info.get("tags")
        if not isinstance(tags, list):
            tags = []

        # Build default params based on tool type
        params: dict[str, Any] = {}

        if "metrics" in tags:
            # Default metrics query
            promql = "sum(up)"
            if service:
                promql = f'sum(up{{job="{service}"}})'
            if namespace:
                promql = f'sum(up{{namespace="{namespace}"}})'

            params = {
                "promql": promql,
                "step_seconds": 30,
            }

            item = ToolCallItem(
                tool=name,
                params=params,
                query_type="metrics",
                purpose="Query metrics for anomaly analysis",
            )
            idx = plan.add_item(item)
            parallel_indices.append(idx)

        elif "logs" in tags:
            # Default logs query
            query = '{job=~".+"} |= "error"'
            if service:
                query = f'{{job="{service}"}} |= "error"'
            if namespace:
                query = f'{{namespace="{namespace}"}} |= "error"'

            params = {
                "query": query,
                "limit": 200,
            }

            item = ToolCallItem(
                tool=name,
                params=params,
                query_type="logs",
                purpose="Search logs for error messages",
            )
            idx = plan.add_item(item)
            parallel_indices.append(idx)

    # Set up parallel execution for collected items
    if parallel_indices:
        # Split into groups if too many items
        for i in range(0, len(parallel_indices), max_parallel):
            group = parallel_indices[i : i + max_parallel]
            plan.add_parallel_group(group)

    return plan


def tool_call_plan_from_state(state: Any) -> ToolCallPlan | None:
    """Extract ToolCallPlan from a GraphState object.

    Args:
        state: GraphState object with tool_call_plan attribute.

    Returns:
        ToolCallPlan if available, None otherwise.
    """
    plan_data = getattr(state, "tool_call_plan", None)
    if not isinstance(plan_data, dict) or not plan_data:
        return None

    return ToolCallPlan.from_dict(plan_data)