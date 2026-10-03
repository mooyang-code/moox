import io
import hashlib
import math
import subprocess
import sys
import textwrap
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
            "source_hash": source_hash(noisy),
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


def test_batch_runs_multiple_factors_on_shared_frame(tmp_path: Path):
    factors_dir = make_factor_dir(tmp_path)
    other = write_factor(
        factors_dir,
        "Other",
        'result = df[["data_time", "series_tag"]].copy(); result["other"] = df["value"] + 10; return result',
    )
    worker = FactorWorker(factors_dir)
    meta = batch_request([
        factor_call(factors_dir / "Generic.py", "factor_one", "Generic", ["value"], ["double", "triple"]),
        factor_call(other, "factor_two", "Other", ["value"], ["other"]),
    ])
    response = worker.execute_request(meta)
    assert [item["factor_id"] for item in response["items"]] == ["factor_one", "factor_two"]
    assert all(item["ok"] for item in response["items"])
    assert response["items"][0]["results"]["columns"] == ["data_time", "series_tag", "double", "triple"]
    assert response["items"][0]["results"]["rows"] == [[
        "2026-07-28T00:00:00Z", "venue:binance", 6.0, 9.0
    ], ["2026-07-28T00:00:00Z", "venue:okx", 10.0, 15.0]]
    assert response["items"][1]["results"]["columns"] == ["data_time", "series_tag", "other"]


def test_slices_frame_per_factor_lookback(tmp_path: Path):
    factors_dir = make_factor_dir(tmp_path)
    count = write_factor(
        factors_dir,
        "Count",
        'result = df[["data_time", "series_tag"]].copy(); result["count"] = len(df); return result',
    )
    worker = FactorWorker(factors_dir)
    rows = [
        [f"2026-07-27T23:{i}:00Z", "venue:binance", float(i)] for i in range(58, 60)
    ] + [["2026-07-28T00:00:00Z", "venue:binance", 60.0]]
    short, long = factor_call(count, "short", "Count", ["value"], ["count"], 2), factor_call(
        count, "long", "CountAlias", ["value"], ["count"], 3
    )
    meta = batch_request([short, long], rows=rows)
    results = {item["factor_id"]: item["results"]["rows"][0][2] for item in worker.execute_request(meta)["items"]}
    assert results == {"short": 2, "long": 3}
    assert len(worker.modules) == 1


def test_projects_only_input_columns(tmp_path: Path):
    factors_dir = make_factor_dir(tmp_path)
    project = write_factor(
        factors_dir,
        "Project",
        'result = df[["data_time", "series_tag"]].copy(); result["seen"] = ",".join(df.columns); return result',
    )
    response = FactorWorker(factors_dir).execute_request(batch_request([
        factor_call(project, "project", "Project", ["value"], ["seen"])
    ]))
    assert response["items"][0]["results"]["rows"][0][2] == "data_time,series_tag,value"


def test_factor_error_isolated(tmp_path: Path):
    factors_dir = make_factor_dir(tmp_path)
    broken = write_factor(factors_dir, "Broken", 'raise RuntimeError("isolated failure")')
    good = write_factor(
        factors_dir, "Good",
        'result = df[["data_time", "series_tag"]].copy(); result["ok"] = 1; return result',
    )
    response = FactorWorker(factors_dir).execute_request(batch_request([
        factor_call(broken, "bad", "Broken", ["value"], ["ok"]),
        factor_call(good, "good", "Good", ["value"], ["ok"]),
    ]))
    assert response["items"][0]["ok"] is False
    assert "isolated failure" in response["items"][0]["error"]["message"]
    assert response["items"][1]["ok"] is True


def test_timeseries_context_has_subject_and_period_times(tmp_path: Path):
    factors_dir = make_factor_dir(tmp_path)
    capture = write_factor(
        factors_dir, "Capture",
        'result = df[["data_time", "series_tag"]].copy(); result["seen"] = str(context); return result',
    )
    response = FactorWorker(factors_dir).execute_request(batch_request([
        factor_call(capture, "capture", "Capture", ["value"], ["seen"])
    ]))
    seen = response["items"][0]["results"]["rows"][0][2]
    assert "'subject_id': 'BTC'" in seen
    assert "'period_times': ['2026-07-27T23:59:00Z', '2026-07-28T00:00:00Z']" in seen


def test_recalc_returns_every_target_period_in_one_factor_call(tmp_path: Path):
    factors_dir = make_factor_dir(tmp_path)
    factor_path = write_factor(
        factors_dir, "Recalc",
        'result = df[["data_time", "series_tag"]].copy(); result["value"] = df["value"] * 2; return result',
    )
    factor = factor_call(factor_path, "recalc", "Recalc", ["value"], ["value"])
    meta = batch_request([factor], rows=[
        ["2026-07-27T23:59:00Z", "venue:binance", 1.0],
        ["2026-07-28T00:00:00Z", "venue:binance", 2.0],
        ["2026-07-28T00:01:00Z", "venue:binance", 3.0],
        ["2026-07-28T00:02:00Z", "venue:binance", 4.0],
    ])
    meta["context"]["period_time"] = 1785196920
    meta["context"]["period_times_by_factor"]["recalc"] = [
        "2026-07-27T23:59:00Z", "2026-07-28T00:00:00Z",
        "2026-07-28T00:01:00Z", "2026-07-28T00:02:00Z",
    ]
    meta["context"]["target_period_times"] = ["2026-07-28T00:01:00Z", "2026-07-28T00:02:00Z"]
    response = FactorWorker(factors_dir).execute_request(meta)
    item = response["items"][0]
    assert item["ok"] is True
    assert item["results"]["rows"] == [
        ["2026-07-28T00:01:00Z", "venue:binance", 6.0],
        ["2026-07-28T00:02:00Z", "venue:binance", 8.0],
    ]


def test_cross_section_rejects_unknown_subject_output(tmp_path: Path):
    factors_dir = make_factor_dir(tmp_path)
    cross = write_factor(
        factors_dir, "Cross",
        'result = df[["data_time", "series_tag", "subject_id"]].copy(); result["score"] = 1; result.loc[0, "subject_id"] = "ETH"; return result',
    )
    meta = batch_request([
        factor_call(cross, "cross", "Cross", ["value"], ["score"], factor_type="cross_section")
    ], columns=["data_time", "series_tag", "subject_id", "value"], rows=[
        ["2026-07-28T00:00:00Z", "venue:binance", "BTC", 60.0]
    ])
    meta["context"].pop("subject_id")
    meta["context"].update(expected_subjects=["BTC"], available_subjects=["BTC"])
    response = FactorWorker(factors_dir).execute_request(meta)
    assert response["items"][0]["ok"] is False
    assert "outside the available universe" in response["items"][0]["error"]["message"]


@pytest.mark.parametrize(
    ("location", "field"),
    [("request", "task_id"), ("context", "binding_id"), ("factor", "config_snapshot_id"),
     ("request", "missing_subjects")],
)
def test_rejects_legacy_fields(tmp_path: Path, location: str, field: str):
    factors_dir = make_factor_dir(tmp_path)
    worker = FactorWorker(factors_dir)
    meta = batch_request([factor_call(factors_dir / "Generic.py", "generic", "Generic", ["value"], ["double", "triple"])])
    if location == "request":
        meta[field] = "legacy"
    elif location == "context":
        meta["context"][field] = "legacy"
    else:
        meta["factors"][0][field] = "legacy"
    with pytest.raises(ValueError, match="legacy field"):
        worker.execute_request(meta)


@pytest.mark.parametrize(
    ("body", "outputs", "message"),
    [
        ('return pd.DataFrame({"data_time": df["data_time"], "series_tag": df["series_tag"], "one": 1})', ["two"], "outputs mismatch"),
        ('return {"one": 1}', ["one"], "pandas DataFrame"),
    ],
)
def test_batch_rejects_invalid_output_shape(tmp_path, body, outputs, message):
    factors_dir = make_factor_dir(tmp_path)
    path = write_factor(factors_dir, "Invalid", body)
    response = FactorWorker(factors_dir).execute_request(batch_request([
        factor_call(path, "invalid", "Invalid", ["value"], outputs)
    ]))
    assert response["items"][0]["ok"] is False
    assert message in response["items"][0]["error"]["message"]


def test_batch_normalizes_nan_and_infinity(tmp_path: Path):
    factors_dir = make_factor_dir(tmp_path)
    path = write_factor(
        factors_dir, "NonFinite",
        'result = df[["data_time", "series_tag"]].copy(); result["nan"] = float("nan"); result["inf"] = float("inf"); return result',
    )
    response = FactorWorker(factors_dir).execute_request(batch_request([
        factor_call(path, "nonfinite", "NonFinite", ["value"], ["nan", "inf"])
    ]))
    assert response["items"][0]["results"]["rows"][0][2:] == [None, None]


def test_batch_rejects_non_object_params(tmp_path: Path):
    factors_dir = make_factor_dir(tmp_path)
    meta = batch_request([factor_call(factors_dir / "Generic.py", "generic", "Generic", ["value"], ["double", "triple"])])
    meta["factors"][0]["params"] = []
    response = FactorWorker(factors_dir).execute_request(meta)
    assert response["items"][0]["ok"] is False
    assert "params must be an object" in response["items"][0]["error"]["message"]


def test_batch_rejects_legacy_signal_only_module(tmp_path: Path):
    factors_dir = make_factor_dir(tmp_path)
    path = factors_dir / "Legacy.py"
    path.write_text("def signal(df, n, factor_name):\n    return df\n", encoding="utf-8")
    response = FactorWorker(factors_dir).execute_request(batch_request([
        factor_call(path, "legacy", "Legacy", ["value"], ["signal"])
    ]))
    assert response["items"][0]["ok"] is False
    assert "must define compute(df, params, context)" in response["items"][0]["error"]["message"]


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
    assert worker.modules == {}


def test_load_rejects_missing_source_hash_without_importing(tmp_path: Path):
    factors_dir = make_factor_dir(tmp_path)
    worker = FactorWorker(factors_dir)
    with pytest.raises(ValueError, match="source_hash"):
        worker.load_one({
            "logical_id": "Generic",
            "path": str(factors_dir / "Generic.py"),
        })
    assert worker.modules == {}


def test_json_value_normalizes_nan_and_infinity():
    assert _json_value(float("nan")) is None
    assert _json_value(float("inf")) is None
    assert _json_value(float("-inf")) is None


def make_factor_dir(tmp_path: Path, body=None) -> Path:
    factors_dir = tmp_path / "factors"
    factors_dir.mkdir(exist_ok=True)
    if body is None:
        body = 'result = df[["data_time", "series_tag"]].copy(); result["double"] = df["value"] * 2; result["triple"] = df["value"] * 3; return result'
    write_factor(factors_dir, "Generic", body)
    return factors_dir


def write_factor(factors_dir: Path, name: str, body: str) -> Path:
    path = factors_dir / f"{name}.py"
    body = textwrap.dedent(body).strip()
    path.write_text(
        "import pandas as pd\n\ndef compute(df, params, context):\n"
        + "\n".join(f"    {line}" for line in body.splitlines()) + "\n",
        encoding="utf-8",
    )
    return path


def source_hash(path: Path) -> str:
    return "sha256:" + hashlib.sha256(path.read_bytes()).hexdigest()


def load_factor(worker: FactorWorker, path: Path, logical_id: str):
    return worker.load_one({
        "logical_id": logical_id,
        "path": str(path),
        "source_hash": source_hash(path),
    })


def factor_call(path, factor_id, name, inputs, outputs, lookback=2, factor_type="timeseries"):
    return {
        "factor_id": factor_id, "name": name, "source_hash": source_hash(path),
        "source_path": str(path), "factor_type": factor_type,
        "input_columns": inputs, "outputs": outputs, "params": {},
        "lookback_periods": lookback,
    }


def batch_request(factors, rows=None, columns=None):
    target = "2026-07-28T00:00:00Z"
    if rows is None:
        rows = [
            ["2026-07-27T23:59:00Z", "venue:binance", 3.0],
            ["2026-07-27T23:59:00Z", "venue:okx", 5.0],
            [target, "venue:binance", 3.0],
            [target, "venue:okx", 5.0],
        ]
    if columns is None:
        columns = ["data_time", "series_tag", "value"]
    return {
        "id": "period-1", "encoding": "json",
        "context": {
            "period_time": 1785196800, "frequency": "1m", "subject_id": "BTC",
            "period_times_by_factor": {
                factor["factor_id"]: [
                    (pd.Timestamp(target) - pd.Timedelta(minutes=offset)).isoformat().replace("+00:00", "Z")
                    for offset in reversed(range(factor["lookback_periods"]))
                ]
                for factor in factors
            },
        },
        "df": {"columns": columns, "rows": rows}, "factors": factors,
    }
