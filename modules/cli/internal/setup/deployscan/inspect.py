# 只读检测一台主机上的 MooX 部署：部署根、version.json、moox-* 用户单元、部署根里的进程、归档占用、
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

def read_text(path, limit=65536):
    if os.path.isfile(path) and not os.path.islink(path):
        try:
            with open(path, "rb") as handle:
                return handle.read(limit).decode("utf-8", "replace")
        except OSError:
            return ""
    return ""

def nonempty_file(path):
    if not os.path.isfile(path) or os.path.islink(path):
        return False
    try:
        return os.lstat(path).st_size > 0
    except OSError:
        return False

def exists_regular(path):
    return os.path.isfile(path) and not os.path.islink(path)

def nonempty_dir(path):
    if not os.path.isdir(path) or os.path.islink(path):
        return False
    try:
        return bool(os.listdir(path))
    except OSError:
        return False

def inspect_data_formats(root):
    def join(*parts):
        return os.path.join(root, *parts)
    sqlite_files = {
        "admin": ("data", "admin", "admin.db"),
        "monitor": ("data", "monitor", "monitor.db"),
        "collector": ("data", "collector", "moox_collector.db"),
        "factor-mgr": ("data", "factor-mgr", "factor.db"),
        "archive-mgr": ("data", "archive-mgr", "archive.db"),
        "strategy": ("data", "strategy", "strategy.sqlite"),
        "trade": ("data", "trade", "moox_trade.db"),
    }
    formats = {}
    for cid, rel in sqlite_files.items():
        formats[cid] = {
            "has_data": nonempty_file(join(*rel)),
            "format_text": read_text(join("data", cid, "data-format")),
        }
    formats["storage-primary"] = {
        "has_data": (
            nonempty_file(join("data", "storage-primary", "metadata", "storage_metadata.db"))
            or exists_regular(join("data", "storage-primary", "pebble", "storage-primary", "CURRENT"))
            or exists_regular(join("data", "storage-primary", "pebble", "CURRENT"))
        ),
        "format_text": read_text(join("data", "storage-primary", "data-format")),
    }
    formats["storage-view"] = {
        "has_data": nonempty_dir(join("data", "storage-view", "view-indexes")),
        "format_text": read_text(join("data", "storage-view", "data-format")),
    }
    formats["eventbus"] = {
        "has_data": nonempty_dir(join("data", "eventbus", "jetstream")),
        "format_text": read_text(join("data", "eventbus", "data-format")),
    }
    formats["factor-engine"] = {
        "has_data": (
            exists_regular(join("data", "factor-engine", "seriesstore", "CURRENT"))
            or exists_regular(join("data", "factor-engine", "engine", "watermarks.json"))
            or exists_regular(join("data", "factor-engine", "engine", "param_cache.json"))
            or exists_regular(join("data", "factor-engine", "engine", "lifecycle.json"))
            or nonempty_dir(join("data", "factor-engine", "gap"))
        ),
        "format_text": read_text(join("data", "factor-engine", "data-format")),
    }
    formats["archive-worker"] = {
        "has_data": nonempty_file(join("archive", ".state", "staging.db")),
        "format_text": read_text(join("archive", ".state", "data-format")),
    }
    return formats

request = json.loads(sys.argv[1])
root, sizes = request.get("root"), bool(request.get("sizes"))
if not valid_root(root):
    fail("deploy root must be a clean absolute path other than /")
report = {"root": root, "root_exists": os.path.isdir(root), "entries": [], "installed": False, "install_complete": False, "version_text": "",
          "units": [], "active": [], "disabled": [], "processes": 0, "archive_kb": -1, "disk_free_kb": -1,
          "linger": False, "journald": os.path.isdir("/var/log/journal"), "data_formats": {}}
if report["root_exists"]:
    report["entries"] = sorted(os.listdir(root))
    version = os.path.join(root, "version.json")
    if os.path.isfile(version) and not os.path.islink(version):
        report["installed"] = True
        with open(version, "rb") as handle:
            raw = handle.read(64 << 10)
        report["version_text"] = raw.decode("utf-8", "replace")
    report["install_complete"] = exists_regular(os.path.join(root, "install-complete.json"))
    if sizes:
        report["archive_kb"] = size_kb(os.path.join(root, "archive"))
    report["data_formats"] = inspect_data_formats(root)
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
