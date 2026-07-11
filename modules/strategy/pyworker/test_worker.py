import importlib.util
from pathlib import Path
import pytest

def test_example_strategy_exports_run():
    path = Path(__file__).parents[1] / "strategies" / "example" / "strategy.py"
    spec = importlib.util.spec_from_file_location("example_strategy", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    result = module.run({}, [], {}, {})
    assert result["action"] == "hold"
    assert result["next_state"] == {}

def test_validate_result_rejects_duplicate_or_non_decimal_targets():
    spec = importlib.util.spec_from_file_location("strategy_worker", Path(__file__).with_name("worker.py"))
    worker = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(worker)
    with pytest.raises(ValueError):
        worker.validate_result({"action": "rebalance", "targets": [{"instrument_id": "BTC", "target_weight": "x"}, {"instrument_id": "BTC", "target_weight": "1"}], "next_state": {}})

def test_run_converts_rows_to_dataframe_and_preserves_context():
    spec = importlib.util.spec_from_file_location("strategy_worker", Path(__file__).with_name("worker.py"))
    worker = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(worker)
    worker.modules[("demo", "hash")] = (type("M", (), {})(), lambda context, data, params, state: {
        "action": "hold" if data.loc[0, "symbol"] == "BTC-USDT" and context["strategy_id"] == "demo" else "rebalance",
        "next_state": {"revision": context["state_revision"]},
    })
    out = worker.run({"logical_id": "demo", "source_hash": "hash", "context": {"strategy_id": "demo", "state_revision": 2}, "data": [{"symbol": "BTC-USDT"}]})
    assert out["action"] == "hold"
