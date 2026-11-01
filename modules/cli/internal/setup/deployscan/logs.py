import glob, json, os, subprocess, sys


def tail(path, lines):
    with open(path, 'rb') as stream:
        stream.seek(0, os.SEEK_END)
        size = stream.tell()
        start = max(0, size - (1 << 20))
        stream.seek(start)
        rows = stream.read().split(b'\n')
        if start:
            # 第一行可能是半截，没有键名就遮不到密钥。
            rows = rows[1:]
        return b'\n'.join(rows[-lines - 1:]).decode('utf-8', 'replace')


def main():
    request = json.loads(sys.argv[1])
    component, lines = request['component'], request['lines']
    sections = []
    if request.get('unit'):
        try:
            output = subprocess.run(['journalctl', '--user', '-u', request['unit'], '-n', str(lines), '--no-pager'],
                                    stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=30)
            sections.append({'path': 'journalctl --user -u ' + request['unit'], 'text': output.stdout.decode('utf-8', 'replace')})
        except (OSError, subprocess.SubprocessError):
            pass
    candidates = []
    for root in request['roots']:
        candidates += glob.glob(os.path.join(root, 'releases/*', component, 'logs/trpc.log'))
        candidates += glob.glob(os.path.join(root, '*/releases/*', component, 'logs/trpc.log'))
        candidates += glob.glob(os.path.join(root, 'run/logs', component + '.log'))
    candidates = sorted(set(candidates))
    candidates.sort(key=os.path.getmtime, reverse=True)
    for path in candidates[:3]:
        sections.append({'path': path, 'text': tail(path, lines)})
    json.dump(sections, sys.stdout)


main()
