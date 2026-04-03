"""Tests for capability output validation."""
import unittest

from orchestrator.skills.validation import (
    ValidationError,
    ValidationErrorKind,
    ValidationResult,
    validate_capability_output,
)
from orchestrator.skills.schemas import get_schema, CAPABILITY_SCHEMAS


class TestCapabilitySchemas(unittest.TestCase):
    """Tests for JSON Schema definitions."""

    def test_schemas_registered(self):
        """All expected capabilities have schemas registered."""
        expected = {"tool.plan", "evidence.plan", "diagnosis.enrich"}
        self.assertEqual(set(CAPABILITY_SCHEMAS.keys()), expected)

    def test_get_schema_returns_none_for_unknown(self):
        """Unknown capability returns None."""
        self.assertIsNone(get_schema("unknown.capability"))

    def test_tool_plan_schema_structure(self):
        """tool.plan schema has required structure."""
        schema = get_schema("tool.plan")
        self.assertIsNotNone(schema)
        self.assertIn("tool_call_plan", schema.get("required", []))
        props = schema.get("properties", {}).get("tool_call_plan", {}).get("properties", {})
        self.assertIn("items", props)


class TestToolPlanValidation(unittest.TestCase):
    """Tests for tool.plan capability validation."""

    def test_valid_output(self):
        """Valid tool.plan output passes validation."""
        output = {
            "tool_call_plan": {
                "items": [
                    {"tool": "prometheus_query", "params": {"query": "up"}}
                ]
            }
        }
        result = validate_capability_output("tool.plan", output)
        self.assertTrue(result.is_valid)
        self.assertEqual(len(result.errors), 0)

    def test_missing_tool_field(self):
        """Missing required 'tool' field fails validation."""
        output = {
            "tool_call_plan": {
                "items": [{"params": {}}]  # Missing 'tool'
            }
        }
        result = validate_capability_output("tool.plan", output)
        self.assertFalse(result.is_valid)
        self.assertTrue(any(e.kind == ValidationErrorKind.MISSING_REQUIRED_FIELD for e in result.errors))

    def test_empty_items_array(self):
        """Empty items array fails validation."""
        output = {
            "tool_call_plan": {
                "items": []
            }
        }
        result = validate_capability_output("tool.plan", output)
        self.assertFalse(result.is_valid)
        self.assertTrue(any(e.kind == ValidationErrorKind.VALUE_CONSTRAINT for e in result.errors))

    def test_missing_tool_call_plan(self):
        """Missing tool_call_plan fails validation."""
        output = {"other": "data"}
        result = validate_capability_output("tool.plan", output)
        self.assertFalse(result.is_valid)
        self.assertTrue(any(e.kind == ValidationErrorKind.MISSING_REQUIRED_FIELD for e in result.errors))

    def test_tool_is_empty_string(self):
        """Empty tool string fails validation."""
        output = {
            "tool_call_plan": {
                "items": [{"tool": ""}]
            }
        }
        result = validate_capability_output("tool.plan", output)
        self.assertFalse(result.is_valid)
        self.assertTrue(any(e.kind == ValidationErrorKind.VALUE_CONSTRAINT for e in result.errors))

    def test_with_optional_fields(self):
        """Output with optional fields passes validation."""
        output = {
            "tool_call_plan": {
                "items": [
                    {
                        "tool": "prometheus_query",
                        "params": {"query": "up"},
                        "query_type": "metrics",
                        "purpose": "Check service health",
                        "evidence_kind": "query",
                        "optional": True,
                        "depends_on": ["call_1"],
                        "call_id": "call_2",
                    }
                ],
                "parallel_groups": [[0]]
            }
        }
        result = validate_capability_output("tool.plan", output)
        self.assertTrue(result.is_valid)

    def test_parallel_groups_with_integer_arrays(self):
        """Parallel groups with integer arrays passes validation."""
        output = {
            "tool_call_plan": {
                "items": [
                    {"tool": "tool1"},
                    {"tool": "tool2"},
                ],
                "parallel_groups": [[0, 1]]
            }
        }
        result = validate_capability_output("tool.plan", output)
        self.assertTrue(result.is_valid)


class TestEvidencePlanValidation(unittest.TestCase):
    """Tests for evidence.plan capability validation."""

    def test_valid_output(self):
        """Valid evidence.plan output passes validation."""
        output = {
            "evidence_plan_patch": {"key": "value"},
            "evidence_candidates": [{"id": "1"}],
            "metrics_branch_meta": {"mode": "query"},
            "logs_branch_meta": {"mode": "query"},
        }
        result = validate_capability_output("evidence.plan", output)
        self.assertTrue(result.is_valid)

    def test_empty_output_is_valid(self):
        """Empty output passes validation (all fields optional)."""
        output = {}
        result = validate_capability_output("evidence.plan", output)
        self.assertTrue(result.is_valid)

    def test_evidence_candidates_with_objects(self):
        """Evidence candidates with object items passes validation."""
        output = {
            "evidence_candidates": [
                {"id": "1", "type": "metrics"},
                {"id": "2", "type": "logs"},
            ]
        }
        result = validate_capability_output("evidence.plan", output)
        self.assertTrue(result.is_valid)


class TestDiagnosisValidation(unittest.TestCase):
    """Tests for diagnosis.enrich capability validation."""

    def test_valid_patch(self):
        """Valid diagnosis.enrich output passes."""
        output = {
            "diagnosis_patch": {
                "summary": "Test summary",
                "root_cause": {"statement": "Test statement"}
            }
        }
        result = validate_capability_output("diagnosis.enrich", output)
        self.assertTrue(result.is_valid)

    def test_empty_output_is_valid(self):
        """Empty output passes validation."""
        output = {}
        result = validate_capability_output("diagnosis.enrich", output)
        self.assertTrue(result.is_valid)

    def test_full_diagnosis_patch(self):
        """Full diagnosis patch with all fields passes."""
        output = {
            "diagnosis_patch": {
                "summary": "Test summary",
                "root_cause": {
                    "summary": "Root cause summary",
                    "statement": "Root cause statement",
                },
                "recommendations": [{"text": "Fix it"}],
                "unknowns": ["Unknown factor"],
                "next_steps": ["Investigate further"],
            }
        }
        result = validate_capability_output("diagnosis.enrich", output)
        self.assertTrue(result.is_valid)


class TestUnknownCapabilityValidation(unittest.TestCase):
    """Tests for unknown capability handling."""

    def test_unknown_capability_passes(self):
        """Unknown capability passes through without validation."""
        output = {"any": "data"}
        result = validate_capability_output("unknown.capability", output)
        self.assertTrue(result.is_valid)
        self.assertEqual(result.normalized_payload, output)


class TestNonDictInput(unittest.TestCase):
    """Tests for non-dict input handling."""

    def test_non_dict_input_fails(self):
        """Non-dict input fails with TYPE_MISMATCH."""
        result = validate_capability_output("tool.plan", "not a dict")
        self.assertFalse(result.is_valid)
        self.assertTrue(any(e.kind == ValidationErrorKind.TYPE_MISMATCH for e in result.errors))

    def test_list_input_fails(self):
        """List input fails with TYPE_MISMATCH."""
        result = validate_capability_output("tool.plan", [{"tool": "test"}])
        self.assertFalse(result.is_valid)
        self.assertEqual(len(result.errors), 1)
        self.assertEqual(result.errors[0].kind, ValidationErrorKind.TYPE_MISMATCH)

    def test_none_input_fails(self):
        """None input fails with TYPE_MISMATCH."""
        result = validate_capability_output("tool.plan", None)
        self.assertFalse(result.is_valid)


class TestValidationResult(unittest.TestCase):
    """Tests for ValidationResult dataclass."""

    def test_error_messages_property(self):
        """error_messages property returns formatted messages."""
        errors = [
            ValidationError(
                kind=ValidationErrorKind.MISSING_REQUIRED_FIELD,
                path="tool_call_plan.items[0].tool",
                message="Required field 'tool' is missing",
            )
        ]
        result = ValidationResult(is_valid=False, errors=errors)
        self.assertEqual(result.error_messages, ["tool_call_plan.items[0].tool: Required field 'tool' is missing"])

    def test_to_dict(self):
        """to_dict returns proper structure."""
        errors = [
            ValidationError(
                kind=ValidationErrorKind.TYPE_MISMATCH,
                path="field",
                message="Expected string, got int",
                value=123,
            )
        ]
        result = ValidationResult(is_valid=False, errors=errors, normalized_payload={})
        d = result.to_dict()
        self.assertFalse(d["is_valid"])
        self.assertEqual(len(d["errors"]), 1)
        self.assertEqual(d["errors"][0]["kind"], "type_mismatch")
        self.assertEqual(d["errors"][0]["value"], 123)


class TestStrictMode(unittest.TestCase):
    """Tests for strict mode validation."""

    def test_strict_mode_rejects_unknown_fields(self):
        """Strict mode rejects unknown fields."""
        output = {
            "tool_call_plan": {
                "items": [{"tool": "test"}]
            },
            "unknown_field": "value"
        }
        result = validate_capability_output("tool.plan", output, strict=True)
        self.assertFalse(result.is_valid)
        self.assertTrue(any(e.kind == ValidationErrorKind.UNKNOWN_FIELD for e in result.errors))

    def test_non_strict_allows_unknown_fields(self):
        """Non-strict mode allows unknown fields."""
        output = {
            "tool_call_plan": {
                "items": [{"tool": "test"}]
            },
            "unknown_field": "value"
        }
        result = validate_capability_output("tool.plan", output, strict=False)
        self.assertTrue(result.is_valid)


if __name__ == "__main__":
    unittest.main()