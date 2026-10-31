# 清除一台目标机上的 MooX。argv[1] 是 JSON 请求，dirs 是预览列出并已通过删除保护的目录。
# 可重复执行：已经清掉的步骤再跑一次没有副作用。结果用 JSON 输出，不含任何文件内容。
import fcntl, glob, json, os, shutil, signal, stat, subprocess, sys, time

CRON_MARKER = '# moox-watchdog'
SERVICES = ('moox-factor-engine.service', 'moox-archive-worker.service')
LOCK_WAIT = 120
SCRIPT_TIMEOUT = 90
TERM_WAIT = 20
KILL_WAIT = 5


class Stop(Exception):
    pass


def run(argv, stdin=None, timeout=30):
    return subprocess.run(argv, input=stdin, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=timeout)


def remove_watchdog():
    listed = run(['crontab', '-l'])
    if listed.returncode != 0:
        return
    lines = listed.stdout.decode().splitlines()
    kept = [line for line in lines if CRON_MARKER not in line]
    if len(kept) == len(lines):
        return
    if kept:
        result = run(['crontab', '-'], stdin=('\n'.join(kept) + '\n').encode())
    else:
        result = run(['crontab', '-r'])
    if result.returncode != 0:
        raise Stop('watchdog_cron_failed')


def wait_watchdog(root):
    """等正在运行的那轮看护结束；只探测，拿到锁立刻释放，不让子进程继承。"""
    path = os.path.join(root, 'run/watchdog.lock')
    if not os.path.exists(path):
        return
    deadline = time.time() + LOCK_WAIT
    with open(path) as stream:
        while True:
            try:
                fcntl.flock(stream, fcntl.LOCK_EX | fcntl.LOCK_NB)
                fcntl.flock(stream, fcntl.LOCK_UN)
                return
            except OSError:
                if time.time() > deadline:
                    raise Stop('watchdog_busy')
                time.sleep(1)


def stop_services(home):
    env = dict(os.environ)
    env.setdefault('XDG_RUNTIME_DIR', '/run/user/%d' % os.getuid())
    present = [n for n in SERVICES if os.path.lexists(os.path.join(home, '.config/systemd/user', n))]
    if not present:
        return
    def systemctl(*args):
        return subprocess.run(['systemctl', '--user'] + list(args), env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=120)
    # 服务带自动重启；连不上用户级 systemd 时不能往下删，否则进程还会被拉起来。
    if systemctl('list-units', '--no-legend').returncode != 0:
        raise Stop('systemd_unreachable')
    for name in present:
        systemctl('stop', name)
        systemctl('disable', name)
        path = os.path.join(home, '.config/systemd/user', name)
        if systemctl('is-active', name).returncode == 0:
            raise Stop('service_still_active')
        os.unlink(path)
    systemctl('daemon-reload')


def stop_units(root):
    for link in sorted(glob.glob(os.path.join(root, '*', 'current'))):
        for script in ('pause.sh', 'stop.sh'):
            path = os.path.join(link, script)
            if not os.path.exists(path):
                continue
            try:
                run([path], timeout=SCRIPT_TIMEOUT)
            except (OSError, subprocess.SubprocessError):
                pass


def executable(pid):
    try:
        path = os.readlink('/proc/%s/exe' % pid)
    except OSError:
        return ''
    return path[:-len(' (deleted)')] if path.endswith(' (deleted)') else path


def under(path, directories):
    return any(path == d or path.startswith(d.rstrip('/') + '/') for d in directories)


def inside(directories):
    if not os.path.isdir('/proc'):
        return []
    me = os.getpid()
    return [int(p) for p in os.listdir('/proc') if p.isdigit() and int(p) != me and under(executable(p), directories)]


def signal_all(pids, number):
    for pid in pids:
        try:
            os.kill(pid, number)
        except OSError:
            pass


def wait_gone(directories, seconds):
    deadline = time.time() + seconds
    while time.time() < deadline:
        if not inside(directories):
            return True
        time.sleep(0.5)
    return not inside(directories)


def stop_processes(directories):
    signal_all(inside(directories), signal.SIGTERM)
    if wait_gone(directories, TERM_WAIT):
        return
    signal_all(inside(directories), signal.SIGKILL)
    if not wait_gone(directories, KILL_WAIT):
        raise Stop('processes_still_running')


def unlock(path):
    """发布目录可能是只读的，先放开权限才能删。不跟随符号链接。"""
    os.chmod(path, stat.S_IRWXU)
    for entry in os.scandir(path):
        if entry.is_dir(follow_symlinks=False):
            unlock(entry.path)


def remove(path):
    info = os.lstat(path)
    if stat.S_ISDIR(info.st_mode):
        unlock(path)
        shutil.rmtree(path)
    else:
        os.unlink(path)


def clear(directory):
    """删除目录里的全部内容，保留目录本身。"""
    if not os.path.isdir(directory) or os.path.islink(directory):
        return
    for name in os.listdir(directory):
        if name == 'lost+found' and os.path.ismount(directory):
            continue
        remove(os.path.join(directory, name))


def outside():
    if not os.path.isdir('/proc'):
        return []
    me, found = os.getpid(), set()
    for pid in os.listdir('/proc'):
        if not pid.isdigit() or int(pid) == me:
            continue
        path = executable(pid)
        name = path
        if not path:
            try:
                with open('/proc/%s/comm' % pid) as stream:
                    name = stream.read().strip()
            except OSError:
                continue
        if os.path.basename(name).startswith('moox-'):
            found.add(name)
    return sorted(found)


def main():
    request = json.loads(sys.argv[1])
    root, directories = request['deploy_root'], request.get('dirs') or []
    home = os.path.expanduser('~')
    try:
        remove_watchdog()
        wait_watchdog(root)
        stop_services(home)
        stop_units(root)
        stop_processes([root] + directories)
        clear(root)
        for directory in directories:
            clear(directory)
        result = {'status': 'done', 'processes_outside': outside()}
    except Stop as stopped:
        result = {'status': 'failed', 'reason': str(stopped)}
    except (OSError, subprocess.SubprocessError) as error:
        result = {'status': 'failed', 'reason': 'clear_failed', 'path': getattr(error, 'filename', '') or ''}
    json.dump(result, sys.stdout)


main()
