import io
import math
import subprocess
import sys
from pathlib import Path

import pandas as pd

from codec import FRAME_READY, FRAME_REQUEST, read_frame, write_frame, decode_json_df
from worker import FactorWorker


def test_frame_round_trip():
    stream = io.BytesIO()

    write_frame(stream, FRAME_REQUEST, {"id": "task-1", "encoding": "json"}, b"payload")
    stream.seek(0)
    frame_type, meta, payload = read_frame(stream)

    assert frame_type == FRAME_REQUEST
    assert meta == {"id": "task-1", "encoding": "json"}
    assert payload == b"payload"


def test_worker_writes_ready_with_loaded_factors(tmp_path: Path):
    factors_dir = tmp_path / "factors"
    sections_dir = tmp_path / "sections"
    factors_dir.mkdir()
    sections_dir.mkdir()
    (factors_dir / "Bias.py").write_text(
        "def signal_multi_params(df, param_list):\n"
        "    return {str(p): df['close'] for p in param_list}\n",
        encoding="utf-8",
    )

    proc = subprocess.Popen(
        [
            sys.executable,
            str(Path(__file__).with_name("worker.py")),
            "--factors-dir",
            str(factors_dir),
            "--sections-dir",
            str(sections_dir),
            "--encoding",
            "json",
        ],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    try:
        frame_type, meta, payload = read_frame(proc.stdout)
        assert frame_type == FRAME_READY
        assert payload == b""
        assert meta["status"] == "ready"
        assert meta["factors"] == ["Bias"]
    finally:
        proc.kill()
        proc.wait(timeout=5)


def test_bad_factor_module_does_not_block_worker_ready(tmp_path: Path):
    factors_dir = tmp_path / "factors"
    sections_dir = tmp_path / "sections"
    factors_dir.mkdir()
    sections_dir.mkdir()
    (factors_dir / "Bias.py").write_text(
        "def signal_multi_params(df, param_list):\n"
        "    return {str(p): df['close'] for p in param_list}\n",
        encoding="utf-8",
    )
    (factors_dir / "Broken.py").write_text("def signal(:\n", encoding="utf-8")

    worker = FactorWorker(factors_dir=factors_dir, sections_dir=sections_dir, encoding="json")
    worker.load_modules()

    assert sorted(worker.factors.keys()) == ["Bias"]
    assert "Broken" in worker.load_errors


def test_signal_multi_params_is_preferred(tmp_path: Path):
    factors_dir = tmp_path / "factors"
    factors_dir.mkdir()
    (factors_dir / "Bias.py").write_text(
        "def signal(*args):\n"
        "    raise AssertionError('signal should not be called')\n\n"
        "def signal_multi_params(df, param_list):\n"
        "    return {str(p): df['close'] + int(p) for p in param_list}\n",
        encoding="utf-8",
    )
    worker = FactorWorker(factors_dir=factors_dir, sections_dir=tmp_path / "sections", encoding="json")
    worker.load_modules()

    response = worker.execute_request(request_meta(params=[20, 96], writeback_bars=2))

    assert response["results"]["Bias_20"]["values"] == [23.0, 24.0]
    assert response["results"]["Bias_96"]["values"] == [99.0, 100.0]


def test_signal_path_uses_copy_per_param_and_only_returns_factor_columns(tmp_path: Path):
    factors_dir = tmp_path / "factors"
    factors_dir.mkdir()
    (factors_dir / "Cci.py").write_text(
        "def signal(df, n, factor_name):\n"
        "    if 'temp' in df.columns:\n"
        "        raise AssertionError('df was reused across params')\n"
        "    df['temp'] = 100\n"
        "    df[factor_name] = df['close'] + n\n"
        "    return df\n",
        encoding="utf-8",
    )
    worker = FactorWorker(factors_dir=factors_dir, sections_dir=tmp_path / "sections", encoding="json")
    worker.load_modules()

    meta = request_meta(name="Cci", params=[2, 3], writeback_bars=2)
    response = worker.execute_request(meta)

    assert sorted(response["results"].keys()) == ["Cci_2", "Cci_3"]
    assert response["results"]["Cci_2"]["values"] == [5.0, 6.0]
    assert response["results"]["Cci_3"]["values"] == [6.0, 7.0]


def test_each_factor_keeps_its_own_writeback_tail(tmp_path: Path):
    factors_dir = tmp_path / "factors"
    factors_dir.mkdir()
    (factors_dir / "Fast.py").write_text(
        "def signal(df, n, factor_name):\n"
        "    df[factor_name] = df['close'] + n\n"
        "    return df\n", encoding="utf-8"
    )
    (factors_dir / "Slow.py").write_text(
        "def signal(df, n, factor_name):\n"
        "    df[factor_name] = df['close'] + n\n"
        "    return df\n", encoding="utf-8"
    )
    worker = FactorWorker(factors_dir=factors_dir, sections_dir=tmp_path / "sections", encoding="json")
    worker.load_modules()
    meta = request_meta(name="Fast", params=[1], writeback_bars=1)
    meta["factors"] = [
        {"name": "Fast", "params": [1], "writeback_bars": 1},
        {"name": "Slow", "params": [1], "writeback_bars": 3},
    ]
    response = worker.execute_request(meta)
    assert response["results"]["Fast_1"]["tail"] == 1
    assert response["results"]["Slow_1"]["tail"] == 3


def test_decode_json_df_converts_null_to_nan_and_time_to_utc():
    df = decode_json_df(
        {
            "df": {
                "columns": {
                    "open": [1.0, None],
                    "close": [2.0, 3.0],
                },
                "index_ms": [1783347300000, 1783347360000],
            }
        }
    )

    assert str(df["candle_begin_time"].dtype) == "datetime64[ns, UTC]"
    assert pd.Timestamp("2026-07-06T14:15:00Z") == df["candle_begin_time"].iloc[0]
    assert math.isnan(df["open"].iloc[1])


def request_meta(name="Bias", params=None, writeback_bars=1):
    if params is None:
        params = [20]
    return {
        "id": "task-1",
        "encoding": "json",
        "factors": [
            {
                "name": name,
                "params": params,
                "writeback_bars": writeback_bars,
            }
        ],
        "df": {
            "columns": {
                "close": [1.0, 2.0, 3.0, 4.0],
            },
            "index_ms": [1783347180000, 1783347240000, 1783347300000, 1783347360000],
        },
    }
