import importlib.util
import os
try:
    import pandas as pd
except ImportError:  # pragma: no cover - production image declares pandas.
    pd = None
from pathlib import Path
import re
import sys
import traceback
from decimal import Decimal, InvalidOperation
from contextlib import redirect_stdout, redirect_stderr
from io import StringIO

DECIMAL_RE=re.compile(r"^-?(0|[1-9][0-9]*)(\.[0-9]+)?$")

runtime_python = os.environ.get("MOOX_PYTHON_RUNTIME_PATH")
if runtime_python:
    sys.path.insert(0, runtime_python)
else:
    worker_path = Path(__file__).resolve()
    runtime_candidates = (
        worker_path.parents[1] / "python-runtime",
        worker_path.parents[3] / "packages" / "pyruntime" / "python",
    )
    runtime_python = next((path for path in runtime_candidates if path.is_dir()), runtime_candidates[0])
    sys.path.insert(0, str(runtime_python))
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
modules={}
def validate_result(value):
    if not isinstance(value,dict) or value.get("action") not in ("hold","rebalance"):
        raise ValueError("strategy output action must be hold or rebalance")
    if not isinstance(value.get("next_state"),dict):
        raise ValueError("strategy output next_state must be an object")
    targets=value.get("targets",[])
    if not isinstance(targets,list):
        raise ValueError("strategy output targets must be a list")
    if value["action"] == "rebalance" and not targets:
        raise ValueError("strategy rebalance targets are required")
    seen=set()
    for target in targets:
        if not isinstance(target,dict):
            raise ValueError("strategy target must be an object")
        if "target_weight" in target:
            raise ValueError("strategy target_weight is not supported")
        instrument=target.get("instrument_id")
        symbol=target.get("symbol")
        if (
            not isinstance(instrument,str)
            or not instrument.strip()
            or not isinstance(symbol,str)
            or not symbol.strip()
        ):
            raise ValueError("strategy target instrument_id and symbol are required")
        if symbol in seen:
            raise ValueError("strategy target symbols must be unique")
        quantity=target.get("target_quantity")
        if not isinstance(quantity,str):
            raise ValueError("strategy target_quantity must be decimal")
        if len(quantity) > 256 or not DECIMAL_RE.fullmatch(quantity):
            raise ValueError("strategy target_quantity must be decimal")
        try:
            if not Decimal(quantity).is_finite():
                raise ValueError("strategy target_quantity must be finite")
        except (InvalidOperation, ValueError):
            raise ValueError("strategy target_quantity must be decimal")
        seen.add(symbol)
    return value

def json_safe(value):
    if isinstance(value, dict):
        return {str(k): json_safe(v) for k, v in value.items()}
    if isinstance(value, (list, tuple)):
        return [json_safe(v) for v in value]
    item = getattr(value, "item", None)
    if callable(item):
        try:
            return json_safe(item())
        except Exception:
            pass
    return value
def load(meta):
    spec=importlib.util.spec_from_file_location("moox_strategy_"+meta["source_hash"],meta["path"]); mod=importlib.util.module_from_spec(spec); spec.loader.exec_module(mod); 
    entrypoint = meta.get("entrypoint") or "run"
    if ":" in entrypoint:
        _, entrypoint = entrypoint.split(":", 1)
    fn = getattr(mod, entrypoint, None)
    if fn is None or not callable(fn): raise ValueError(f"strategy module must export callable {entrypoint}")
    modules[(meta["logical_id"],meta["source_hash"])]=(mod, fn)
    while len(modules)>8:
        modules.pop(next(iter(modules)))
def run(meta):
    _, fn=modules[(meta["logical_id"],meta["source_hash"])]
    output=StringIO(); errors=StringIO()
    if pd is None:
        raise RuntimeError("strategy worker requires pandas")
    data = pd.DataFrame(meta.get("data", []))
    for column in ("candle_begin_time", "candle_end_time", "available_at"):
        if column in data.columns:
            data[column] = pd.to_datetime(data[column], utc=True)
    with redirect_stdout(output),redirect_stderr(errors): result=fn(meta["context"],data,meta.get("params",{}),meta.get("state",{}))
    result=json_safe(validate_result(result))
    result["logs"]={"stdout":output.getvalue(),"stderr":errors.getvalue()}; return result
def serve():
    write_frame(sys.stdout.buffer, HELLO,{"protocol_version":"moox.py/v1","worker_version":"1","python_version":"3","runtime_env_hash":"","encodings":["json"]})
    try:
        while True:
            typ,meta,payload=read_frame(sys.stdin.buffer)
            try:
                if typ==LOAD: load(meta); write_frame(sys.stdout.buffer, RESULT,{"ok":True})
                elif typ==RUN: write_frame(sys.stdout.buffer, RESULT,{"ok":True,"result":run(meta)})
                elif typ==TYPE_DRAIN: break
            except Exception as exc:
                traceback.print_exc(file=sys.stderr); write_frame(sys.stdout.buffer, ERROR,{"error_type":type(exc).__name__,"message":str(exc)})
    except EOFError: pass

if __name__ == "__main__":
    serve()
