# 清空一台主机上的 MooX 部署：停用并删除全部 moox-* 用户单元，杀掉可执行文件在部署根里的进程（先 TERM，最多等 10 秒再 KILL），
# 清空部署根下的全部内容但保留根目录本身。systemctl 用不了（例如容器里没有用户实例）时照样清进程和文件，并如实报告。
# 参数是一个 JSON：{"root": 部署根}。部署根里的符号链接只删链接本身，不跟随到部署根之外。
import json, os, shutil, signal, subprocess, sys, time

def fail(message):
    sys.stderr.write(message + "\n")
    sys.exit(2)

def valid_root(root):
    return isinstance(root, str) and root.startswith("/") and root != "/" and os.path.normpath(root) == root and "\x00" not in root and "\n" not in root

def systemctl(*args):
    try:
        done = subprocess.run(["systemctl", "--user"] + list(args), stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, timeout=180, text=True)
        return "" if done.returncode == 0 else (done.stderr.strip().splitlines() or ["exit %d" % done.returncode])[-1]
    except (OSError, subprocess.SubprocessError) as error:
        return str(error)

def processes(root):
    found = []
    if not os.path.isdir("/proc"):
        return found
    for pid in os.listdir("/proc"):
        if not pid.isdigit() or int(pid) == os.getpid():
            continue
        try:
            exe = os.path.realpath(os.readlink("/proc/%s/exe" % pid))
        except OSError:
            continue
        if exe == root or exe.startswith(root.rstrip("/") + "/"):
            found.append(int(pid))
    return found

request = json.loads(sys.argv[1])
root = request.get("root")
if not valid_root(root):
    fail("deploy root must be a clean absolute path other than /")
if os.path.islink(root):
    fail("deploy root must not be a symbolic link; write its real path in paths.deploy_root")
result = {"units": [], "killed": 0, "removed": [], "systemd_error": ""}
errors = []
units_dir = os.path.join(os.path.expanduser("~"), ".config", "systemd", "user")
if os.path.isdir(units_dir):
    result["units"] = sorted(name for name in os.listdir(units_dir) if name.startswith("moox-") and name.endswith(".service"))
for unit in result["units"]:
    error = systemctl("disable", "--now", unit)
    if error:
        errors.append(unit + ": " + error)
    # disable 会删掉 *.wants 下的链接；systemctl 用不了时自己删，单元文件本身也删掉。
    for top, _, files in os.walk(units_dir):
        for name in files:
            path = os.path.join(top, name)
            if name == unit and (top == units_dir or os.path.islink(path)):
                os.unlink(path)
if result["units"]:
    error = systemctl("daemon-reload")
    if error:
        errors.append("daemon-reload: " + error)
result["systemd_error"] = "; ".join(errors)
if os.path.isdir(root):
    real_root = os.path.realpath(root)
    pids = processes(real_root)
    result["killed"] = len(pids)
    for pid in pids:
        try:
            os.kill(pid, signal.SIGTERM)
        except OSError:
            pass
    deadline = time.time() + 10
    while pids and time.time() < deadline:
        time.sleep(0.2)
        pids = [pid for pid in pids if os.path.exists("/proc/%d" % pid)]
    for pid in pids:
        try:
            os.kill(pid, signal.SIGKILL)
        except OSError:
            pass
    for name in sorted(os.listdir(root)):
        path = os.path.join(root, name)
        if os.path.isdir(path) and not os.path.islink(path):
            shutil.rmtree(path)
        else:
            os.unlink(path)
        result["removed"].append(name)
print(json.dumps(result))
