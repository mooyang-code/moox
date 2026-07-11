import importlib.util
from pathlib import Path

import pytest


if importlib.util.find_spec("pyarrow") is None:
    pytest.skip("pyarrow is optional in the local test environment", allow_module_level=True)

import pyarrow as pa

from moox_pyruntime import decode_stream, encode_stream, open_mmap


def test_stream_round_trip():
    table = pa.table({"close": [1.25, None], "symbol": ["BTC", "ETH"]})
    assert decode_stream(encode_stream(table)).equals(table)


def test_mmap_reads_go_file(tmp_path):
    path = tmp_path / "snapshot.arrow"
    with pa.OSFile(str(path), "wb") as sink:
        with pa.ipc.new_file(sink, pa.schema([pa.field("close", pa.float64())])) as writer:
            writer.write_table(pa.table({"close": [1.25, 2.5]}))
    with open_mmap(path) as reader:
        assert reader.read_all().num_rows == 2
