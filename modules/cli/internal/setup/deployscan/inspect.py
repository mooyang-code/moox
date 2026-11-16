# 只读检测一台主机上的 MooX 部署：部署根、version.json、moox-* 用户单元、部署根里的进程、归档与 SeaweedFS 占用、
# 磁盘、linger 与 journald。不建目录、不改文件。参数是一个 JSON：{"root": 部署根, "sizes": 是否统计目录大小}。
import json, os, pwd, subprocess, sys

def fail(message):
    sys.stderr.write(message + "\n")
    sys.exit(2)

def valid_root(root):
    return isinstance(root, str) and root.startswith("/") and root != "/" and os.path.normpath(root) == root and "\x00" not in root and "\n" not in root

def run(argv):
    try:
        return subprocess.run(argv, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=30, text=True).stdout
    except (OSError, subprocess.SubprocessError):
        return ""

def inside(path, root):
    return path == root or path.startswith(root.rstrip("/") + "/")

def size_kb(path):
    if not os.path.lexists(path):
        return -1
    total = 0
    for top, dirs, files in os.walk(path, followlinks=False):
        for name in dirs + files:
            try:
                total += os.lstat(os.path.join(top, name)).st_blocks * 512
            except OSError:
                pass
    return total // 1024

request = json.loads(sys.argv[1])
root, sizes = request.get("root"), bool(request.get("sizes"))
if not valid_root(root):
    fail("deploy root must be a clean absolute path other than /")
report = {"root": root, "root_exists": os.path.isdir(root), "entries": [], "installed": False, "version_text": "",
          "units": [], "active": [], "disabled": [], "processes": 0, "archive_kb": -1, "seaweedfs_kb": -1, "disk_free_kb": -1,
          "linger": False, "journald": os.path.isdir("/var/log/journal")}
if report["root_exists"]:
    report["entries"] = sorted(os.listdir(root))
    version = os.path.join(root, "version.json")
    if os.path.isfile(version) and not os.path.islink(version):
        report["installed"] = True
        with open(version, "rb") as handle:
            raw = handle.read(64 << 10)
        report["version_text"] = raw.decode("utf-8", "replace")
    if sizes:
        report["archive_kb"] = size_kb(os.path.join(root, "archive"))
        report["seaweedfs_kb"] = size_kb(os.path.join(root, "data", "seaweedfs"))
probe = root
while not os.path.isdir(probe) and probe != "/":
    probe = os.path.dirname(probe)
try:
    stat = os.statvfs(probe)
    report["disk_free_kb"] = stat.f_bavail * stat.f_frsize // 1024
except OSError:
    pass
units_dir = os.path.join(os.path.expanduser("~"), ".config", "systemd", "user")
if os.path.isdir(units_dir):
    report["units"] = sorted(name for name in os.listdir(units_dir) if name.startswith("moox-") and name.endswith(".service"))
if report["units"]:
    states = run(["systemctl", "--user", "is-active"] + report["units"]).splitlines()
    report["active"] = [unit for unit, state in zip(report["units"], states) if state.strip() == "active"]
    states = run(["systemctl", "--user", "is-enabled"] + report["units"]).splitlines()
    report["disabled"] = [unit for unit, state in zip(report["units"], states) if state.strip() == "disabled"]
real_root = os.path.realpath(root)
if os.path.isdir("/proc"):
    for pid in os.listdir("/proc"):
        if not pid.isdigit() or int(pid) == os.getpid():
            continue
        try:
            if inside(os.path.realpath(os.readlink("/proc/%s/exe" % pid)), real_root):
                report["processes"] += 1
        except OSError:
            pass
user = pwd.getpwuid(os.getuid()).pw_name
report["linger"] = run(["loginctl", "show-user", user, "-p", "Linger", "--value"]).strip() == "yes"
print(json.dumps(report))
