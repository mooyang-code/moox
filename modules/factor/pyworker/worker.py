import argparse
import importlib.util
import sys
import time
import traceback
from pathlib import Path

from codec import (
    FRAME_ERROR,
    FRAME_READY,
    FRAME_REQUEST,
    FRAME_RESPONSE,
    decode_json_df,
    encode_json_results,
    read_frame,
    write_frame,
)


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
        return {
            "status": "ready",
            "encoding": self.encoding,
            "encodings": ["json"],
            "factors": sorted(self.factors.keys()),
            "sections": sorted(self.sections.keys()),
            "load_errors": self.load_errors,
        }

    def execute_request(self, meta):
        # Factor definitions are edited through RPC/CLI and materialized as .py
        # files. Reload before each task so saved sources participate without a
        # full service restart.
        self.load_modules()
        started = time.perf_counter()
        df = decode_json_df(meta)
        results = {}
        per_factor_ms = {}
        max_tail = 1
        modules = self.sections if meta.get("kind") == "cross_section" else self.factors

        for factor in meta.get("factors", []):
            name = factor["name"]
            params = factor.get("params", [])
            tail = int(factor.get("writeback_bars") or meta.get("tail") or 1)
            max_tail = max(max_tail, tail)
            mod = modules[name]
            factor_started = time.perf_counter()
            if hasattr(mod, "signal_multi_params"):
                out = mod.signal_multi_params(df.copy(), params)
                for param, series in out.items():
                    results[f"{name}_{param}"] = _tail_values(series, tail)
            else:
                for param in params:
                    column = f"{name}_{param}"
                    out_df = mod.signal(df.copy(), param, column)
                    results[column] = _tail_values(out_df[column], tail)
            per_factor_ms[name] = int((time.perf_counter() - factor_started) * 1000)

        elapsed_ms = int((time.perf_counter() - started) * 1000)
        return encode_json_results(meta.get("id", ""), results, max_tail, per_factor_ms, elapsed_ms)

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
        write_frame(sys.stdout.buffer, FRAME_READY, worker.ready_meta())
        while True:
            frame_type, meta, _payload = read_frame(sys.stdin.buffer)
            if frame_type != FRAME_REQUEST:
                continue
            try:
                response = worker.execute_request(meta)
                write_frame(sys.stdout.buffer, FRAME_RESPONSE, response)
            except Exception as exc:  # noqa: BLE001 - factor errors must be reported to Go.
                traceback.print_exc(file=sys.stderr)
                write_frame(
                    sys.stdout.buffer,
                    FRAME_ERROR,
                    {"id": meta.get("id", ""), "error_type": type(exc).__name__, "message": str(exc)},
                )
    except EOFError:
        return
    except Exception as exc:  # noqa: BLE001
        traceback.print_exc(file=sys.stderr)
        write_frame(sys.stdout.buffer, FRAME_ERROR, {"id": "", "error_type": type(exc).__name__, "message": str(exc)})


if __name__ == "__main__":
    main()
