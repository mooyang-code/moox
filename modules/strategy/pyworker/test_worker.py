import importlib.util
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


def load_worker():
    spec = importlib.util.spec_from_file_location(
        "strategy_worker", Path(__file__).with_name("worker.py")
    )
    worker = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(worker)
    return worker


class WorkerTest(unittest.TestCase):
    def test_user_output_is_bounded_and_cannot_corrupt_protocol_frames(self):
        worker = load_worker()
        with tempfile.TemporaryDirectory() as root:
            source = Path(root) / "strategy.py"
            source.write_text(
                "print('import-log')\n"
                "def run(context, data, params):\n"
                "    print('x' * 100000)\n"
                "    print('run-error-log', file=__import__('sys').stderr)\n"
                "    return {'action': 'hold', 'targets': []}\n",
                encoding="utf-8",
            )
            with subprocess.Popen(
                [sys.executable, str(Path(__file__).with_name("worker.py"))],
                stdin=subprocess.PIPE,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            ) as process:
                typ, _, _ = worker.read_frame(process.stdout)
                self.assertEqual(typ, worker.HELLO)

                worker.write_frame(process.stdin, worker.LOAD, {
                    "path": str(source),
                    "source_hash": "print-source",
                    "logical_id": "print-source",
                    "entrypoint": "strategy.py:run",
                })
                typ, loaded, _ = worker.read_frame(process.stdout)
                self.assertEqual(typ, worker.RESULT)
                self.assertIn("import-log", loaded["logs"]["stdout"])

                worker.write_frame(process.stdin, worker.RUN, {
                    "request_id": "print-run",
                    "logical_id": "print-source",
                    "source_hash": "print-source",
                    "context": {},
                    "data": [],
                    "params": {},
                })
                typ, result, _ = worker.read_frame(process.stdout)
                self.assertEqual(typ, worker.RESULT)
                self.assertEqual(result["result"]["action"], "hold")
                self.assertTrue(result["logs"]["truncated"])
                self.assertLessEqual(
                    len(result["logs"]["stdout"].encode("utf-8")),
                    64 * 1024,
                )
                self.assertIn("run-error-log", result["logs"]["stderr"])

                worker.write_frame(process.stdin, worker.TYPE_DRAIN, {})
                process.wait(timeout=5)

    def test_example_strategy_exports_three_argument_run(self):
        path = Path(__file__).parents[1] / "strategies" / "example" / "strategy.py"
        spec = importlib.util.spec_from_file_location("example_strategy", path)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        result = module.run({}, [], {})
        self.assertEqual(result, {"action": "hold", "targets": []})

    def test_accepts_empty_rebalance(self):
        worker = load_worker()
        self.assertEqual(
            worker.validate_result({"action": "rebalance", "targets": []}),
            {"action": "rebalance", "targets": []},
        )

    def test_rejects_target_quantity_alias(self):
        worker = load_worker()
        with self.assertRaises(ValueError):
            worker.validate_result({
                "action": "rebalance",
                "targets": [{
                    "instrument_id": "BTC-USDT-SPOT",
                    "target_quantity": "1",
                }],
            })

    def test_passes_complete_history_without_previous_targets(self):
        worker = load_worker()
        captured = {}

        def strategy(context, data, params):
            captured["context"] = context
            captured["rows"] = data.to_dict("records")
            captured["params"] = params
            return {"action": "hold", "targets": []}

        worker.modules[("demo", "hash")] = (object(), strategy)
        result = worker.run({
            "logical_id": "demo",
            "source_hash": "hash",
            "context": {
                "strategy_id": "demo",
                "runner_id": "runner-1",
                "trigger_bar_time": "2026-07-29T10:00:00Z",
            },
            "data": [
                {"time": "2026-07-29T09:58:00Z", "close": "1"},
                {"time": "2026-07-29T09:59:00Z", "close": "2"},
            ],
            "params": {"fast": 12},
        })
        self.assertEqual(result["action"], "hold")
        self.assertEqual(len(captured["rows"]), 2)
        self.assertNotIn("previous_targets", captured["context"])
        self.assertNotIn("state", captured["context"])

    def test_preserves_input_history_order(self):
        worker = load_worker()
        captured = []

        def strategy(context, data, params):
            captured.extend(data["time"].astype(str).tolist())
            return {"action": "hold", "targets": []}

        worker.modules[("order", "hash")] = (object(), strategy)
        worker.run({
            "logical_id": "order",
            "source_hash": "hash",
            "context": {},
            "data": [
                {"time": "2026-07-29T10:00:00Z"},
                {"time": "2026-07-29T09:59:00Z"},
            ],
            "params": {},
        })
        self.assertEqual(
            captured,
            ["2026-07-29 10:00:00+00:00", "2026-07-29 09:59:00+00:00"],
        )

    def test_rejects_four_argument_stateful_entrypoint(self):
        worker = load_worker()
        with tempfile.TemporaryDirectory() as root:
            source = Path(root) / "strategy.py"
            source.write_text(
                "def run(context, data, params, state):\n"
                "    return {'action': 'hold', 'targets': []}\n",
                encoding="utf-8",
            )
            with self.assertRaisesRegex(ValueError, "exactly context, data, params"):
                worker.load({
                    "path": str(source),
                    "source_hash": "stateful",
                    "logical_id": "stateful",
                    "entrypoint": "strategy.py:run",
                })


if __name__ == "__main__":
    unittest.main()
