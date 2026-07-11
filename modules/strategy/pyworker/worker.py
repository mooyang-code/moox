import importlib.util
import json
import sys
import traceback
from contextlib import redirect_stdout, redirect_stderr
from io import StringIO

MAGIC=b"MX"; HELLO=1; LOAD=2; RUN=3; RESULT=4; ERROR=5
MAX_META=4*1024*1024; MAX_PAYLOAD=64*1024*1024
def read_exact(n):
    b=sys.stdin.buffer.read(n)
    if len(b)!=n: raise EOFError()
    return b
def read_frame():
    if read_exact(2)!=MAGIC: raise ValueError("invalid magic")
    typ=read_exact(1)[0]; ml=int.from_bytes(read_exact(4),"big");
    if ml>MAX_META: raise ValueError("meta too large")
    meta=json.loads(read_exact(ml)); pl=int.from_bytes(read_exact(8),"big");
    if pl>MAX_PAYLOAD: raise ValueError("payload too large")
    payload=read_exact(pl) if pl else b""; return typ,meta,payload
def write_frame(typ,meta,payload=b""):
    raw=json.dumps(meta,separators=(",",":"),ensure_ascii=False).encode(); out=MAGIC+bytes([typ])+len(raw).to_bytes(4,"big")+raw+len(payload).to_bytes(8,"big")+payload; sys.stdout.buffer.write(out); sys.stdout.buffer.flush()
modules={}
def load(meta):
    spec=importlib.util.spec_from_file_location("moox_strategy_"+meta["source_hash"],meta["path"]); mod=importlib.util.module_from_spec(spec); spec.loader.exec_module(mod); 
    if not hasattr(mod,"run"): raise ValueError("strategy module must export run")
    modules[(meta["logical_id"],meta["source_hash"])]=mod
    while len(modules)>8:
        modules.pop(next(iter(modules)))
def run(meta):
    mod=modules[(meta["logical_id"],meta["source_hash"])]
    output=StringIO(); errors=StringIO()
    with redirect_stdout(output),redirect_stderr(errors): result=mod.run(meta["context"],meta.get("data",[]),meta.get("params",{}),meta.get("state",{}))
    if not isinstance(result,dict): raise ValueError("strategy output must be an object")
    result["logs"]={"stdout":output.getvalue(),"stderr":errors.getvalue()}; return result
write_frame(HELLO,{"protocol_version":"moox.py/v1","worker_version":"1","python_version":"3","runtime_env_hash":"","encodings":["json"]})
try:
    while True:
        typ,meta,payload=read_frame()
        try:
            if typ==LOAD: load(meta); write_frame(RESULT,{"ok":True})
            elif typ==RUN: write_frame(RESULT,{"ok":True,"result":run(meta)})
            elif typ==7: break
        except Exception as exc:
            traceback.print_exc(file=sys.stderr); write_frame(ERROR,{"error_type":type(exc).__name__,"message":str(exc)})
except EOFError: pass
