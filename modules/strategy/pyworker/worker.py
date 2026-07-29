import importlib.util
import inspect
import os
try:
    import pandas as pd
except ImportError:  # pragma: no cover - production image declares pandas.
    pd = None
from pathlib import Path
import sys
import traceback

runtime_python = os.environ.get("MOOX_PYTHON_RUNTIME_PATH")
if runtime_python:
    sys.path.insert(0, runtime_python)
else:
    worker_path = Path(__file__).resolve()
    runtime_candidates = (
        worker_path.parents[1] / "python-runtime",
        worker_path.parents[3] / "packages" / "pyruntime" / "python",
    )
    runtime_python = next(
        (path for path in runtime_candidates if path.is_dir()),
        runtime_candidates[0],
    )
    sys.path.insert(0, str(runtime_python))

sys.path.insert(0, str(Path(__file__).parents[1] / "pysdk"))

from moox_strategy import validate_output
from moox_pyruntime.protocol import (
    TYPE_ERROR as ERROR,
    TYPE_HELLO as HELLO,
    TYPE_LOAD as LOAD,
    TYPE_RESULT as RESULT,
    TYPE_RUN as RUN,
    TYPE_DRAIN,
    read_frame,
    write_frame,
)

modules = {}


def validate_result(value):
    try:
        return validate_output(value)
    except ValueError as exc:
        raise ValueError(f"strategy output: {exc}") from exc


def json_safe(value):
    if isinstance(value, dict):
        return {str(key): json_safe(item) for key, item in value.items()}
    if isinstance(value, (list, tuple)):
        return [json_safe(item) for item in value]
    item = getattr(value, "item", None)
    if callable(item):
        try:
            return json_safe(item())
        except Exception:
            pass
    return value


def _validate_entrypoint(fn):
    parameters = list(inspect.signature(fn).parameters.values())
    if len(parameters) != 3 or any(
        parameter.kind
        not in (inspect.Parameter.POSITIONAL_ONLY, inspect.Parameter.POSITIONAL_OR_KEYWORD)
        for parameter in parameters
    ):
        raise ValueError("strategy entrypoint must accept exactly context, data, params")


def load(meta):
    spec = importlib.util.spec_from_file_location(
        "moox_strategy_" + meta["source_hash"],
        meta["path"],
    )
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    entrypoint = meta.get("entrypoint") or "run"
    if ":" in entrypoint:
        _, entrypoint = entrypoint.split(":", 1)
    fn = getattr(module, entrypoint, None)
    if fn is None or not callable(fn):
        raise ValueError(f"strategy module must export callable {entrypoint}")
    _validate_entrypoint(fn)
    modules[(meta["logical_id"], meta["source_hash"])] = (module, fn)
    while len(modules) > 8:
        modules.pop(next(iter(modules)))


def run(meta):
    _, fn = modules[(meta["logical_id"], meta["source_hash"])]
    if pd is None:
        raise RuntimeError("strategy worker requires pandas")
    data = pd.DataFrame(meta.get("data", []))
    for column in ("time", "candle_begin_time", "candle_end_time", "available_at"):
        if column in data.columns:
            data[column] = pd.to_datetime(data[column], utc=True)
    result = fn(meta["context"], data, meta.get("params", {}))
    return json_safe(validate_result(result))


def serve():
    write_frame(
        sys.stdout.buffer,
        HELLO,
        {
            "protocol_version": "moox.py/v1",
            "worker_version": "1",
            "python_version": "3",
            "runtime_env_hash": "",
            "encodings": ["json"],
        },
    )
    try:
        while True:
            typ, meta, payload = read_frame(sys.stdin.buffer)
            try:
                if typ == LOAD:
                    load(meta)
                    write_frame(sys.stdout.buffer, RESULT, {"ok": True})
                elif typ == RUN:
                    write_frame(
                        sys.stdout.buffer,
                        RESULT,
                        {"ok": True, "result": run(meta)},
                    )
                elif typ == TYPE_DRAIN:
                    break
            except Exception as exc:
                traceback.print_exc(file=sys.stderr)
                write_frame(
                    sys.stdout.buffer,
                    ERROR,
                    {"error_type": type(exc).__name__, "message": str(exc)},
                )
    except EOFError:
        pass


if __name__ == "__main__":
    serve()
