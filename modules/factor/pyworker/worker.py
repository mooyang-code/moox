import argparse
import hashlib
import importlib.util
import sys
from copy import deepcopy
import traceback
from contextlib import redirect_stdout, redirect_stderr
from io import StringIO
from pathlib import Path
import os
import pandas as pd

runtime_python = os.environ.get("MOOX_PYTHON_RUNTIME_PATH")
if runtime_python:
    sys.path.insert(0, runtime_python)
else:
    sys.path.insert(0, str(Path(__file__).resolve().parents[3] / "packages" / "pyruntime" / "python"))

from codec import (
    TYPE_ERROR,
    TYPE_HELLO,
    TYPE_LOAD,
    TYPE_RESULT,
    TYPE_RUN,
    decode_json_df,
    encode_json_results,
    encode_json_batch_results,
    encode_result_rows,
    read_frame,
    write_frame,
    _validate_series_tags,
)

pd.options.mode.copy_on_write = True


class FactorWorker:
    def __init__(self, factors_dir, encoding="json"):
        self.factors_dir = Path(factors_dir)
        self.encoding = encoding
        self.factors = {}
        self.factor_hashes = {}

    def ready_meta(self):
        return {
            "status": "ready",
            "protocol_version": "moox.py/v1",
            "worker_version": "factor-v1",
            "python_version": sys.version.split()[0],
            "runtime_env_hash": "",
            "encoding": self.encoding,
            "encodings": ["json"],
            "factors": [],
            "load_errors": {},
        }

    def execute_request(self, meta):
        df = self.decode_frame(meta)
        target_start = pd.Timestamp(meta["target_start_time"])
        target_end = pd.Timestamp(meta["target_end_time"])

        if isinstance(meta.get("factors"), list):
            return self.execute_batch_request(meta, df, target_start, target_end)

        stdout, stderr = StringIO(), StringIO()
        with redirect_stdout(stdout), redirect_stderr(stderr):
            factor = meta.get("factor")
            if not isinstance(factor, dict):
                raise TypeError("factor must be an object")
            produced = self.compute_factor(df, factor, target_start, target_end, meta.get("context"))

        return encode_json_results(
            meta.get("id", ""), produced,
            {"stdout": stdout.getvalue(), "stderr": stderr.getvalue()},
        )

    def execute_batch_request(self, meta, df, target_start, target_end):
        items = []
        for item in meta["factors"]:
            if not isinstance(item, dict):
                raise TypeError("batch factor item must be an object")
            task_id = item.get("task_id", "")
            binding_id = item.get("binding_id", "")
            factor = item.get("factor")
            if not task_id or not binding_id or not isinstance(factor, dict):
                raise ValueError("batch factor item requires task_id, binding_id, and factor")
            stdout, stderr = StringIO(), StringIO()
            try:
                item_start = pd.Timestamp(item.get("target_start_time", target_start))
                item_end = pd.Timestamp(item.get("target_end_time", target_end))
                item_df = self.slice_batch_frame(
                    df, int(item.get("lookback_periods", 0) or 0), item_start, item_end
                )
                with redirect_stdout(stdout), redirect_stderr(stderr):
                    self.ensure_factor_loaded(factor)
                    produced = self.compute_factor(item_df, factor, item_start, item_end,
                                                   item.get("context", meta.get("context")))
                items.append({
                    "task_id": task_id, "binding_id": binding_id, "ok": True,
                    "results": encode_result_rows(produced),
                    "logs": {"stdout": stdout.getvalue(), "stderr": stderr.getvalue()},
                })
            except Exception as exc:  # noqa: BLE001 - isolate one factor in the batch.
                items.append({
                    "task_id": task_id, "binding_id": binding_id, "ok": False,
                    "error_type": type(exc).__name__, "message": str(exc),
                    "logs": {"stdout": stdout.getvalue(), "stderr": stderr.getvalue()},
                })
        return encode_json_batch_results(meta.get("id", ""), items)

    @staticmethod
    def slice_batch_frame(df, lookback_periods, target_start, target_end):
        """Give each member the same frame it would receive outside a batch.

        The shared frame uses the largest lookback. Keep all target rows, plus
        only the last N-1 distinct periods before that member's target window.
        """
        if lookback_periods <= 0:
            return df
        target = (df["data_time"] >= target_start) & (df["data_time"] < target_end)
        history = df[df["data_time"] < target_start]
        if not history.empty:
            times = history["data_time"].drop_duplicates().sort_values()
            keep = max(0, lookback_periods - 1)
            if keep:
                history = history[history["data_time"].isin(times.iloc[-keep:])]
            else:
                history = history.iloc[0:0]
        return pd.concat([history, df[target]], ignore_index=True).sort_values(
            ["data_time", "series_tag"], kind="stable"
        ).reset_index(drop=True)

    def compute_factor(self, df, factor, target_start, target_end, context):
        name = factor.get("name", "")
        if not name:
            raise ValueError("factor name is required")
        factor_type = factor.get("factor_type")
        if factor_type not in {"timeseries", "cross_section"}:
            raise ValueError("factor_type must be timeseries or cross_section")
        self.validate_context(context, factor_type, df)
        identity = ["data_time", "series_tag"]
        if factor_type == "cross_section":
            identity.append("subject_id")
        inputs = list(factor.get("input_columns", []))
        expected_outputs = list(factor.get("outputs", []))
        params = factor.get("params", {})
        if not isinstance(params, dict):
            raise TypeError(f"{name} params must be an object")
        if len(set(expected_outputs)) != len(expected_outputs):
            raise ValueError(f"{name} outputs must be unique")
        missing = [column for column in inputs if column not in df.columns]
        if missing:
            raise ValueError(f"{name} input columns missing: {missing}")
        module = self.factors[name]
        compute = getattr(module, "compute", None)
        if not callable(compute):
            raise AttributeError(f"{name} must define compute(df, params, context)")
        factor_df = df[list(dict.fromkeys([*identity, *inputs]))].copy(deep=True)
        produced = compute(factor_df, deepcopy(params), deepcopy(context))
        if not isinstance(produced, pd.DataFrame):
            raise TypeError(f"{name} compute result must be a pandas DataFrame")
        expected_columns = {*identity, *expected_outputs}
        if set(produced.columns) != expected_columns or len(produced.columns) != len(expected_columns):
            raise ValueError(
                f"{name} outputs mismatch: got={sorted(produced.columns)} "
                f"want={sorted(expected_columns)}"
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
        return produced[
            (produced["data_time"] >= target_start)
            & (produced["data_time"] < target_end)
        ].sort_values(identity, kind="stable").reset_index(drop=True)

    @staticmethod
    def validate_context(context, factor_type, df):
        if not isinstance(context, dict):
            raise TypeError("execution context must be an object")
        if not isinstance(context.get("period_time"), int) or context["period_time"] <= 0:
            raise ValueError("context period_time must be a positive integer")
        if not isinstance(context.get("frequency"), str) or not context["frequency"]:
            raise ValueError("context frequency is required")
        if not isinstance(context.get("input_contract_version"), str):
            raise ValueError("context input_contract_version is required")
        if factor_type == "timeseries":
            if not isinstance(context.get("subject_id"), str) or not context["subject_id"]:
                raise ValueError("context subject_id is required for timeseries")
            if "subject_id" in df and any(df["subject_id"] != context["subject_id"]):
                raise ValueError("timeseries input contains another subject")
            return
        for name in ("expected_subjects", "available_subjects", "missing_subjects"):
            values = context.get(name)
            if not isinstance(values, list) or any(not isinstance(v, str) or not v for v in values):
                raise ValueError(f"context {name} must be a list of subjects")
            if len(values) != len(set(values)):
                raise ValueError(f"context {name} contains duplicate subjects")
        expected, available, missing = (set(context[k]) for k in
                                       ("expected_subjects", "available_subjects", "missing_subjects"))
        if not expected or available & missing or available | missing != expected:
            raise ValueError("context subject universe partition is invalid")
        if "subject_id" not in df or set(df["subject_id"]) != available:
            raise ValueError("cross_section input does not match the available universe")

    def ensure_factor_loaded(self, factor):
        name = factor.get("name", "")
        if not name:
            raise ValueError("factor name is required")
        source_hash = factor.get("source_hash", "")
        if name in self.factors and (not source_hash or self.factor_hashes.get(name) == source_hash):
            return
        path = factor.get("source_path", "")
        if not source_hash or not path:
            raise ValueError(f"factor {name} is not loaded and has no source metadata")
        self.load_one({"logical_id": name, "path": path, "source_hash": source_hash})

    def decode_frame(self, meta):
        return decode_json_df(meta)

    def load_one(self, meta):
        name = meta.get("logical_id") or meta.get("name")
        path = Path(meta.get("path", ""))
        expected_hash = meta.get("source_hash", "")
        if name:
            # Never retain an older module after a failed replacement load.
            self.factors.pop(name, None)
            self.factor_hashes.pop(name, None)
        if not name or not expected_hash or not path.is_file():
            raise ValueError("factor load requires logical_id, source_hash, and existing path")
        raw = path.read_bytes()
        if hashlib.sha256(raw).hexdigest() != expected_hash:
            raise ValueError(f"factor source hash mismatch for {name}")
        spec = importlib.util.spec_from_file_location(f"moox_factor_{name}_{abs(hash(path))}", path)
        if spec is None or spec.loader is None:
            raise ImportError(f"cannot load factor module {name}")
        module = importlib.util.module_from_spec(spec)
        stdout, stderr = StringIO(), StringIO()
        try:
            with redirect_stdout(stdout), redirect_stderr(stderr):
                spec.loader.exec_module(module)
        except Exception as exc:
            raise FactorLoadError(
                f"{type(exc).__name__}: {exc}",
                stdout.getvalue(),
                stderr.getvalue(),
            ) from exc
        self.factors[name] = module
        self.factor_hashes[name] = expected_hash
        return {"stdout": stdout.getvalue(), "stderr": stderr.getvalue()}


class FactorLoadError(Exception):
    def __init__(self, message, stdout="", stderr=""):
        super().__init__(message)
        self.stdout = stdout
        self.stderr = stderr


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
            except Exception as exc:  # noqa: BLE001 - factor errors must be reported to Go.
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
