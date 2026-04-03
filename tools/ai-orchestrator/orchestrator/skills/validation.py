"""Unified validation for capability outputs."""
from __future__ import annotations

from dataclasses import dataclass, field
from enum import Enum
from typing import Any

try:
    import jsonschema
    from jsonschema import ValidationError as JsonSchemaValidationError
    HAS_JSONSCHEMA = True
except ImportError:
    HAS_JSONSCHEMA = False
    JsonSchemaValidationError = Exception  # type: ignore[misc,assignment]

from .schemas import get_schema


class ValidationErrorKind(str, Enum):
    """Structured error kinds for validation failures."""
    INVALID_JSON = "invalid_json"
    SCHEMA_VIOLATION = "schema_violation"
    MISSING_REQUIRED_FIELD = "missing_required_field"
    TYPE_MISMATCH = "type_mismatch"
    VALUE_CONSTRAINT = "value_constraint"
    UNKNOWN_FIELD = "unknown_field"


@dataclass
class ValidationError:
    """Single validation error."""
    kind: ValidationErrorKind
    path: str  # JSON path, e.g., "tool_call_plan.items[0].tool"
    message: str
    value: Any = None  # Optional: the invalid value

    def to_dict(self) -> dict[str, Any]:
        result: dict[str, Any] = {
            "kind": self.kind.value,
            "path": self.path,
            "message": self.message,
        }
        if self.value is not None:
            result["value"] = self.value
        return result


@dataclass
class ValidationResult:
    """Result of validating a capability output."""
    is_valid: bool
    errors: list[ValidationError] = field(default_factory=list)
    normalized_payload: dict[str, Any] = field(default_factory=dict)

    @property
    def error_messages(self) -> list[str]:
        """Get human-readable error messages."""
        return [f"{e.path}: {e.message}" for e in self.errors]

    def to_dict(self) -> dict[str, Any]:
        return {
            "is_valid": self.is_valid,
            "errors": [e.to_dict() for e in self.errors],
            "error_messages": self.error_messages,
        }


def validate_capability_output(
    capability: str,
    output: dict[str, Any],
    *,
    strict: bool = False,
) -> ValidationResult:
    """Validate a capability output against its JSON Schema.

    Args:
        capability: The capability name (e.g., "tool.plan").
        output: The output payload to validate.
        strict: If True, reject unknown fields; if False, allow them.

    Returns:
        ValidationResult with validation status and errors.
    """
    schema = get_schema(capability)
    if schema is None:
        # No schema defined, pass through
        return ValidationResult(is_valid=True, normalized_payload=output)

    if not isinstance(output, dict):
        return ValidationResult(
            is_valid=False,
            errors=[
                ValidationError(
                    kind=ValidationErrorKind.TYPE_MISMATCH,
                    path="",
                    message=f"Expected object, got {type(output).__name__}",
                )
            ],
        )

    if not HAS_JSONSCHEMA:
        # Fallback to basic validation without jsonschema library
        return _validate_basic(schema, output, strict=strict)

    errors: list[ValidationError] = []

    try:
        jsonschema.validate(output, schema)
    except JsonSchemaValidationError as e:
        errors.append(_convert_schema_error(e))

    if errors:
        return ValidationResult(is_valid=False, errors=errors)

    # If strict mode, check for unknown fields
    if strict:
        unknown_errors = _check_unknown_fields(schema, output, "")
        errors.extend(unknown_errors)
        if errors:
            return ValidationResult(is_valid=False, errors=errors)

    return ValidationResult(is_valid=True, normalized_payload=output)


def _convert_schema_error(error: JsonSchemaValidationError) -> ValidationError:
    """Convert jsonschema error to our ValidationError."""
    path = ".".join(str(p) for p in error.absolute_path) if error.absolute_path else ""

    kind = ValidationErrorKind.SCHEMA_VIOLATION
    if "required" in error.message.lower() or error.validator == "required":
        kind = ValidationErrorKind.MISSING_REQUIRED_FIELD
    elif error.validator in ("type",):
        kind = ValidationErrorKind.TYPE_MISMATCH
    elif error.validator in ("minLength", "maxLength", "minimum", "maximum", "pattern", "minItems"):
        kind = ValidationErrorKind.VALUE_CONSTRAINT

    return ValidationError(
        kind=kind,
        path=path,
        message=error.message,
        value=error.instance if isinstance(error.instance, (str, int, float, bool, type(None))) else None,
    )


def _validate_basic(schema: dict[str, Any], output: dict[str, Any], *, strict: bool) -> ValidationResult:
    """Basic validation without jsonschema library."""
    errors: list[ValidationError] = []

    # Check required fields
    required = schema.get("required", [])
    for field_name in required:
        if field_name not in output:
            errors.append(
                ValidationError(
                    kind=ValidationErrorKind.MISSING_REQUIRED_FIELD,
                    path=field_name,
                    message=f"Required field '{field_name}' is missing",
                )
            )

    # Check type of properties
    properties = schema.get("properties", {})
    for prop_name, prop_schema in properties.items():
        if prop_name not in output:
            continue
        value = output[prop_name]
        prop_errors = _validate_property(prop_name, prop_schema, value)
        errors.extend(prop_errors)

    return ValidationResult(
        is_valid=len(errors) == 0,
        errors=errors,
        normalized_payload=output if not errors else {},
    )


def _validate_property(path: str, schema: dict[str, Any], value: Any) -> list[ValidationError]:
    """Validate a single property against its schema."""
    errors: list[ValidationError] = []
    expected_type = schema.get("type")

    if expected_type and not _check_type(value, expected_type):
        errors.append(
            ValidationError(
                kind=ValidationErrorKind.TYPE_MISMATCH,
                path=path,
                message=f"Expected {expected_type}, got {type(value).__name__}",
            )
        )
        return errors

    # Check required fields in nested objects
    if expected_type == "object" and isinstance(value, dict):
        required = schema.get("required", [])
        for field_name in required:
            if field_name not in value:
                errors.append(
                    ValidationError(
                        kind=ValidationErrorKind.MISSING_REQUIRED_FIELD,
                        path=f"{path}.{field_name}",
                        message=f"Required field '{field_name}' is missing",
                    )
                )

        # Recursively validate properties
        properties = schema.get("properties", {})
        for prop_name, prop_schema in properties.items():
            if prop_name in value:
                prop_errors = _validate_property(f"{path}.{prop_name}", prop_schema, value[prop_name])
                errors.extend(prop_errors)

    # Check array items
    if expected_type == "array" and isinstance(value, list):
        items_schema = schema.get("items")
        if isinstance(items_schema, dict):
            for idx, item in enumerate(value):
                item_errors = _validate_property(f"{path}[{idx}]", items_schema, item)
                errors.extend(item_errors)

        # Check minItems
        min_items = schema.get("minItems")
        if isinstance(min_items, int) and len(value) < min_items:
            errors.append(
                ValidationError(
                    kind=ValidationErrorKind.VALUE_CONSTRAINT,
                    path=path,
                    message=f"Array must have at least {min_items} items, got {len(value)}",
                )
            )

    # Check minLength for strings
    if expected_type == "string" and isinstance(value, str):
        min_length = schema.get("minLength")
        if isinstance(min_length, int) and len(value) < min_length:
            errors.append(
                ValidationError(
                    kind=ValidationErrorKind.VALUE_CONSTRAINT,
                    path=path,
                    message=f"String must have at least {min_length} characters, got {len(value)}",
                )
            )

    return errors


def _check_type(value: Any, expected_type: str) -> bool:
    """Check if value matches expected JSON type."""
    type_map: dict[str, type | tuple[type, ...]] = {
        "string": str,
        "number": (int, float),
        "integer": int,
        "boolean": bool,
        "object": dict,
        "array": list,
        "null": type(None),
    }
    expected = type_map.get(expected_type)
    if expected is None:
        return True  # Unknown type, allow
    return isinstance(value, expected)


def _check_unknown_fields(
    schema: dict[str, Any],
    obj: dict[str, Any],
    path_prefix: str,
) -> list[ValidationError]:
    """Check for fields not defined in schema."""
    errors: list[ValidationError] = []
    properties = schema.get("properties", {}).keys()

    for key in obj.keys():
        if key not in properties:
            full_path = f"{path_prefix}.{key}" if path_prefix else key
            errors.append(
                ValidationError(
                    kind=ValidationErrorKind.UNKNOWN_FIELD,
                    path=full_path,
                    message=f"Unknown field '{key}'",
                )
            )

    return errors