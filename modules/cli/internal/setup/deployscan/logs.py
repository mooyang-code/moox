import glob, json, os, subprocess, sys


def tail(path, lines):
    with open(path, 'rb') as stream:
        stream.seek(0, os.SEEK_END)
        size = stream.tell()
        stream.seek(max(0, size - (1 << 20)))
        return b'\n'.join(stream.read().split(b'\n')[-lines - 1:]).decode('utf-8', 'replace')


def main():
    request = json.loads(sys.argv[1])
    root, component, lines = request['deploy_root'], request['component'], request['lines']
    sections = []
    if request.get('unit'):
        output = subprocess.run(['journalctl', '--user', '-u', request['unit'], '-n', str(lines), '--no-pager'],
                                stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=30)
        sections.append({'path': 'journalctl --user -u ' + request['unit'], 'text': output.stdout.decode('utf-8', 'replace')})
    candidates = glob.glob(os.path.join(root, '*/releases/*', component, 'logs/trpc.log'))
    candidates += glob.glob(os.path.join(root, 'run/logs', component + '.log'))
    candidates.sort(key=os.path.getmtime, reverse=True)
    for path in candidates[:3]:
        sections.append({'path': path, 'text': tail(path, lines)})
    json.dump(sections, sys.stdout)


main()
