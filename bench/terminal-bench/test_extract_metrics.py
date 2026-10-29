import json
import os
import sqlite3
import tempfile
import unittest

import extract_metrics as em


class TokenNormalizationTest(unittest.TestCase):
    def test_token_record_keeps_cache_separate(self):
        got = em.token_record(100, 20, cache_read=30, cache_write=5)
        self.assertEqual(
            got,
            {"input": 100, "output": 20, "reasoning": None,
             "cache_read": 30, "cache_write": 5},
        )

    def test_cost_uses_shared_pricing(self):
        pricing = {
            "models": {
                "deepseek-flash": {
                    "input": 1.0, "output": 2.0,
                    "cache_read": 0.1, "cache_write": 1.0,
                }
            }
        }
        tokens = em.token_record(1_000_000, 1_000_000, 1_000_000, 0)
        cost, key = em.compute_cost(tokens, "deepseek/deepseek-flash", pricing)
        self.assertEqual(key, "deepseek-flash")
        self.assertAlmostEqual(cost, 3.1)

    def test_pricing_period_uses_utc_peak_windows(self):
        self.assertEqual(
            em.pricing_period({"start_time": "2026-10-29T02:00:00+00:00"}),
            ("peak", "metadata"),
        )
        self.assertEqual(
            em.pricing_period({"start_time": "2026-10-29T11:00:00+00:00"}),
            ("off_peak", "metadata"),
        )
        # Saturday is off-peak all day.
        self.assertEqual(
            em.pricing_period({"start_time": "2026-10-31T02:00:00+00:00"}),
            ("off_peak", "metadata"),
        )
        self.assertEqual(em.pricing_period({}), ("off_peak", "assumed"))

    def test_yaah_usage_splits_cache_and_counts_steps(self):
        with tempfile.TemporaryDirectory() as root:
            home = os.path.join(root, "run", "hello-world", "trial", "agent-logs", "yaah-home")
            os.makedirs(home)
            db = sqlite3.connect(os.path.join(home, "state.db"))
            db.execute(
                "CREATE TABLE sessions (tokens_in INTEGER, tokens_out INTEGER, "
                "tokens_cached INTEGER, sub_tokens_in INTEGER, sub_tokens_out INTEGER)"
            )
            db.execute("CREATE TABLE messages (role TEXT)")
            db.execute("INSERT INTO sessions VALUES (1000, 100, 400, 10, 5)")
            db.executemany("INSERT INTO messages VALUES (?)", [("assistant",), ("assistant",), ("tool",)])
            db.commit()
            db.close()
            usage = em.yaah_usage(root, "run", "hello-world")
        self.assertEqual(usage["tokens"]["input"], 600)
        self.assertEqual(usage["tokens"]["cache_read"], 400)
        self.assertEqual(usage["tokens"]["output"], 100)
        self.assertEqual(usage["steps"], 2)
        self.assertEqual(usage["sub_tokens_in"], 10)

    def test_yaah_usage_keeps_cache_on_pre_subagent_schema(self):
        with tempfile.TemporaryDirectory() as root:
            home = os.path.join(root, "run", "hello-world", "trial", "agent-logs", "yaah-home")
            os.makedirs(home)
            db = sqlite3.connect(os.path.join(home, "state.db"))
            db.execute(
                "CREATE TABLE sessions (tokens_in INTEGER, tokens_out INTEGER, tokens_cached INTEGER)"
            )
            db.execute("CREATE TABLE messages (role TEXT)")
            db.execute("INSERT INTO sessions VALUES (1000, 100, 400)")
            db.commit()
            db.close()
            usage = em.yaah_usage(root, "run", "hello-world")
        self.assertEqual(usage["tokens"]["input"], 600)
        self.assertEqual(usage["tokens"]["cache_read"], 400)

    def test_malformed_pricing_degrades_cost(self):
        pricing = {"models": {"deepseek-flash": {"input": "not-a-number"}}}
        cost, key = em.compute_cost(
            em.token_record(100, 20), "deepseek-flash", pricing, "off_peak"
        )
        self.assertIsNone(cost)
        self.assertEqual(key, "deepseek-flash")

    def test_opencode_usage_sums_step_finish_events(self):
        with tempfile.TemporaryDirectory() as root:
            task_dir = os.path.join(root, "run", "hello-world", "trial", "agent-logs")
            os.makedirs(task_dir)
            events = [
                {"type": "step_finish", "sessionID": "ses_test", "part": {
                    "tokens": {"input": 100, "output": 20, "reasoning": 5,
                               "cache": {"read": 30, "write": 0}},
                    "cost": 0.1,
                }},
                {"type": "step_finish", "sessionID": "ses_test", "part": {
                    "tokens": {"input": 10, "output": 2, "reasoning": 0,
                               "cache": {"read": 0, "write": 0}},
                    "cost": 0.02,
                }},
            ]
            with open(os.path.join(task_dir, "opencode-stdout.jsonl"), "w") as f:
                for event in events:
                    f.write(json.dumps(event) + "\n")
            with open(os.path.join(task_dir, "opencode-session.txt"), "w") as f:
                f.write("ses_test")
            usage = em.opencode_usage(root, "run", "hello-world")
        self.assertEqual(usage["steps"], 2)
        self.assertEqual(usage["tokens"]["input"], 110)
        self.assertEqual(usage["tokens"]["output"], 22)
        self.assertEqual(usage["tokens"]["reasoning"], 5)
        self.assertEqual(usage["tokens"]["cache_read"], 30)

    def test_opencode_db_fallback_tolerates_nulls(self):
        with tempfile.TemporaryDirectory() as root:
            task_dir = os.path.join(root, "run", "hello-world", "trial", "agent-logs")
            os.makedirs(os.path.join(task_dir, "opencode-data", "opencode"))
            with open(os.path.join(task_dir, "opencode-session.txt"), "w") as f:
                f.write("ses_root")
            db = sqlite3.connect(
                os.path.join(task_dir, "opencode-data", "opencode", "opencode.db")
            )
            db.execute(
                "CREATE TABLE session_v2 (id TEXT, parent_id TEXT, "
                "tokens_input INTEGER, tokens_output INTEGER, "
                "tokens_reasoning INTEGER, tokens_cache_read INTEGER, "
                "tokens_cache_write INTEGER, cost REAL)"
            )
            db.execute("INSERT INTO session_v2 VALUES ('ses_root', NULL, NULL, NULL, NULL, NULL, NULL, NULL)")
            db.commit()
            db.close()
            usage = em.opencode_usage(root, "run", "hello-world")
        self.assertEqual(usage["tokens"]["input"], 0)
        self.assertEqual(usage["tokens_in"], 0)
        self.assertIsNone(usage["steps"])

    def test_usage_report_splits_cached_prompt(self):
        with tempfile.TemporaryDirectory() as root:
            task_dir = os.path.join(root, "run", "hello-world", "trial", "agent-logs")
            os.makedirs(task_dir)
            with open(os.path.join(task_dir, "hermes-usage.json"), "w") as f:
                json.dump({
                    "input_tokens": 1000,
                    "output_tokens": 100,
                    "cache_read_tokens": 400,
                    "cache_write_tokens": 50,
                    "prompt_includes_cache": True,
                    "api_calls": 3,
                }, f)
            usage = em.usage_report_tokens(root, "run", "hello-world")
        self.assertEqual(usage["tokens"]["input"], 600)
        self.assertEqual(usage["tokens"]["cache_read"], 400)
        self.assertEqual(usage["tokens"]["cache_write"], 50)
        self.assertEqual(usage["tokens_in"], 1000)
        self.assertEqual(usage["steps"], 3)

    def test_usage_report_keeps_zero_calls(self):
        with tempfile.TemporaryDirectory() as root:
            task_dir = os.path.join(root, "run", "hello-world", "trial", "agent-logs")
            os.makedirs(task_dir)
            with open(os.path.join(task_dir, "hermes-usage.json"), "w") as f:
                json.dump({"input_tokens": 10, "output_tokens": 1, "api_calls": 0}, f)
            usage = em.usage_report_tokens(root, "run", "hello-world")
        self.assertEqual(usage["steps"], 0)


if __name__ == "__main__":
    unittest.main()
