import io
import math
import subprocess
import sys
from pathlib import Path

import pandas as pd
import pytest

from codec import TYPE_HELLO, TYPE_RUN, _json_value, decode_json_df, read_frame, write_frame
from worker import FactorWorker


def test_frame_round_trip():
    stream = io.BytesIO()
    write_frame(stream, TYPE_RUN, {"id": "task-1", "encoding": "json"}, b"payload")
    stream.seek(0)
    frame_type, meta, payload = read_frame(stream)
    assert frame_type == TYPE_RUN
    assert meta["id"] == "task-1"
    assert payload == b"payload"


def test_worker_writes_json_only_ready_frame(tmp_path: Path):
    factors_dir = make_factor_dir(tmp_path)
    proc = subprocess.Popen(
        [sys.executable, str(Path(__file__).with_name("worker.py")), "--factors-dir", str(factors_dir)],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    try:
        frame_type, meta, payload = read_frame(proc.stdout)
        assert frame_type == TYPE_HELLO
        assert payload == b""
        assert meta["factors"] == ["Generic"]
        assert meta["encodings"] == ["json"]
    finally:
        proc.kill()
        proc.wait(timeout=5)


def test_decode_json_df_preserves_mixed_nanosecond_precision():
    df = decode_json_df({
        "df": {
            "columns": {"value": [1.0, None]},
            "data_times": ["2026-07-28T00:00:00Z", "2026-07-28T00:00:00.000000001Z"],
        }
    })
    assert str(df["data_time"].dtype) == "datetime64[ns, UTC]"
    assert df["data_time"].iloc[1] - df["data_time"].iloc[0] == pd.Timedelta(1, unit="ns")
    assert math.isnan(df["value"].iloc[1])


def test_execute_supports_multiple_outputs_and_half_open_target_range(tmp_path: Path):
    worker = loaded_worker(tmp_path)
    response = worker.execute_request(request_meta())
    assert response["results"] == {"double": [6.0], "triple": [9.0]}


@pytest.mark.parametrize(
    ("source", "outputs", "message"),
    [
        ("return {'double': df['value'] * 2}", ["double", "extra"], "outputs mismatch"),
        ("return {'double': df['value'] * 2, 'extra': df['value']}", ["double"], "outputs mismatch"),
        ("return {'double': df['value'].iloc[1:]}", ["double"], "must align"),
        ("return {'double': list(df['value'])}", ["double"], "pandas Series"),
    ],
)
def test_execute_rejects_missing_extra_misaligned_or_non_series(tmp_path, source, outputs, message):
    worker = loaded_worker(tmp_path, body=source)
    meta = request_meta()
    meta["factors"][0]["outputs"] = outputs
    with pytest.raises((TypeError, ValueError), match=message):
        worker.execute_request(meta)


def test_execute_rejects_non_object_params(tmp_path: Path):
    worker = loaded_worker(tmp_path)
    meta = request_meta()
    meta["factors"][0]["params"] = []
    with pytest.raises(TypeError, match="params must be an object"):
        worker.execute_request(meta)


def test_execute_rejects_duplicate_outputs_across_factors(tmp_path: Path):
    worker = loaded_worker(tmp_path)
    meta = request_meta()
    meta["factors"].append(dict(meta["factors"][0]))
    with pytest.raises(ValueError, match="duplicate factor output"):
        worker.execute_request(meta)


def test_execute_rejects_legacy_signal_only_module(tmp_path: Path):
    factors_dir = tmp_path / "factors"
    factors_dir.mkdir()
    (factors_dir / "Legacy.py").write_text(
        "def signal(df, n, factor_name):\n    return df\n", encoding="utf-8"
    )
    worker = FactorWorker(factors_dir)
    worker.load_modules()
    meta = request_meta()
    meta["factors"][0]["name"] = "Legacy"
    with pytest.raises(AttributeError, match=r"must define compute\(df, params\)"):
        worker.execute_request(meta)


def test_bad_factor_module_does_not_block_ready(tmp_path: Path):
    factors_dir = make_factor_dir(tmp_path)
    (factors_dir / "Broken.py").write_text("def compute(:\n", encoding="utf-8")
    worker = FactorWorker(factors_dir)
    worker.load_modules()
    assert list(worker.factors) == ["Generic"]
    assert "Broken" in worker.load_errors


def test_json_value_normalizes_nan_and_infinity():
    assert _json_value(float("nan")) is None
    assert _json_value(float("inf")) is None
    assert _json_value(float("-inf")) is None


def make_factor_dir(tmp_path: Path, body=None) -> Path:
    factors_dir = tmp_path / "factors"
    factors_dir.mkdir(exist_ok=True)
    if body is None:
        body = "return {'double': df['value'] * 2, 'triple': df['value'] * 3}"
    (factors_dir / "Generic.py").write_text(
        f"def compute(df, params):\n    {body}\n", encoding="utf-8"
    )
    return factors_dir


def loaded_worker(tmp_path: Path, body=None) -> FactorWorker:
    worker = FactorWorker(make_factor_dir(tmp_path, body))
    worker.load_modules()
    return worker


def request_meta():
    return {
        "id": "task-1",
        "encoding": "json",
        "target_start_time": "2026-07-28T00:00:00.000000001Z",
        "target_end_time": "2026-07-28T00:00:00.000000002Z",
        "factors": [{
            "name": "Generic",
            "input_columns": ["value"],
            "outputs": ["double", "triple"],
            "params": {"window": 2},
        }],
        "df": {
            "columns": {"value": [1.0, 3.0, 5.0]},
            "data_times": [
                "2026-07-28T00:00:00Z",
                "2026-07-28T00:00:00.000000001Z",
                "2026-07-28T00:00:00.000000002Z",
            ],
        },
    }
