from __future__ import annotations

import pathlib
import sys
import unittest


TESTS_DIR = pathlib.Path(__file__).resolve().parent
PROJECT_DIR = TESTS_DIR.parent
if str(PROJECT_DIR) not in sys.path:
    sys.path.insert(0, str(PROJECT_DIR))

from orchestrator.daemon import runner as runner_module
from orchestrator.sdk.errors import OrchestratorErrorCategory, RCAApiError
from orchestrator.state import GraphState


class SessionPatchRetryTest(unittest.TestCase):
    def test_apply_session_patch_retries_once_on_revision_conflict(self) -> None:
        class _FakeRuntime:
            def __init__(self) -> None:
                self.patch_calls: list[dict[str, object]] = []
                self.snapshot_reads = 0

            def patch_job_session_context(self, **kwargs: object) -> dict[str, object]:
                self.patch_calls.append(dict(kwargs))
                if len(self.patch_calls) == 1:
                    raise RCAApiError(
                        category=OrchestratorErrorCategory.OWNER_LOST,
                        message="PATCH /v1/ai/jobs/job-1/session-context failed",
                        method="PATCH",
                        path="/v1/ai/jobs/job-1/session-context",
                        http_status=409,
                        envelope_code="Conflict.SessionContextRevisionConflict",
                        envelope_message="revision mismatch",
                    )
                return {
                    "session_id": "session-1",
                    "session_revision": "rev-3",
                    "latest_summary": {"summary": "prompt enriched"},
                    "context_state": {"skills": {"diagnosis_enrich": {"applied": True}}},
                    "pinned_evidence": [],
                }

            def get_job_session_context(self) -> dict[str, object]:
                self.snapshot_reads += 1
                return {
                    "session_id": "session-1",
                    "session_revision": "rev-2",
                    "latest_summary": {"summary": "native finalized"},
                    "context_state": {},
                    "pinned_evidence": [],
                }

        state = GraphState(
            job_id="job-1",
            instance_id="orc-1",
            session_id="session-1",
            session_snapshot={"session_id": "session-1", "session_revision": "rev-1"},
            session_patch={
                "latest_summary": {"summary": "prompt enriched"},
                "context_state_patch": {"skills": {"diagnosis_enrich": {"applied": True}}},
                "actor": "skill:claude.diagnosis.prompt_enricher",
                "source": "skill.prompt",
            },
        )
        runtime = _FakeRuntime()

        out = runner_module._apply_session_patch_if_needed(runtime, state)

        self.assertIs(out, state)
        self.assertEqual(runtime.snapshot_reads, 1)
        self.assertEqual(len(runtime.patch_calls), 2)
        self.assertEqual(runtime.patch_calls[0]["session_revision"], "rev-1")
        self.assertEqual(runtime.patch_calls[1]["session_revision"], "rev-2")
        self.assertEqual(state.session_snapshot["session_revision"], "rev-3")
        self.assertTrue(state.session_context["skills"]["diagnosis_enrich"]["applied"])
        self.assertEqual(state.latest_summary["summary"], "prompt enriched")


if __name__ == "__main__":
    unittest.main()
