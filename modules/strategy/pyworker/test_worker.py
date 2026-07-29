import importlib.util
from pathlib import Path
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
