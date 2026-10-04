from contextlib import redirect_stdout
from io import StringIO
import sys
import unittest

from moox_pyruntime.capture import capture_output


class CaptureOutputTest(unittest.TestCase):
    def test_capture_is_bounded_and_does_not_leak_to_pipe(self):
        protocol_output = StringIO()
        with redirect_stdout(protocol_output):
            with capture_output(limit_bytes=8) as logs:
                print("hello")
                print("0123456789")
                self.assertLessEqual(logs.buffered_bytes, 8)
        self.assertEqual(protocol_output.getvalue(), "")
        self.assertTrue(logs.stdout)
        self.assertTrue(logs.truncated)

    def test_capture_buffer_stays_bounded_during_large_writes(self):
        with capture_output(limit_bytes=16) as logs:
            print("x" * 1_000_000)
            print("界" * 1_000_000, file=sys.stderr)
            self.assertLessEqual(logs.buffered_bytes, 16)
        self.assertLessEqual(len(logs.stdout.encode("utf-8")), 16)
        self.assertLessEqual(len(logs.stderr.encode("utf-8")), 16)
        self.assertTrue(logs.truncated)

    def test_capture_finalizes_when_business_code_raises(self):
        logs = None
        try:
            with capture_output() as current:
                logs = current
                print("before failure")
                raise RuntimeError("boom")
        except RuntimeError:
            pass
        self.assertEqual(logs.stdout, "before failure\n")


if __name__ == "__main__":
    unittest.main()
