from pathlib import Path

import pytest

from test_worker import batch_request, factor_call, make_factor_dir, write_factor
from worker import FactorWorker


def test_compute_receives_period_context_and_factor_specific_times(tmp_path: Path):
    factors_dir = make_factor_dir(tmp_path)
    path = write_factor(
        factors_dir,
        "Context",
        '''
assert context["subject_id"] == "BTC"
assert context["frequency"] == "1m"
assert context["period_time"] == 1785196800
assert context["period_times"] == ["2026-07-27T23:59:00Z", "2026-07-28T00:00:00Z"]
assert "period_times_by_factor" not in context
assert "config_snapshot_id" not in context
result = df[["data_time", "series_tag"]].copy()
result["double"] = df["value"] * 2
result["triple"] = df["value"] * 3
return result
'''.strip(),
    )
    response = FactorWorker(factors_dir).execute_request(batch_request([
        factor_call(path, "ctx", "Context", ["value"], ["double", "triple"])
    ]))
    assert response["items"][0]["ok"] is True


def test_old_two_argument_compute_is_not_supported(tmp_path: Path):
    factors_dir = make_factor_dir(tmp_path)
    path = factors_dir / "Legacy.py"
    path.write_text("def compute(df, params):\n    return df\n", encoding="utf-8")
    response = FactorWorker(factors_dir).execute_request(batch_request([
        factor_call(path, "legacy", "Legacy", ["value"], ["value"])
    ]))
    assert response["items"][0]["ok"] is False
    assert "positional" in response["items"][0]["error"]["message"]


def test_missing_context_is_rejected(tmp_path: Path):
    factors_dir = make_factor_dir(tmp_path)
    meta = batch_request([factor_call(factors_dir / "Generic.py", "generic", "Generic", ["value"], ["double", "triple"])])
    meta.pop("context")
    with pytest.raises((TypeError, ValueError), match="context"):
        FactorWorker(factors_dir).execute_request(meta)


def test_cross_section_unified_compute_and_subject_identity(tmp_path: Path):
    factors_dir = make_factor_dir(tmp_path)
    path = write_factor(
        factors_dir,
        "Cross",
        '''
assert context["expected_subjects"] == ["BTC", "ETH"]
result = df[["data_time", "series_tag", "subject_id"]].copy()
result["rank"] = df.groupby("data_time")["value"].rank()
return result
'''.strip(),
    )
    rows = [
        ["2026-07-27T23:59:00Z", "", "BTC", 1.0],
        ["2026-07-27T23:59:00Z", "", "ETH", 2.0],
        ["2026-07-28T00:00:00Z", "", "BTC", 2.0],
        ["2026-07-28T00:00:00Z", "", "ETH", 1.0],
    ]
    meta = batch_request([
        factor_call(path, "cross", "Cross", ["value"], ["rank"], factor_type="cross_section")
    ], columns=["data_time", "series_tag", "subject_id", "value"], rows=rows)
    meta["context"].pop("subject_id")
    meta["context"].update(expected_subjects=["BTC", "ETH"], available_subjects=["BTC", "ETH"])
    result = FactorWorker(factors_dir).execute_request(meta)["items"][0]["results"]
    assert result["columns"] == ["data_time", "series_tag", "subject_id", "rank"]
    assert [(row[2], row[3]) for row in result["rows"]] == [("BTC", 2.0), ("ETH", 1.0)]


def test_cross_section_context_can_describe_partial_available_universe(tmp_path: Path):
    factors_dir = make_factor_dir(tmp_path)
    path = write_factor(
        factors_dir, "Cross",
        'result = df[["data_time", "series_tag", "subject_id"]].copy(); result["rank"] = 1; return result',
    )
    meta = batch_request([
        factor_call(path, "cross", "Cross", ["value"], ["rank"], factor_type="cross_section")
    ], columns=["data_time", "series_tag", "subject_id", "value"], rows=[
        ["2026-07-28T00:00:00Z", "", "BTC", 1.0]
    ])
    meta["context"].pop("subject_id")
    meta["context"].update(expected_subjects=["BTC", "ETH"], available_subjects=["BTC"])
    result = FactorWorker(factors_dir).execute_request(meta)["items"][0]
    assert result["ok"] is True
    assert result["results"]["rows"][0][2] == "BTC"
