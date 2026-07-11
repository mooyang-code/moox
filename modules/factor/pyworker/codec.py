import json
import struct

import pandas as pd


MAGIC = b"MX"
MAX_META_BYTES = 4 * 1024 * 1024
MAX_PAYLOAD_BYTES = 64 * 1024 * 1024
FRAME_READY = 0x01
FRAME_REQUEST = 0x02
FRAME_RESPONSE = 0x03
FRAME_LOAD = 0x02
FRAME_RUN = 0x03
FRAME_RESULT = 0x04
FRAME_ERROR = 0x05
FRAME_PING = 0x05
FRAME_RELOAD = 0x06


def _read_exact(stream, n):
    data = stream.read(n)
    if data == b"" and n > 0:
        raise EOFError("end of stream")
    if len(data) != n:
        raise ValueError(f"truncated frame: expected {n} bytes, got {len(data)}")
    return data


def read_frame(stream):
    magic = _read_exact(stream, 2)
    if magic != MAGIC:
        raise ValueError("invalid frame magic")
    frame_type = _read_exact(stream, 1)[0]
    meta_len = struct.unpack(">I", _read_exact(stream, 4))[0]
    if meta_len > MAX_META_BYTES:
        raise ValueError("frame meta too large")
    meta = json.loads(_read_exact(stream, meta_len).decode("utf-8"))
    payload_len = struct.unpack(">Q", _read_exact(stream, 8))[0]
    if payload_len > MAX_PAYLOAD_BYTES:
        raise ValueError("frame payload too large")
    payload = _read_exact(stream, payload_len) if payload_len else b""
    return frame_type, meta, payload


def write_frame(stream, frame_type, meta, payload=b""):
    meta_bytes = json.dumps(meta, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    stream.write(MAGIC)
    stream.write(bytes([frame_type]))
    stream.write(struct.pack(">I", len(meta_bytes)))
    stream.write(meta_bytes)
    stream.write(struct.pack(">Q", len(payload)))
    if payload:
        stream.write(payload)
    stream.flush()


def decode_json_df(meta):
    spec = meta.get("df", {})
    columns = spec.get("columns", {})
    if isinstance(columns, dict):
        df = pd.DataFrame(columns)
    else:
        names = list(columns)
        df = pd.DataFrame(spec.get("rows", []), columns=names)
    index_ms = spec.get("index_ms")
    if index_ms is True and "candle_begin_time" in df.columns:
        df["candle_begin_time"] = _utc_ns(df["candle_begin_time"])
    elif isinstance(index_ms, list):
        df.insert(0, "candle_begin_time", _utc_ns(index_ms))
    return df


def encode_json_results(task_id, results, tail, per_factor_ms, elapsed_ms, result_tails=None):
    encoded = {}
    for name, values in results.items():
        item_tail = int((result_tails or {}).get(name, tail))
        encoded[name] = {"tail": item_tail, "values": [_json_value(v) for v in list(values)[-item_tail:]]}
    return {
        "id": task_id,
        "ok": True,
        "encoding": "json",
        "results": encoded,
        "per_factor_ms": per_factor_ms,
        "elapsed_ms": elapsed_ms,
    }


def _json_value(value):
    if pd.isna(value):
        return None
    if hasattr(value, "item"):
        return value.item()
    return value


def _utc_ns(values):
    return pd.to_datetime(values, unit="ms", utc=True).astype("datetime64[ns, UTC]")
