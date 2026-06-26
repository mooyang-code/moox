import json
import os
import stat
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("tencent_lighthouse_firewall.py")
DETAIL_URL = "https://console.cloud.tencent.com/lighthouse/instance/detail?searchParams=rid%3D5&rid=1&id=lhins-a7yikq89"


class TencentLighthouseFirewallTest(unittest.TestCase):
    def test_parse_console_detail_url(self):
        result = subprocess.run(
            [sys.executable, str(SCRIPT), "parse", "--detail-url", DETAIL_URL],
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=True,
        )

        parsed = json.loads(result.stdout)
        self.assertEqual(parsed["instance_id"], "lhins-a7yikq89")
        self.assertEqual(parsed["region"], "ap-guangzhou")

    def test_add_uses_fake_moox_cli_with_default_moox_ports(self):
        with tempfile.TemporaryDirectory() as tmp:
            argv_file = Path(tmp) / "argv.json"
            fake_cli = Path(tmp) / "moox-cli"
            fake_cli.write_text(
                "#!/usr/bin/env python3\n"
                "import json, pathlib, sys\n"
                f"pathlib.Path({str(argv_file)!r}).write_text(json.dumps(sys.argv[1:]))\n"
                "print('{\"status\":\"created\",\"request_id\":\"fake\"}')\n",
                encoding="utf-8",
            )
            fake_cli.chmod(fake_cli.stat().st_mode | stat.S_IXUSR)

            env = os.environ.copy()
            env["TENCENTCLOUD_SECRET_ID"] = "test-secret-id"
            env["TENCENTCLOUD_SECRET_KEY"] = "test-secret-key"

            result = subprocess.run(
                [
                    sys.executable,
                    str(SCRIPT),
                    "add",
                    "--detail-url",
                    DETAIL_URL,
                    "--moox-cli",
                    str(fake_cli),
                ],
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                check=True,
                env=env,
            )

            self.assertEqual(json.loads(result.stdout)["status"], "created")
            argv = json.loads(argv_file.read_text(encoding="utf-8"))
            self.assertEqual(
                argv[:6],
                ["ops", "tencent", "lighthouse", "firewall", "add", "--region"],
            )
            self.assertIn("ap-guangzhou", argv)
            self.assertIn("--instance-id", argv)
            self.assertIn("lhins-a7yikq89", argv)
            self.assertIn("--ports", argv)
            self.assertIn("19104,19101,19105,20103,20180", argv)

    def test_add_can_print_command_without_executing(self):
        result = subprocess.run(
            [
                sys.executable,
                str(SCRIPT),
                "add",
                "--detail-url",
                DETAIL_URL,
                "--ports",
                "19104",
                "--print-command",
            ],
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=True,
        )

        planned = json.loads(result.stdout)
        self.assertEqual(planned["will_execute"], False)
        self.assertEqual(planned["instance_id"], "lhins-a7yikq89")
        self.assertIn("--ports", planned["argv"])
        self.assertIn("19104", planned["argv"])


if __name__ == "__main__":
    unittest.main()
