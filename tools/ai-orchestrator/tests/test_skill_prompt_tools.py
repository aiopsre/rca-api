from __future__ import annotations

import pathlib
import sys
import unittest


TESTS_DIR = pathlib.Path(__file__).resolve().parent
PROJECT_DIR = TESTS_DIR.parent
if str(PROJECT_DIR) not in sys.path:
    sys.path.insert(0, str(PROJECT_DIR))

from orchestrator.skills.agent import _augment_openai_tools_for_skill_document


class SkillPromptToolAugmentationTest(unittest.TestCase):
    def test_augment_tempo_trace_only_from_skill_document(self) -> None:
        tools = [
            {
                "type": "function",
                "function": {
                    "name": "tempo_get_trace",
                    "description": "",
                    "parameters": {
                        "type": "object",
                        "properties": {
                            "trace_id": {"type": "string"},
                        },
                        "required": ["trace_id"],
                    },
                },
            },
        ]
        skill_document = """\
---
name: Tempo Trace Skill
description: Prompt-driven Tempo trace analysis skill for RCA; use Tempo MCP tools to fetch and interpret traces.
compatibility: Prompt-driven skill. Do not use scripts/executor.py. Use tempo_get_trace only.
---
"""

        augmented = _augment_openai_tools_for_skill_document(tools, skill_document)

        tempo_get_trace = next(item for item in augmented if item["function"]["name"] == "tempo_get_trace")

        self.assertEqual(tempo_get_trace["function"]["parameters"]["required"], ["trace_id"])
        self.assertIn("trace ID", tempo_get_trace["function"]["description"])


if __name__ == "__main__":
    unittest.main()
