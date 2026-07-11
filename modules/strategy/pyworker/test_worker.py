import importlib.util
from pathlib import Path

def test_example_strategy_exports_run():
    path = Path(__file__).parents[1] / "strategies" / "example" / "strategy.py"
    spec = importlib.util.spec_from_file_location("example_strategy", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    result = module.run({}, [], {}, {})
    assert result["action"] == "hold"
    assert result["next_state"] == {}
