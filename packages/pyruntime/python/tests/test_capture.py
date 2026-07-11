from moox_pyruntime.capture import capture_output


def test_capture_is_bounded_and_does_not_leak_to_pipe(capsys):
    with capture_output(limit_bytes=8) as logs:
        print("hello")
        print("0123456789")
    captured = capsys.readouterr()
    assert captured.out == ""
    assert logs.stdout
    assert logs.truncated is True


def test_capture_finalizes_when_business_code_raises():
    logs = None
    try:
        with capture_output() as current:
            logs = current
            print("before failure")
            raise RuntimeError("boom")
    except RuntimeError:
        pass
    assert logs.stdout == "before failure\n"
