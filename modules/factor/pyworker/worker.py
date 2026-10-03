import argparse
import hashlib
import importlib.util
import math
import os
import re
import sys
import traceback
from contextlib import redirect_stderr, redirect_stdout
from copy import deepcopy
from io import StringIO
from pathlib import Path

import pandas as pd

runtime_python = os.environ.get("MOOX_PYTHON_RUNTIME_PATH")
if runtime_python:
    sys.path.insert(0, runtime_python)
else:
    sys.path.insert(0, str(Path(__file__).resolve().parents[3] / "packages" / "pyruntime" / "python"))

from codec import TYPE_ERROR, TYPE_HELLO, TYPE_LOAD, TYPE_RESULT, TYPE_RUN, decode_json_df, encode_json_batch_results, read_frame, write_frame, _validate_series_tags


_SOURCE_HASH = re.compile(r"^sha256:([0-9a-f]{64})$")
_LEGACY_FIELDS = {"task_id", "binding_id", "config_snapshot_id", "missing_subjects"}
_IDENTITY_COLUMNS = {"data_time", "series_tag", "subject_id"}


class FactorWorker:
    def __init__(self, factors_dir, encoding="json"):
        self.factors_dir = Path(factors_dir)
        self.encoding = encoding
        self.modules = {}

    def ready_meta(self):
        return {
            "status": "ready",
            "protocol_version": "moox.py/v1",
            "worker_version": "factor-v2",
            "python_version": sys.version.split()[0],
            "runtime_env_hash": "",
            "encoding": self.encoding,
            "encodings": ["json"],
            "factors": [],
            "load_errors": {},
        }

    def execute_request(self, meta):
        if not isinstance(meta, dict):
            raise TypeError("batch request must be an object")
        reject_legacy_fields(meta)
        factors = meta.get("factors")
        if not isinstance(factors, list) or not factors:
            raise ValueError("factors must be a non-empty array")
        context = meta.get("context")
        validate_base_context(context)
        period_times_by_factor = context.get("period_times_by_factor")
        if not isinstance(period_times_by_factor, dict):
            raise TypeError("context period_times_by_factor must be an object")
        frame = decode_json_df(meta)

        items = []
        seen = set()
        for factor in factors:
            if not isinstance(factor, dict):
                raise TypeError("batch factor item must be an object")
            factor_id = factor.get("factor_id")
            if not isinstance(factor_id, str) or not factor_id.strip():
                raise ValueError("factor_id is required")
            if factor_id in seen:
                raise ValueError(f"duplicate factor_id {factor_id}")
            seen.add(factor_id)
            if factor_id not in period_times_by_factor:
                raise ValueError(f"context period_times_by_factor missing {factor_id}")
            try:
                result, diagnostics = self.execute_factor(frame, factor, context, period_times_by_factor[factor_id])
                items.append({
                    "factor_id": factor_id,
                    "ok": True,
                    "results": encode_frame_result(result),
                    "logs": diagnostics,
                })
            except Exception as exc:  # noqa: BLE001 - factor failures must not cancel siblings.
                items.append({
                    "factor_id": factor_id,
                    "ok": False,
                    "error": {"type": type(exc).__name__, "message": str(exc)},
                    "logs": {
                        "stdout": getattr(exc, "stdout", ""),
                        "stderr": getattr(exc, "stderr", ""),
                    },
                })
        return encode_json_batch_results(meta.get("id", ""), items)

    def execute_factor(self, frame, factor, base_context, period_times):
        factor_id = factor["factor_id"]
        name = factor.get("name", "")
        if not isinstance(name, str) or not name.strip():
            raise ValueError(f"{factor_id} name is required")
        factor_type = factor.get("factor_type")
        if factor_type not in {"timeseries", "cross_section"}:
            raise ValueError(f"{name} factor_type must be timeseries or cross_section")
        lookback = factor.get("lookback_periods")
        if not isinstance(lookback, int) or isinstance(lookback, bool) or lookback < 1:
            raise ValueError(f"{name} lookback_periods must be a positive integer")
        periods = validate_period_times(period_times, lookback, base_context["period_time"], name)
        context = {key: deepcopy(value) for key, value in base_context.items() if key != "period_times_by_factor"}
        context["period_times"] = [time.isoformat().replace("+00:00", "Z") for time in periods]
        validate_factor_context(context, factor_type, frame, name)

        input_columns = factor.get("input_columns")
        outputs = factor.get("outputs")
        if not isinstance(input_columns, list) or any(not isinstance(v, str) or not v for v in input_columns):
            raise TypeError(f"{name} input_columns must be an array of column names")
        if not isinstance(outputs, list) or not outputs or any(not isinstance(v, str) or not v for v in outputs):
            raise TypeError(f"{name} outputs must be a non-empty array of column names")
        if len(set(input_columns)) != len(input_columns):
            raise ValueError(f"{name} input_columns must be unique")
        if len(set(outputs)) != len(outputs):
            raise ValueError(f"{name} outputs must be unique")
        if any(column in _IDENTITY_COLUMNS for column in outputs):
            raise ValueError(f"{name} outputs must not contain identity columns")
        params = factor.get("params")
        if not isinstance(params, dict):
            raise TypeError(f"{name} params must be an object")

        identity = ["data_time", "series_tag"]
        if factor_type == "cross_section":
            identity.append("subject_id")
        missing = [column for column in input_columns if column not in frame.columns]
        if missing:
            raise ValueError(f"{name} input columns missing: {missing}")
        projected = list(dict.fromkeys([*identity, *input_columns]))
        factor_frame = self.slice_batch_frame(frame, periods)[projected].copy(deep=True)

        stdout, stderr = StringIO(), StringIO()
        try:
            with redirect_stdout(stdout), redirect_stderr(stderr):
                module, load_diagnostics = self.ensure_factor_loaded(factor)
                compute = getattr(module, "compute", None)
                if not callable(compute):
                    raise AttributeError(f"{name} must define compute(df, params, context)")
                produced = compute(factor_frame, deepcopy(params), deepcopy(context))
            if load_diagnostics:
                stdout.write(load_diagnostics["stdout"])
                stderr.write(load_diagnostics["stderr"])
            produced = self.validate_output(produced, identity, outputs, context, factor_type, name)
            target_time = pd.Timestamp(base_context["period_time"], unit="s", tz="UTC")
            produced = produced[produced["data_time"] == target_time]
            produced = produced.sort_values(identity, kind="stable").reset_index(drop=True)
            return produced, {"stdout": stdout.getvalue(), "stderr": stderr.getvalue()}
        except Exception as exc:
            exc.stdout = stdout.getvalue() + getattr(exc, "stdout", "")
            exc.stderr = stderr.getvalue() + getattr(exc, "stderr", "")
            raise

    @staticmethod
    def validate_output(produced, identity, outputs, context, factor_type, name):
        if not isinstance(produced, pd.DataFrame):
            raise TypeError(f"{name} compute result must be a pandas DataFrame")
        expected_columns = [*identity, *outputs]
        if list(produced.columns) != expected_columns:
            raise ValueError(
                f"{name} outputs mismatch: got={list(produced.columns)} want={expected_columns}"
            )
        produced = produced.copy().reset_index(drop=True)
        produced["data_time"] = pd.to_datetime(
            produced["data_time"], format="ISO8601", utc=True, errors="raise"
        )
        if produced["data_time"].isna().any():
            raise ValueError(f"{name} result contains missing data_time")
        _validate_series_tags(produced["series_tag"])
        if factor_type == "cross_section":
            if any(not isinstance(value, str) or value not in context["available_subjects"]
                   for value in produced["subject_id"]):
                raise ValueError(f"{name} result subject is outside the available universe")
        if produced.duplicated(identity).any():
            raise ValueError(f"{name} result contains duplicate {', '.join(identity)}")
        return produced

    def ensure_factor_loaded(self, factor):
        name = factor.get("name", "")
        source_hash = factor.get("source_hash", "")
        if not isinstance(source_hash, str) or not source_hash:
            raise ValueError(f"{name} source_hash is required")
        if source_hash in self.modules:
            return self.modules[source_hash], {"stdout": "", "stderr": ""}
        source_path = factor.get("source_path", "")
        if not isinstance(source_path, str) or not source_path:
            raise ValueError(f"{name} source_path is required")
        diagnostics = self.load_one({"source_hash": source_hash, "path": source_path})
        return self.modules[source_hash], diagnostics

    def decode_frame(self, meta):
        return decode_json_df(meta)

    @staticmethod
    def slice_batch_frame(frame, period_times):
        return frame[frame["data_time"].isin(period_times)]

    def load_one(self, meta):
        source_hash = meta.get("source_hash", "")
        path = Path(meta.get("path", ""))
        match = _SOURCE_HASH.fullmatch(source_hash) if isinstance(source_hash, str) else None
        if not match or not path.is_file():
            raise ValueError("factor load requires sha256 source_hash and existing path")
        if source_hash in self.modules:
            return {"stdout": "", "stderr": ""}
        raw = path.read_bytes()
        digest = hashlib.sha256(raw).hexdigest()
        if digest != match.group(1):
            raise ValueError(f"factor source hash mismatch for {path.name}")

        module_name = f"moox_factor_{digest}"
        spec = importlib.util.spec_from_file_location(module_name, path)
        if spec is None or spec.loader is None:
            raise ImportError(f"cannot load factor source {path.name}")
        module = importlib.util.module_from_spec(spec)
        stdout, stderr = StringIO(), StringIO()
        sys.modules[module_name] = module
        try:
            with redirect_stdout(stdout), redirect_stderr(stderr):
                spec.loader.exec_module(module)
        except Exception as exc:
            sys.modules.pop(module_name, None)
            raise FactorLoadError(f"{type(exc).__name__}: {exc}", stdout.getvalue(), stderr.getvalue()) from exc
        self.modules[source_hash] = module
        return {"stdout": stdout.getvalue(), "stderr": stderr.getvalue()}


class FactorLoadError(Exception):
    def __init__(self, message, stdout="", stderr=""):
        super().__init__(message)
        self.stdout = stdout
        self.stderr = stderr


def reject_legacy_fields(value):
    if isinstance(value, dict):
        for key, child in value.items():
            if key in _LEGACY_FIELDS:
                raise ValueError(f"legacy field {key} is not supported")
            if key != "params":
                reject_legacy_fields(child)
    elif isinstance(value, list):
        for child in value:
            reject_legacy_fields(child)


def validate_base_context(context):
    if not isinstance(context, dict):
        raise TypeError("execution context must be an object")
    if not isinstance(context.get("period_time"), int) or isinstance(context.get("period_time"), bool) or context["period_time"] <= 0:
        raise ValueError("context period_time must be a positive integer")
    if not isinstance(context.get("frequency"), str) or not context["frequency"]:
        raise ValueError("context frequency is required")


def validate_period_times(raw, lookback, period_time, name):
    if not isinstance(raw, list) or len(raw) != lookback:
        raise ValueError(f"{name} context period_times must contain lookback_periods timestamps")
    try:
        periods = pd.to_datetime(raw, format="ISO8601", utc=True, errors="raise")
    except Exception as exc:
        raise ValueError(f"{name} context period_times must be RFC3339 timestamps") from exc
    if periods.isna().any() or periods.has_duplicates or not periods.is_monotonic_increasing:
        raise ValueError(f"{name} context period_times must be unique and ascending")
    target = pd.Timestamp(period_time, unit="s", tz="UTC")
    if periods[-1] != target:
        raise ValueError(f"{name} context period_times must end at period_time")
    return periods


def validate_factor_context(context, factor_type, df, name):
    if factor_type == "timeseries":
        if not isinstance(context.get("subject_id"), str) or not context["subject_id"]:
            raise ValueError("context subject_id is required for timeseries")
        if "subject_id" in df and any(df["subject_id"] != context["subject_id"]):
            raise ValueError(f"{name} timeseries frame contains another subject")
        return
    for key in ("expected_subjects", "available_subjects"):
        values = context.get(key)
        if not isinstance(values, list) or any(not isinstance(v, str) or not v for v in values):
            raise ValueError(f"context {key} must be a list of subjects")
        if len(values) != len(set(values)):
            raise ValueError(f"context {key} contains duplicate subjects")
    expected, available = set(context["expected_subjects"]), set(context["available_subjects"])
    if not available.issubset(expected):
        raise ValueError("cross_section available subjects are outside the expected universe")
    if "subject_id" not in df or set(df["subject_id"]) != available:
        raise ValueError("cross_section input does not match the available universe")


def encode_frame_result(frame):
    rows = []
    for values in frame.itertuples(index=False, name=None):
        row = []
        for column, value in zip(frame.columns, values):
            if column == "data_time":
                value = value.isoformat().replace("+00:00", "Z")
            elif value is pd.NA or value is pd.NaT:
                value = None
            elif hasattr(value, "item"):
                value = value.item()
            if isinstance(value, float) and not math.isfinite(value):
                value = None
            row.append(value)
        rows.append(row)
    return {"columns": list(frame.columns), "rows": rows}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--factors-dir", required=True)
    parser.add_argument("--encoding", default="json")
    args = parser.parse_args()

    worker = FactorWorker(args.factors_dir, args.encoding)
    try:
        write_frame(sys.stdout.buffer, TYPE_HELLO, worker.ready_meta())
        while True:
            frame_type, meta, _payload = read_frame(sys.stdin.buffer)
            if frame_type == TYPE_LOAD and "path" in meta:
                try:
                    diagnostics = worker.load_one(meta)
                    write_frame(
                        sys.stdout.buffer,
                        TYPE_RESULT,
                        {"id": meta.get("id", ""), "status": "loaded", "diagnostics": diagnostics},
                    )
                except Exception as exc:  # noqa: BLE001
                    write_frame(
                        sys.stdout.buffer,
                        TYPE_ERROR,
                        {
                            "id": meta.get("id", ""),
                            "error_type": type(exc).__name__,
                            "message": str(exc),
                            "diagnostics": {
                                "stdout": getattr(exc, "stdout", ""),
                                "stderr": getattr(exc, "stderr", ""),
                            },
                        },
                    )
                continue
            if frame_type != TYPE_RUN:
                continue
            try:
                response = worker.execute_request(meta)
                write_frame(sys.stdout.buffer, TYPE_RESULT, response)
            except Exception as exc:  # noqa: BLE001 - malformed protocol fails the request.
                traceback.print_exc(file=sys.stderr)
                write_frame(
                    sys.stdout.buffer,
                    TYPE_ERROR,
                    {"id": meta.get("id", ""), "error_type": type(exc).__name__, "message": str(exc)},
                )
    except EOFError:
        return
    except Exception as exc:  # noqa: BLE001
        traceback.print_exc(file=sys.stderr)
        write_frame(sys.stdout.buffer, TYPE_ERROR, {"id": "", "error_type": type(exc).__name__, "message": str(exc)})


if __name__ == "__main__":
    main()
