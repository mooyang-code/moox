import argparse
import hashlib
import importlib.util
import sys
import time
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
    def __init__(self, factors_dir, sections_dir, encoding="auto"):
        self.factors_dir = Path(factors_dir)
        self.sections_dir = Path(sections_dir)
        self.encoding = encoding
        self.factors = {}
        self.sections = {}
        self.load_errors = {}

    def load_modules(self):
        self.load_errors = {}
        self.factors = self._load_modules_from(self.factors_dir)
        self.sections = self._load_modules_from(self.sections_dir)

    def ready_meta(self):
        encodings = ["json"]
        try:
            import pyarrow  # noqa: F401
            encodings.append("arrow_mmap")
        except ImportError:
            pass
        return {
            "status": "ready",
            "protocol_version": "moox.py/v1",
            "worker_version": "factor-v1",
            "python_version": sys.version.split()[0],
            "runtime_env_hash": "",
            "encoding": self.encoding,
            "encodings": encodings,
            "factors": sorted(self.factors.keys()),
            "sections": sorted(self.sections.keys()),
            "load_errors": self.load_errors,
        }

    def execute_request(self, meta):
        started = time.perf_counter()
        df = self.decode_frame(meta)
        results = {}
        result_tails = {}
        per_factor_ms = {}
        max_tail = 1
        modules = self.sections if meta.get("kind") == "cross_section" else self.factors

        stdout, stderr = StringIO(), StringIO()
        with redirect_stdout(stdout), redirect_stderr(stderr):
            for factor in meta.get("factors", []):
                name = factor["name"]
                params = factor.get("params", [])
                tail = int(factor.get("writeback_bars") or meta.get("tail") or 1)
                max_tail = max(max_tail, tail)
                mod = modules[name]
                factor_started = time.perf_counter()
                if hasattr(mod, "signal_multi_params"):
                    out = mod.signal_multi_params(df.copy(deep=False), params)
                    for param, series in out.items():
                        results[f"{name}_{param}"] = _tail_values(series, tail)
                        result_tails[f"{name}_{param}"] = tail
                else:
                    for param in params:
                        column = f"{name}_{param}"
                        out_df = mod.signal(df.copy(deep=False), param, column)
                        results[column] = _tail_values(out_df[column], tail)
                        result_tails[column] = tail
                per_factor_ms[name] = int((time.perf_counter() - factor_started) * 1000)

        elapsed_ms = int((time.perf_counter() - started) * 1000)
        return encode_json_results(meta.get("id", ""), results, max_tail, per_factor_ms, elapsed_ms, result_tails, {"stdout": stdout.getvalue(), "stderr": stderr.getvalue()})

    def decode_frame(self, meta):
        if meta.get("encoding") == "arrow_mmap" and meta.get("snapshot_path"):
            from moox_pyruntime.arrow import open_mmap
            with open_mmap(meta["snapshot_path"]) as reader:
                return reader.read_all().to_pandas()
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
        if name in self.sections:
            self.sections[name] = module
        else:
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


def _tail_values(series, tail):
    if hasattr(series, "tail"):
        return series.tail(tail)
    return list(series)[-tail:]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--factors-dir", required=True)
    parser.add_argument("--sections-dir", required=True)
    parser.add_argument("--encoding", default="auto")
    args = parser.parse_args()

    worker = FactorWorker(args.factors_dir, args.sections_dir, args.encoding)
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
