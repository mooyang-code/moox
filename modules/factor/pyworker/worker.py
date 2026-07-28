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
)

pd.options.mode.copy_on_write = True


class FactorWorker:
    def __init__(self, factors_dir, encoding="json"):
        self.factors_dir = Path(factors_dir)
        self.encoding = encoding
        self.factors = {}
        self.load_errors = {}

    def load_modules(self):
        self.load_errors = {}
        self.factors = self._load_modules_from(self.factors_dir)

    def ready_meta(self):
        return {
            "status": "ready",
            "protocol_version": "moox.py/v1",
            "worker_version": "factor-v1",
            "python_version": sys.version.split()[0],
            "runtime_env_hash": "",
            "encoding": self.encoding,
            "encodings": ["json"],
            "factors": sorted(self.factors.keys()),
            "load_errors": self.load_errors,
        }

    def execute_request(self, meta):
        df = self.decode_frame(meta)
        results = {}
        target_start = pd.Timestamp(meta["target_start_time"])
        target_end = pd.Timestamp(meta["target_end_time"])
        target_mask = (
            (df["data_time"] >= target_start)
            & (df["data_time"] < target_end)
        )

        stdout, stderr = StringIO(), StringIO()
        with redirect_stdout(stdout), redirect_stderr(stderr):
            for factor in meta.get("factors", []):
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

                factor_df = df[["data_time", *inputs]].copy(deep=False)
                produced = compute(factor_df, params)
                if not isinstance(produced, dict):
                    raise TypeError(f"{name} compute result must be a dict")
                if set(produced) != set(expected_outputs):
                    raise ValueError(
                        f"{name} outputs mismatch: got={sorted(produced)} "
                        f"want={sorted(expected_outputs)}"
                    )
                for output in expected_outputs:
                    if output in results:
                        raise ValueError(f"duplicate factor output {output}")
                    series = produced[output]
                    if not isinstance(series, pd.Series):
                        raise TypeError(f"{name}.{output} must be a pandas Series")
                    if len(series) != len(df.index) or not series.index.equals(df.index):
                        raise ValueError(f"{name}.{output} must align with input rows")
                    results[output] = series.loc[target_mask].tolist()

        return encode_json_results(
            meta.get("id", ""), results,
            {"stdout": stdout.getvalue(), "stderr": stderr.getvalue()},
        )

    def decode_frame(self, meta):
        return decode_json_df(meta)

    def load_one(self, meta):
        name = meta.get("logical_id") or meta.get("name")
        path = Path(meta.get("path", ""))
        expected_hash = meta.get("source_hash", "")
        if not name or not path.is_file():
            raise ValueError("factor load requires logical_id and existing path")
        raw = path.read_bytes()
        if expected_hash and hashlib.sha256(raw).hexdigest() != expected_hash:
            raise ValueError(f"factor source hash mismatch for {name}")
        spec = importlib.util.spec_from_file_location(f"moox_factor_{name}_{abs(hash(path))}", path)
        if spec is None or spec.loader is None:
            raise ImportError(f"cannot load factor module {name}")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        self.factors[name] = module

    def _load_modules_from(self, directory):
        modules = {}
        errors = {}
        if not directory.exists():
            return modules
        for path in sorted(directory.glob("*.py")):
            if not path.name[0].isalpha():
                continue
            name = path.stem
            spec = importlib.util.spec_from_file_location(f"moox_factor_{name}_{abs(hash(path))}", path)
            module = importlib.util.module_from_spec(spec)
            try:
                spec.loader.exec_module(module)
            except Exception as exc:  # noqa: BLE001 - one bad draft must not kill the worker.
                traceback.print_exc(file=sys.stderr)
                errors[name] = f"{type(exc).__name__}: {exc}"
                continue
            modules[name] = module
        self.load_errors.update(errors)
        return modules


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--factors-dir", required=True)
    parser.add_argument("--encoding", default="json")
    args = parser.parse_args()

    worker = FactorWorker(args.factors_dir, args.encoding)
    try:
        worker.load_modules()
        write_frame(sys.stdout.buffer, TYPE_HELLO, worker.ready_meta())
        while True:
            frame_type, meta, _payload = read_frame(sys.stdin.buffer)
            if frame_type == TYPE_LOAD and "path" in meta:
                try:
                    worker.load_one(meta)
                    write_frame(sys.stdout.buffer, TYPE_RESULT, {"id": meta.get("id", ""), "status": "loaded"})
                except Exception as exc:  # noqa: BLE001
                    write_frame(sys.stdout.buffer, TYPE_ERROR, {"id": meta.get("id", ""), "error_type": type(exc).__name__, "message": str(exc)})
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
