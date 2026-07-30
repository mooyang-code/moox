import argparse
import hashlib
import importlib.util
import sys
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

        stdout, stderr = StringIO(), StringIO()
        with redirect_stdout(stdout), redirect_stderr(stderr):
            factor = meta.get("factor")
            if not isinstance(factor, dict):
                raise TypeError("factor must be an object")
            name = factor["name"]
            inputs = list(factor.get("input_columns", []))
            expected_outputs = list(factor.get("outputs", []))
            params = factor.get("params", {})
            if not isinstance(params, dict):
                raise TypeError(f"{name} params must be an object")
            if len(set(expected_outputs)) != len(expected_outputs):
                raise ValueError(f"{name} outputs must be unique")

            module = self.factors[name]
            compute = getattr(module, "compute", None)
            if not callable(compute):
                raise AttributeError(f"{name} must define compute(df, params)")

            factor_df = df[["data_time", "series_tag", *inputs]].copy(deep=False)
            produced = compute(factor_df, params)
            if not isinstance(produced, pd.DataFrame):
                raise TypeError(f"{name} compute result must be a pandas DataFrame")
            expected_columns = {"data_time", "series_tag", *expected_outputs}
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
            if produced.duplicated(["data_time", "series_tag"]).any():
                raise ValueError(f"{name} result contains duplicate data_time, series_tag")
            produced = produced[
                (produced["data_time"] >= target_start)
                & (produced["data_time"] < target_end)
            ].sort_values(["data_time", "series_tag"], kind="stable").reset_index(drop=True)

        return encode_json_results(
            meta.get("id", ""), produced,
            {"stdout": stdout.getvalue(), "stderr": stderr.getvalue()},
        )

    def decode_frame(self, meta):
        return decode_json_df(meta)

    def load_one(self, meta):
        name = meta.get("logical_id") or meta.get("name")
        path = Path(meta.get("path", ""))
        expected_hash = meta.get("source_hash", "")
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
