from pathlib import Path

import pytest

from test_worker import load_factor, loaded_worker, request_meta
from worker import FactorWorker


def source_worker(tmp_path, source):
    path = tmp_path / "Generic.py"
    path.write_text(source, encoding="utf-8")
    worker = FactorWorker(tmp_path)
    load_factor(worker, path, "Generic")
    return worker


def test_compute_receives_separate_context_and_params(tmp_path: Path):
    source = '''
def compute(df, params, context):
    assert params == {"window": 2}
    assert context["subject_id"] == "BTC"
    assert context["frequency"] == "1m"
    assert context["input_contract_version"] == "contract-1"
    assert "factor_type" not in context
    result = df[["data_time", "series_tag"]].copy()
    result["double"] = df["value"] * 2
    result["triple"] = df["value"] * 3
    return result
'''
    worker = source_worker(tmp_path, source)
    meta = request_meta()
    meta["factor"]["factor_type"] = "timeseries"
    meta["context"] = context()
    response = worker.execute_request(meta)
    assert len(response["results"]) == 2


def context():
    return {"subject_id": "BTC", "frequency": "1m", "period_time": 1785196800,
            "input_contract_version": "contract-1"}


def test_old_two_argument_compute_is_not_supported(tmp_path: Path):
    worker = source_worker(tmp_path, "def compute(df, params): return df\n")
    meta = request_meta()
    meta["factor"]["factor_type"] = "timeseries"
    meta["context"] = context()
    with pytest.raises(TypeError, match="positional"):
        worker.execute_request(meta)


def test_missing_context_is_rejected(tmp_path: Path):
    worker = loaded_worker(tmp_path)
    meta = request_meta()
    meta.pop("context", None)
    with pytest.raises((TypeError, ValueError), match="context"):
        worker.execute_request(meta)


def test_cross_section_unified_compute_and_subject_identity(tmp_path: Path):
    source = '''
def compute(df, params, context):
    assert context["expected_subjects"] == ["BTC", "ETH"]
    result = df[["data_time", "series_tag", "subject_id"]].copy()
    result["rank"] = df["value"].rank()
    return result
'''
    worker = source_worker(tmp_path, source)
    meta = request_meta()
    meta["factor"].update(factor_type="cross_section", outputs=["rank"])
    meta["context"] = {**context(), "subject_id": "", "expected_subjects": ["BTC", "ETH"],
                       "available_subjects": ["BTC", "ETH"], "missing_subjects": []}
    at = meta["target_start_time"]
    meta["df"] = {"columns": ["data_time", "series_tag", "subject_id", "value"],
                  "rows": [[at, "", "BTC", 2.0], [at, "", "ETH", 1.0]]}
    rows = worker.execute_request(meta)["results"]
    assert [(r["subject_id"], r["values"]["rank"]) for r in rows] == [("BTC", 2.0), ("ETH", 1.0)]


def test_cross_section_rejects_output_outside_universe(tmp_path: Path):
    source = '''
def compute(df, params, context):
    result = df[["data_time", "series_tag"]].copy()
    result["subject_id"] = "OUTSIDE"
    result["rank"] = 1
    return result
'''
    worker = source_worker(tmp_path, source)
    meta = request_meta()
    meta["factor"].update(factor_type="cross_section", outputs=["rank"])
    meta["context"] = {**context(), "subject_id": "", "expected_subjects": ["BTC"],
                       "available_subjects": ["BTC"], "missing_subjects": []}
    meta["df"]["columns"].insert(2, "subject_id")
    for row in meta["df"]["rows"]:
        row.insert(2, "BTC")
    with pytest.raises(ValueError, match="universe"):
        worker.execute_request(meta)
