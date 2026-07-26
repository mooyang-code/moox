import io
import math
import subprocess
import sys
from pathlib import Path

import pandas as pd

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
    factors_dir = make_factor_dir(tmp_path, "Bias")
    proc = subprocess.Popen(
        [
            sys.executable,
            str(Path(__file__).with_name("worker.py")),
            "--factors-dir",
            str(factors_dir),
        ],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    try:
        frame_type, meta, payload = read_frame(proc.stdout)
        assert frame_type == TYPE_HELLO
        assert payload == b""
        assert meta["factors"] == ["Bias"]
        assert meta["encodings"] == ["json"]
        assert "sections" not in meta
    finally:
        proc.kill()
        proc.wait(timeout=5)


def test_execute_returns_values_only_for_target_range(tmp_path: Path):
    worker = FactorWorker(make_factor_dir(tmp_path, "Bias"))
    worker.load_modules()
    response = worker.execute_request(
        request_meta(
            periods=[2],
            target_start_time="2026-07-06T14:15:00Z",
            target_end_time="2026-07-06T14:17:00Z",
        )
    )
    assert response["results"]["Bias_2"] == [5.0, 6.0]
    assert "result_tails" not in response
    assert "per_factor_ms" not in response
    assert "elapsed_ms" not in response


def test_bad_factor_module_does_not_block_ready(tmp_path: Path):
    factors_dir = make_factor_dir(tmp_path, "Bias")
    (factors_dir / "Broken.py").write_text("def signal(:\n", encoding="utf-8")
    worker = FactorWorker(factors_dir)
    worker.load_modules()
    assert list(worker.factors) == ["Bias"]
    assert "Broken" in worker.load_errors


def test_json_value_normalizes_nan_and_infinity():
    assert _json_value(float("nan")) is None
    assert _json_value(float("inf")) is None
    assert _json_value(float("-inf")) is None


def test_decode_json_df_converts_null_and_time_to_utc():
    df = decode_json_df(
        {
            "df": {
                "columns": {"open": [1.0, None], "close": [2.0, 3.0]},
                "index_ms": [1783347300000, 1783347360000],
            }
        }
    )
    assert str(df["candle_begin_time"].dtype) == "datetime64[ns, UTC]"
    assert pd.Timestamp("2026-07-06T14:15:00Z") == df["candle_begin_time"].iloc[0]
    assert math.isnan(df["open"].iloc[1])


def make_factor_dir(tmp_path: Path, name: str) -> Path:
    factors_dir = tmp_path / "factors"
    factors_dir.mkdir(exist_ok=True)
    (factors_dir / f"{name}.py").write_text(
        "def signal(df, n, factor_name):\n"
        "    df[factor_name] = df['close'] + n\n"
        "    return df\n",
        encoding="utf-8",
    )
    return factors_dir


def request_meta(periods=None, target_start_time=None, target_end_time=None):
    return {
        "id": "task-1",
        "encoding": "json",
        "target_start_time": target_start_time or "2026-07-06T14:15:00Z",
        "target_end_time": target_end_time or "2026-07-06T14:17:00Z",
        "factors": [{"name": "Bias", "periods": periods or [20]}],
        "df": {
            "columns": {"close": [1.0, 2.0, 3.0, 4.0]},
            "index_ms": [1783347180000, 1783347240000, 1783347300000, 1783347360000],
        },
    }
