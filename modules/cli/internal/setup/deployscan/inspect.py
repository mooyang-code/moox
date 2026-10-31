# Read-only inspection of one target host: writes nothing and never prints secrets. argv[1] is a JSON request.
import glob, json, os, re, stat, subprocess, sys

try:
    import sqlite3
except ImportError:  # minimal python3 packages may lack this module
    sqlite3 = None

CRON_MARKER = '# moox-watchdog'
SERVICES = ('moox-factor-engine.service', 'moox-archive-worker.service')
OPEN_ORDERS = "'PENDING', 'SUBMITTING', 'SUBMIT_UNKNOWN', 'OPEN', 'PARTIALLY_FILLED', 'CANCELING', 'CANCEL_UNKNOWN'"
EXTERNAL_MARKERS = ('bin/moox-factor-engine', 'bin/moox-archive-worker')
MOUNTS = set()


def load_mounts(path):
    """Mount points, from mountinfo field 5 with octal escapes decoded."""
    try:
        with open(path, errors='replace') as stream:
            rows = stream.readlines()
    except OSError:
        return set()
    decode = lambda text: re.sub(r'\\([0-7]{3})', lambda m: chr(int(m.group(1), 8)), text)
    return set(decode(row.split()[4]) for row in rows if len(row.split()) > 4)


def holds_mount(path):
    return any(m.startswith(path.rstrip('/') + '/') for m in MOUNTS)


def names(path):
    """Directory contents that count as leftovers: lost+found and emptied nested mount points do not."""
    try:
        listed = sorted(n for n in os.listdir(path) if n != 'lost+found')
    except OSError:
        return []
    kept = []
    for name in listed:
        child = os.path.join(path, name)
        if (child in MOUNTS or holds_mount(child)) and not os.path.islink(child) and not names(child):
            continue
        kept.append(name)
    return kept


def service_file(home, name):
    return os.path.join(home, '.config/systemd/user', name)


def service_dir(path):
    try:
        with open(path, errors='replace') as stream:
            for line in stream:
                line = line.strip()
                if line.startswith('WorkingDirectory='):
                    return os.path.normpath(line.split('=', 1)[1].strip().lstrip('-'))
    except OSError:
        pass
    return ''


def archive_dir(directory):
    try:
        with open(os.path.join(directory, 'config/app.yaml'), errors='replace') as stream:
            match = re.search(r'^\s*root_dir:\s*["\']?([^"\'\s#]+)', stream.read(), re.M)
    except OSError:
        return ''
    if not match:
        return ''
    return os.path.normpath(os.path.join(directory, match.group(1)))


def executable(pid):
    try:
        path = os.readlink('/proc/%s/exe' % pid)
    except OSError:
        return ''
    return path[:-len(' (deleted)')] if path.endswith(' (deleted)') else path


def command_name(pid):
    try:
        with open('/proc/%s/comm' % pid) as stream:
            return stream.read().strip()
    except OSError:
        return ''


def under(path, directories):
    return any(path == d or path.startswith(d.rstrip('/') + '/') for d in directories)


def moox_process(name):
    # moox-cli is the operator tool, possibly running this very inspection on the same host.
    base = os.path.basename(name)
    return base.startswith('moox-') and base != 'moox-cli'


def processes(inside_dirs):
    """Processes inside the directories, and moox- processes outside; an unreadable path counts as outside."""
    inside, outside = [], []
    if not os.path.isdir('/proc'):
        return inside, outside
    me = os.getpid()
    for pid in os.listdir('/proc'):
        if not pid.isdigit() or int(pid) == me:
            continue
        path = executable(pid)
        if path and under(path, inside_dirs):
            inside.append(int(pid))
        elif moox_process(path or command_name(pid)):
            outside.append(path or command_name(pid))
    return inside, sorted(set(outside))


def listening(ports):
    found = {}
    for name in ('/proc/net/tcp', '/proc/net/tcp6'):
        try:
            with open(name) as stream:
                rows = stream.readlines()[1:]
        except OSError:
            continue
        for row in rows:
            fields = row.split()
            if len(fields) > 9 and fields[3] == '0A':
                port = int(fields[1].rsplit(':', 1)[1], 16)
                if port in ports:
                    found[fields[9]] = port
    return found


def occupied_ports(ports, inside_dirs):
    """Ports held by programs outside the deployment; an unknown owner counts as occupied."""
    sockets = listening(set(ports))
    if not sockets:
        return []
    for pid in os.listdir('/proc'):
        if not pid.isdigit() or not under(executable(pid), inside_dirs):
            continue
        try:
            descriptors = os.listdir('/proc/%s/fd' % pid)
        except OSError:
            continue
        for descriptor in descriptors:
            try:
                target = os.readlink('/proc/%s/fd/%s' % (pid, descriptor))
            except OSError:
                continue
            if target.startswith('socket:['):
                sockets.pop(target[8:-1], None)
    return sorted(set(sockets.values()))


def foreign_file(root):
    """True when the tree holds anything the SSH user cannot delete. A mount's lost+found belongs to root."""
    uid = os.getuid()
    denied = []
    for base, directories, files in os.walk(root, onerror=denied.append):
        if base in MOUNTS or os.path.ismount(base):
            directories[:] = [d for d in directories if d != 'lost+found']
        for name in directories + files:
            try:
                if os.lstat(os.path.join(base, name)).st_uid != uid:
                    return True
            except OSError:
                return True
        if denied:
            return True
    return False


def writable_parent(path):
    current = os.path.dirname(path)
    while current != '/' and not os.path.lexists(current):
        current = os.path.dirname(current)
    return os.access(current, os.W_OK | os.X_OK)


def cron_marked():
    try:
        output = subprocess.run(['crontab', '-l'], stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=20)
    except (OSError, subprocess.SubprocessError):
        return False
    return CRON_MARKER.encode() in output.stdout


def describe(path, marker):
    """A candidate directory outside the deployment root. Only marked directories are MooX's to delete."""
    return {
        'path': path,
        'empty': not names(path),
        'marked': any(os.path.lexists(os.path.join(path, m)) for m in marker),
        'symlink': os.path.islink(path),
        'foreign': os.path.lexists(path) and not os.path.islink(path) and foreign_file(path),
    }


def runtime_binding(root):
    try:
        with open(os.path.join(root, 'identity/runtime.json')) as stream:
            return json.load(stream).get('sha256', '')
    except (OSError, ValueError, AttributeError):
        return ''


def units(root):
    found = {}
    for link in glob.glob(os.path.join(root, '*', 'current')):
        try:
            found[os.path.basename(os.path.dirname(link))] = os.path.basename(os.readlink(link))
        except OSError:
            found[os.path.basename(os.path.dirname(link))] = ''
    return found


def query(path, statement):
    if sqlite3 is None:
        raise LookupError('sqlite3')
    connection = sqlite3.connect('file:%s?mode=ro' % path, uri=True, timeout=5)
    try:
        return connection.execute(statement).fetchall()
    finally:
        connection.close()


def trade_state(root):
    paths = glob.glob(os.path.join(root, '*/current/trade/data/moox_trade.db'))
    if not paths:
        return 'none'
    try:
        count = 0
        paper = "SELECT c_trading_account_id FROM t_trading_accounts WHERE c_execution_mode = 'PAPER'"
        for path in paths:
            count += query(path, "SELECT COUNT(*) FROM t_trading_accounts WHERE c_execution_mode = 'LIVE'")[0][0]
            count += query(path, "SELECT COUNT(*) FROM t_trading_positions WHERE CAST(c_signed_quantity AS REAL) != 0 AND c_trading_account_id NOT IN (%s)" % paper)[0][0]
            count += query(path, "SELECT COUNT(*) FROM t_trade_orders WHERE c_state IN (%s) AND c_trading_account_id NOT IN (%s)" % (OPEN_ORDERS, paper))[0][0]
        return 'live' if count else 'clear'
    except Exception:
        return 'unreadable'


def registered_hosts(database):
    if not database:
        return None
    try:
        return [{'id': row[0], 'address': row[1]} for row in query(database, 'SELECT c_host_id, c_address FROM t_hosts ORDER BY c_host_id')]
    except Exception:
        return None


def size_kb(path):
    try:
        output = subprocess.run(['du', '-sk', path], stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=60)
        return int(output.stdout.split()[0])
    except (OSError, ValueError, IndexError, subprocess.SubprocessError):
        return -1


def main():
    request = json.loads(sys.argv[1])
    root = request['deploy_root']
    home = os.path.expanduser('~')
    MOUNTS.update(load_mounts(request.get('mountinfo') or '/proc/self/mountinfo'))
    services = [name for name in SERVICES if os.path.lexists(service_file(home, name))]
    service_dirs = sorted(set(d for d in (service_dir(service_file(home, n)) for n in services) if d))
    external = [describe(d, EXTERNAL_MARKERS) for d in service_dirs if not under(d, [root])]
    archives = {request.get('archive_root', '')}
    for directory in service_dirs + [os.path.join(root, 'archive-worker')]:
        archives.add(archive_dir(directory))
    archives = sorted(d for d in archives if d and not under(d, [root]))
    archive_reports = [describe(d, ('.state',)) for d in archives]
    inside_dirs = [root] + [e['path'] for e in external if e['marked'] and not e['symlink']]
    inside, outside = processes(inside_dirs)
    exists = os.path.lexists(root)
    owner_ok, mode_ok = True, True
    if exists:
        info = os.lstat(root)
        owner_ok = info.st_uid == os.getuid()
        mode_ok = stat.S_ISDIR(info.st_mode) and not stat.S_IMODE(info.st_mode) & 0o022
    report = {
        'home': home,
        'root_exists': exists,
        'entries': names(root),
        'owner_ok': owner_ok,
        'mode_ok': mode_ok,
        'parent_writable': exists or writable_parent(root),
        'foreign_files': exists and owner_ok and foreign_file(root),
        'markers': any(os.path.lexists(os.path.join(root, n)) for n in ('run/host.json', 'host/current', 'identity', 'bootstrap', 'bootstrap-input')),
        'cron': cron_marked(),
        'services': services,
        'external_dirs': external,
        'archive_dirs': archive_reports,
        'processes_inside': len(inside),
        'processes_outside': outside,
        'ports_occupied': occupied_ports(request.get('ports', []), inside_dirs),
        'runtime_sha256': runtime_binding(root),
        'units': units(root),
        'trade': trade_state(root),
        'registered_hosts': registered_hosts(request.get('admin_db', '')),
    }
    if request.get('sizes'):
        report['sizes_kb'] = dict((d, size_kb(d)) for d in [root] + [e['path'] for e in external] + archives if os.path.lexists(d))
    json.dump(report, sys.stdout)


main()
