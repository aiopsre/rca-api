from __future__ import annotations

import pathlib
import sys
import unittest


TESTS_DIR = pathlib.Path(__file__).resolve().parent
PROJECT_DIR = TESTS_DIR.parent
if str(PROJECT_DIR) not in sys.path:
    sys.path.insert(0, str(PROJECT_DIR))

from orchestrator.evidence_plan import BudgetTracker, build_candidates, rank_candidates


class EvidencePlanRankingTest(unittest.TestCase):
    def test_ranking_is_stable_and_explainable(self) -> None:
        context = {"service": "checkout", "namespace": "prod", "severity": "P1"}
        ranked_once = rank_candidates(build_candidates(context), context)
        ranked_twice = rank_candidates(build_candidates(context), context)

        self.assertGreaterEqual(len(ranked_once), 2)
        self.assertEqual(
            [item.name for item in ranked_once],
            [item.name for item in ranked_twice],
        )
        self.assertEqual(ranked_once[0].name, "query_metrics:apiserver_5xx_rate")
        for item in ranked_once:
            self.assertGreater(len(item.reasons), 0)
            self.assertIsInstance(float(item.score), float)


class BudgetTrackerTest(unittest.TestCase):
    def test_budget_tracker_accumulates_and_stops_at_thresholds(self) -> None:
        tracker = BudgetTracker(max_calls=2, max_total_bytes=100, max_total_latency_ms=1000)
        self.assertTrue(tracker.can_execute_query())

        tracker.record_query(result_bytes=40, latency_ms=100)
        self.assertEqual(tracker.used_snapshot()["calls"], 1)
        self.assertEqual(tracker.used_snapshot()["total_bytes"], 40)
        self.assertEqual(tracker.used_snapshot()["total_latency_ms"], 100)
        self.assertTrue(tracker.can_execute_query())

        tracker.record_query(result_bytes=30, latency_ms=150)
        self.assertEqual(tracker.used_snapshot()["calls"], 2)
        self.assertFalse(tracker.can_execute_query())

        bytes_limited = BudgetTracker(max_calls=10, max_total_bytes=50, max_total_latency_ms=1000)
        bytes_limited.record_query(result_bytes=55, latency_ms=1)
        self.assertFalse(bytes_limited.can_execute_query())

        latency_limited = BudgetTracker(max_calls=10, max_total_bytes=500, max_total_latency_ms=20)
        latency_limited.record_query(result_bytes=1, latency_ms=25)
        self.assertFalse(latency_limited.can_execute_query())


if __name__ == "__main__":
    unittest.main()
