import importlib.util
from pathlib import Path
import unittest


def load_worker():
    spec = importlib.util.spec_from_file_location(
        "strategy_worker", Path(__file__).with_name("worker.py")
    )
    worker = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(worker)
    return worker


class WorkerTest(unittest.TestCase):
    def test_example_strategy_exports_run(self):
        path = Path(__file__).parents[1] / "strategies" / "example" / "strategy.py"
        spec = importlib.util.spec_from_file_location("example_strategy", path)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        result = module.run({}, [], {}, {})
        self.assertEqual(result["action"], "hold")
        self.assertEqual(result["next_state"], {})

    def test_validate_result_rejects_invalid_target(self):
        worker = load_worker()
        for target in (
            {"instrument_id": "BTC", "symbol": "BTCUSDT", "target_weight": "1"},
            {"instrument_id": " ", "symbol": "BTCUSDT", "target_quantity": "1"},
            {"instrument_id": "BTC-USDT", "symbol": "\t", "target_quantity": "1"},
            *[
                {"instrument_id": "BTC", "symbol": "BTCUSDT", "target_quantity": quantity}
                for quantity in ("+1", ".5", "1.", "01", "1e3", "1/2", "NaN", "Inf", " 1", "9" * 257)
            ],
        ):
            with self.subTest(target=target), self.assertRaises(ValueError):
                worker.validate_result({
                    "action": "rebalance",
                    "targets": [target],
                    "next_state": {},
                })

    def test_validate_result_rejects_empty_rebalance(self):
        worker = load_worker()
        with self.assertRaises(ValueError):
            worker.validate_result({
                "action": "rebalance",
                "targets": [],
                "next_state": {},
            })

    def test_run_converts_rows_to_dataframe_and_preserves_context(self):
        worker = load_worker()
        worker.modules[("demo", "hash")] = (
            type("M", (), {})(),
            lambda context, data, params, state: {
                "action": (
                    "hold"
                    if data.loc[0, "symbol"] == "BTC-USDT"
                    and context["strategy_id"] == "demo"
                    else "rebalance"
                ),
                "next_state": {"revision": context["state_revision"]},
            },
        )
        result = worker.run({
            "logical_id": "demo",
            "source_hash": "hash",
            "context": {"strategy_id": "demo", "state_revision": 2},
            "data": [{"symbol": "BTC-USDT"}],
        })
        self.assertEqual(result["action"], "hold")


if __name__ == "__main__":
    unittest.main()
