import io
import hashlib
import math
import subprocess
import sys
from pathlib import Path

import pandas as pd
import pytest

from codec import (
    TYPE_ERROR,
    TYPE_HELLO,
    TYPE_LOAD,
    TYPE_RUN,
    _json_value,
    decode_json_df,
    read_frame,
    write_frame,
)
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
    (factors_dir / "Loop.py").write_text("while True:\n    pass\n", encoding="utf-8")
    (factors_dir / "Exit.py").write_text("import os\nos._exit(1)\n", encoding="utf-8")
    (factors_dir / "Noisy.py").write_text("print('draft output')\n", encoding="utf-8")
    (factors_dir / "Broken.py").write_text("def compute(:\n", encoding="utf-8")
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
        assert meta["factors"] == []
        assert meta["load_errors"] == {}
        assert meta["encodings"] == ["json"]
    finally:
        proc.kill()
        proc.wait(timeout=5)


def test_worker_load_error_is_structured_and_stdout_safe(tmp_path: Path):
    factors_dir = make_factor_dir(tmp_path)
    noisy = factors_dir / "Noisy.py"
    noisy.write_text(
        "print('import stdout')\n"
        "import sys\n"
        "print('import stderr', file=sys.stderr)\n"
        "raise ValueError('load failed')\n",
        encoding="utf-8",
    )
    proc = subprocess.Popen(
        [sys.executable, str(Path(__file__).with_name("worker.py")), "--factors-dir", str(factors_dir)],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    try:
        assert read_frame(proc.stdout)[0] == TYPE_HELLO
        write_frame(proc.stdin, TYPE_LOAD, {
            "id": "load-1",
            "logical_id": "Noisy",
            "path": str(noisy),
            "source_hash": hashlib.sha256(noisy.read_bytes()).hexdigest(),
        })
        frame_type, meta, payload = read_frame(proc.stdout)
        assert frame_type == TYPE_ERROR
        assert payload == b""
        assert meta["id"] == "load-1"
        assert "load failed" in meta["message"]
        assert meta["diagnostics"] == {
            "stdout": "import stdout\n",
            "stderr": "import stderr\n",
        }
    finally:
        proc.kill()
        proc.wait(timeout=5)


def test_decode_json_df_preserves_identity_order_and_nanoseconds():
    df = decode_json_df({
        "df": {
            "columns": ["data_time", "series_tag", "value"],
            "rows": [
                ["2026-07-28T00:00:00Z", "venue:binance", 1.0],
                ["2026-07-28T00:00:00Z", "venue:okx", None],
                ["2026-07-28T00:00:00.000000001Z", "", 2.0],
            ],
        }
    })
    assert str(df["data_time"].dtype) == "datetime64[ns, UTC]"
    assert df["data_time"].iloc[2] - df["data_time"].iloc[0] == pd.Timedelta(1, unit="ns")
    assert math.isnan(df["value"].iloc[1])


@pytest.mark.parametrize(
    ("rows", "message"),
    [
        (
            [
                ["2026-07-28T00:00:00Z", "venue:binance", 1.0],
                ["2026-07-28T00:00:00Z", "venue:binance", 2.0],
            ],
            "duplicate",
        ),
        (
            [
                ["2026-07-28T00:00:00Z", "venue:okx", 1.0],
                ["2026-07-28T00:00:00Z", "venue:binance", 2.0],
            ],
            "sorted",
        ),
        ([["2026-07-28T00:00:00Z", " bad", 1.0]], "whitespace"),
    ],
)
def test_decode_json_df_rejects_invalid_identity(rows, message):
    with pytest.raises((TypeError, ValueError), match=message):
        decode_json_df({
            "df": {
                "columns": ["data_time", "series_tag", "value"],
                "rows": rows,
            }
        })


def test_execute_preserves_tags_and_filters_half_open_target_range(tmp_path: Path):
    worker = loaded_worker(tmp_path)
    response = worker.execute_request(request_meta())
    assert response["results"] == [
        {
            "data_time": "2026-07-28T00:00:00.000000001Z",
            "series_tag": "venue:binance",
            "values": {"double": 6.0, "triple": 9.0},
        },
        {
            "data_time": "2026-07-28T00:00:00.000000001Z",
            "series_tag": "venue:okx",
            "values": {"double": 10.0, "triple": 15.0},
        },
    ]


def test_execute_can_return_cross_tag_spread(tmp_path: Path):
    source = """
left = df[df["series_tag"] == params["left"]].set_index("data_time")
right = df[df["series_tag"] == params["right"]].set_index("data_time")
joined = left[["value"]].join(right[["value"]], lsuffix="_left", rsuffix="_right")
return pd.DataFrame({
    "data_time": joined.index,
    "series_tag": params["output"],
    "double": joined["value_left"] - joined["value_right"],
    "triple": joined["value_right"] - joined["value_left"],
})
""".strip()
    worker = loaded_worker(tmp_path, body=source)
    meta = request_meta()
    meta["factor"]["params"] = {
        "left": "venue:binance",
        "right": "venue:okx",
        "output": "venue_pair:binance-okx",
    }
    result = worker.execute_request(meta)["results"]
    assert result == [{
        "data_time": "2026-07-28T00:00:00.000000001Z",
        "series_tag": "venue_pair:binance-okx",
        "values": {"double": -2.0, "triple": 2.0},
    }]


def test_execute_accepts_empty_dataframe(tmp_path: Path):
    worker = loaded_worker(
        tmp_path,
        body='return pd.DataFrame(columns=["data_time", "series_tag", "double", "triple"])',
    )
    assert worker.execute_request(request_meta())["results"] == []


@pytest.mark.parametrize(
    ("source", "outputs", "message"),
    [
        (
            'return pd.DataFrame({"data_time": df["data_time"], "series_tag": df["series_tag"], "double": df["value"]})',
            ["double", "extra"],
            "outputs mismatch",
        ),
        (
            'return pd.DataFrame({"data_time": df["data_time"], "series_tag": df["series_tag"], "double": df["value"], "triple": df["value"], "extra": df["value"]})',
            ["double", "triple"],
            "outputs mismatch",
        ),
        ('return {"double": df["value"]}', ["double", "triple"], "pandas DataFrame"),
    ],
)
def test_execute_rejects_invalid_output_shape(tmp_path, source, outputs, message):
    worker = loaded_worker(tmp_path, body=source)
    meta = request_meta()
    meta["factor"]["outputs"] = outputs
    with pytest.raises((TypeError, ValueError), match=message):
        worker.execute_request(meta)


@pytest.mark.parametrize(
    ("body", "message"),
    [
        (
            'result = df[["data_time", "series_tag"]].copy(); result["double"] = 1; result["triple"] = 2; return pd.concat([result, result.iloc[[0]]], ignore_index=True)',
            "duplicate",
        ),
        (
            'result = df[["data_time", "series_tag"]].copy(); result.loc[:, "data_time"] = "bad"; result["double"] = 1; result["triple"] = 2; return result',
            "(?i)time",
        ),
        (
            'result = df[["data_time", "series_tag"]].copy(); result.loc[0, "data_time"] = pd.NaT; result["double"] = 1; result["triple"] = 2; return result',
            "(?i)time",
        ),
        (
            'result = df[["data_time", "series_tag"]].copy(); result.loc[:, "series_tag"] = " bad"; result["double"] = 1; result["triple"] = 2; return result',
            "whitespace",
        ),
    ],
)
def test_execute_rejects_invalid_result_identity(tmp_path, body, message):
    worker = loaded_worker(tmp_path, body=body)
    with pytest.raises((TypeError, ValueError), match=message):
        worker.execute_request(request_meta())


def test_execute_normalizes_nan_and_infinity(tmp_path: Path):
    worker = loaded_worker(
        tmp_path,
        body='result = df[["data_time", "series_tag"]].copy(); result["double"] = float("nan"); result["triple"] = float("inf"); return result',
    )
    result = worker.execute_request(request_meta())["results"]
    assert all(row["values"] == {"double": None, "triple": None} for row in result)


def test_execute_rejects_non_object_params(tmp_path: Path):
    worker = loaded_worker(tmp_path)
    meta = request_meta()
    meta["factor"]["params"] = []
    with pytest.raises(TypeError, match="params must be an object"):
        worker.execute_request(meta)


def test_execute_rejects_legacy_signal_only_module(tmp_path: Path):
    factors_dir = tmp_path / "factors"
    factors_dir.mkdir()
    (factors_dir / "Legacy.py").write_text(
        "def signal(df, n, factor_name):\n    return df\n", encoding="utf-8"
    )
    worker = FactorWorker(factors_dir)
    load_factor(worker, factors_dir / "Legacy.py", "Legacy")
    meta = request_meta()
    meta["factor"]["name"] = "Legacy"
    with pytest.raises(AttributeError, match=r"must define compute\(df, params\)"):
        worker.execute_request(meta)


def test_explicit_load_reports_captured_import_diagnostics(tmp_path: Path):
    factors_dir = make_factor_dir(tmp_path)
    noisy = factors_dir / "Noisy.py"
    noisy.write_text(
        "print('draft stdout')\n"
        "import sys\n"
        "print('draft stderr', file=sys.stderr)\n"
        "raise ValueError('broken draft')\n",
        encoding="utf-8",
    )
    worker = FactorWorker(factors_dir)
    with pytest.raises(Exception, match="broken draft") as exc_info:
        load_factor(worker, noisy, "Noisy")
    assert exc_info.value.stdout == "draft stdout\n"
    assert exc_info.value.stderr == "draft stderr\n"
    assert worker.factors == {}


def test_load_rejects_missing_source_hash_without_importing(tmp_path: Path):
    factors_dir = make_factor_dir(tmp_path)
    worker = FactorWorker(factors_dir)
    with pytest.raises(ValueError, match="source_hash"):
        worker.load_one({
            "logical_id": "Generic",
            "path": str(factors_dir / "Generic.py"),
        })
    assert worker.factors == {}


def test_json_value_normalizes_nan_and_infinity():
    assert _json_value(float("nan")) is None
    assert _json_value(float("inf")) is None
    assert _json_value(float("-inf")) is None


def make_factor_dir(tmp_path: Path, body=None) -> Path:
    factors_dir = tmp_path / "factors"
    factors_dir.mkdir(exist_ok=True)
    if body is None:
        body = """
result = df[["data_time", "series_tag"]].copy()
result["double"] = df["value"] * 2
result["triple"] = df["value"] * 3
return result
""".strip()
    (factors_dir / "Generic.py").write_text(
        f"import pandas as pd\n\ndef compute(df, params):\n"
        + "\n".join(f"    {line}" for line in body.splitlines())
        + "\n",
        encoding="utf-8",
    )
    return factors_dir


def loaded_worker(tmp_path: Path, body=None) -> FactorWorker:
    factors_dir = make_factor_dir(tmp_path, body)
    worker = FactorWorker(factors_dir)
    load_factor(worker, factors_dir / "Generic.py", "Generic")
    return worker


def load_factor(worker: FactorWorker, path: Path, logical_id: str):
    raw = path.read_bytes()
    return worker.load_one({
        "logical_id": logical_id,
        "path": str(path),
        "source_hash": hashlib.sha256(raw).hexdigest(),
    })


def request_meta():
    return {
        "id": "task-1",
        "encoding": "json",
        "target_start_time": "2026-07-28T00:00:00.000000001Z",
        "target_end_time": "2026-07-28T00:00:00.000000002Z",
        "factor": {
            "name": "Generic",
            "input_columns": ["value"],
            "outputs": ["double", "triple"],
            "params": {"window": 2},
        },
        "df": {
            "columns": ["data_time", "series_tag", "value"],
            "rows": [
                ["2026-07-28T00:00:00Z", "venue:binance", 1.0],
                ["2026-07-28T00:00:00Z", "venue:okx", 2.0],
                ["2026-07-28T00:00:00.000000001Z", "venue:binance", 3.0],
                ["2026-07-28T00:00:00.000000001Z", "venue:okx", 5.0],
                ["2026-07-28T00:00:00.000000002Z", "", 7.0],
            ],
        },
    }
