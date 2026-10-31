# Wipes MooX from one target host. argv[1] is a JSON request; dirs were listed in the preview and passed the delete guard.
# Repeatable: finished steps have no effect on a rerun. The JSON result holds no file contents.
import fcntl, glob, json, os, re, shutil, signal, stat, subprocess, sys, time

CRON_MARKER = '# moox-watchdog'
SERVICES = ('moox-factor-engine.service', 'moox-archive-worker.service')
LOCK_WAIT = 300
SCRIPT_TIMEOUT = 90
TERM_WAIT = 20
KILL_WAIT = 5
MOUNTS = set()


class Stop(Exception):
    pass


def run(argv, stdin=None, timeout=30):
    return subprocess.run(argv, input=stdin, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=timeout)


def remove_watchdog():
    try:
        listed = run(['crontab', '-l'])
    except OSError:
        return  # no crontab command means no watchdog entry
    if listed.returncode != 0:
        return
    lines = listed.stdout.decode('utf-8', 'surrogateescape').splitlines()
    kept = [line for line in lines if CRON_MARKER not in line]
    if len(kept) == len(lines):
        return
    if kept:
        result = run(['crontab', '-'], stdin=('\n'.join(kept) + '\n').encode('utf-8', 'surrogateescape'))
    else:
        result = run(['crontab', '-r'])
    if result.returncode != 0:
        raise Stop('watchdog_cron_failed')


def wait_watchdog(root):
    """Wait for a running watchdog pass. Only probes: the lock is released at once so no child inherits it."""
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
    # The services restart themselves: without the user manager, deleting files would not stop them.
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


def load_mounts(path):
    try:
        with open(path, errors='replace') as stream:
            rows = stream.readlines()
    except OSError:
        return set()
    decode = lambda text: re.sub(r'\\([0-7]{3})', lambda m: chr(int(m.group(1), 8)), text)
    return set(decode(row.split()[4]) for row in rows if len(row.split()) > 4)


def holds_mount(path):
    return any(m.startswith(path.rstrip('/') + '/') for m in MOUNTS)


def unlock(path):
    """Release directories may be read-only; open them first. Never follows symlinks."""
    os.chmod(path, stat.S_IRWXU)
    for entry in os.scandir(path):
        if entry.is_dir(follow_symlinks=False):
            unlock(entry.path)


def remove(path):
    info = os.lstat(path)
    if not stat.S_ISDIR(info.st_mode):
        os.unlink(path)
    elif path in MOUNTS or holds_mount(path):
        # A mount point cannot be removed: empty it and keep the directory.
        clear(path)
    else:
        unlock(path)
        shutil.rmtree(path)


def clear(directory):
    """Delete everything inside the directory and keep the directory."""
    if not os.path.isdir(directory) or os.path.islink(directory):
        return
    mounted = directory in MOUNTS or os.path.ismount(directory)
    for name in os.listdir(directory):
        if name == 'lost+found' and mounted:
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
        base = os.path.basename(name)
        if base.startswith('moox-') and base != 'moox-cli':
            found.add(name)
    return sorted(found)


def main():
    request = json.loads(sys.argv[1])
    root, directories = request['deploy_root'], request.get('dirs') or []
    home = os.path.expanduser('~')
    MOUNTS.update(load_mounts(request.get('mountinfo') or '/proc/self/mountinfo'))
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
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        result = {'status': 'failed', 'reason': 'clear_failed', 'path': getattr(error, 'filename', '') or ''}
    json.dump(result, sys.stdout)


main()
