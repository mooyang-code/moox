import io

from moox_pyruntime.protocol import TYPE_RUN, read_frame, write_frame


def test_frame_round_trip():
    stream = io.BytesIO()
    write_frame(stream, TYPE_RUN, {"id": "r1"}, b"arrow")
    stream.seek(0)
    assert read_frame(stream) == (TYPE_RUN, {"id": "r1"}, b"arrow")
