"""Download deployment unit packages on the target host. Reads JSON on stdin, prints JSON progress lines.

Source choice: fetch 1 MB from each address within 3 seconds and rank by bytes received; status codes do not count.
Download: at most `connections` ranged connections into a temporary file, 3 retries per segment, next source on failure.
Finish: verify SHA256, sync, rename to <deploy root>/staging/<digest>/<name>. An interrupted process downloads again.
"""
import hashlib
import json
import os
import re
import shutil
import sys
import threading
import time
import urllib.error
import urllib.request

PROBE_BYTES = 1 << 20
PROBE_SECONDS = 3
SEGMENT_BYTES = 16 << 20
RETRIES = 3
TIMEOUT = 30
KEEP_RELEASES = 2


def emit(event, **fields):
    fields['event'] = event
    sys.stdout.write(json.dumps(fields) + '\n')
    sys.stdout.flush()


def address(template, name):
    return template.replace('{file}', name)


def probe(url):
    """Bytes received within the time limit; a failed source scores whatever arrived before it failed."""
    received = 0
    deadline = time.monotonic() + PROBE_SECONDS
    request = urllib.request.Request(url, headers={'Range': 'bytes=0-%d' % (PROBE_BYTES - 1)})
    try:
        with urllib.request.urlopen(request, timeout=PROBE_SECONDS) as response:
            while received < PROBE_BYTES and time.monotonic() < deadline:
                block = response.read(1 << 16)
                if not block:
                    break
                received += len(block)
    except (OSError, ValueError):
        pass
    return received


def rank(templates, name):
    scores = {}

    def run(template):
        scores[template] = probe(address(template, name))

    workers = [threading.Thread(target=run, args=(template,), daemon=True) for template in templates]
    for worker in workers:
        worker.start()
    for worker in workers:
        worker.join(PROBE_SECONDS + TIMEOUT)
    ordered = sorted(templates, key=lambda template: -scores.get(template, 0))
    return [template for template in ordered if scores.get(template, 0) > 0], scores


class Limiter:
    """Shared byte budget per second; zero means unlimited."""

    def __init__(self, rate):
        self.rate, self.lock, self.window, self.used = rate, threading.Lock(), time.monotonic(), 0

    def take(self, count):
        if self.rate <= 0:
            return
        with self.lock:
            self.used += count
            wait = self.used / float(self.rate) - (time.monotonic() - self.window)
        if wait > 0:
            time.sleep(wait)


def fetch_range(url, descriptor, start, end, limiter):
    request = urllib.request.Request(url, headers={'Range': 'bytes=%d-%d' % (start, end)})
    with urllib.request.urlopen(request, timeout=TIMEOUT) as response:
        if response.status != 206:
            raise ValueError('range')
        offset = start
        while offset <= end:
            block = response.read(min(1 << 20, end - offset + 1))
            if not block:
                raise ValueError('short')
            os.pwrite(descriptor, block, offset)
            offset += len(block)
            limiter.take(len(block))


def fetch_segments(url, descriptor, size, connections, limiter):
    pending = [(start, min(start + SEGMENT_BYTES, size) - 1) for start in range(0, size, SEGMENT_BYTES)]
    pending.reverse()
    lock = threading.Lock()
    failures = []

    def run():
        while True:
            with lock:
                if not pending or failures:
                    return
                start, end = pending.pop()
            for attempt in range(RETRIES):
                try:
                    fetch_range(url, descriptor, start, end, limiter)
                    break
                except (OSError, ValueError) as error:
                    if attempt == RETRIES - 1:
                        with lock:
                            failures.append(str(error))
                        return
                    time.sleep(attempt + 1)

    workers = [threading.Thread(target=run, daemon=True) for _ in range(max(1, connections))]
    for worker in workers:
        worker.start()
    for worker in workers:
        worker.join()
    if failures:
        raise ValueError(failures[0])


def digest_of(path):
    digest = hashlib.sha256()
    with open(path, 'rb') as stream:
        for block in iter(lambda: stream.read(1 << 20), b''):
            digest.update(block)
    return digest.hexdigest()


def sync_directory(path):
    descriptor = os.open(path, os.O_RDONLY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def present(path, size, sha256):
    try:
        return os.path.getsize(path) == size and digest_of(path) == sha256
    except OSError:
        return False


def download(item, staging, request, limiter):
    directory = os.path.join(staging, item['sha256'])
    final = os.path.join(directory, item['name'])
    if present(final, item['size'], item['sha256']):
        emit('present', file=item['name'])
        return
    os.makedirs(directory, 0o700, exist_ok=True)
    temporary = '%s.part-%d' % (final, os.getpid())
    candidates, scores = rank(request['urls'], item['name'])
    emit('sources', file=item['name'], scores=dict((re.sub(r'/[^/]*$', '', key), value) for key, value in scores.items()))
    if not candidates:
        raise ValueError('no_source')
    last = 'no_source'
    for template in candidates:
        url = address(template, item['name'])
        started = time.monotonic()
        descriptor = os.open(temporary, os.O_CREAT | os.O_TRUNC | os.O_WRONLY, 0o600)
        try:
            os.ftruncate(descriptor, item['size'])
            fetch_segments(url, descriptor, item['size'], request.get('connections') or 4, limiter)
            os.fsync(descriptor)
        except (OSError, ValueError) as error:
            last = str(error)
            emit('source_failed', file=item['name'], reason=last)
            continue
        finally:
            os.close(descriptor)
        if digest_of(temporary) != item['sha256']:
            last = 'digest'
            emit('source_failed', file=item['name'], reason=last)
            continue
        os.replace(temporary, final)
        sync_directory(directory)
        emit('done', file=item['name'], seconds=round(time.monotonic() - started, 2))
        return
    try:
        os.unlink(temporary)
    except OSError:
        pass
    raise ValueError(last)


def prune(staging, release):
    """Keep the artifacts of this release and the one before it. Markers name the release of each directory."""
    releases = {}
    entries = []
    for name in os.listdir(staging):
        marker = os.path.join(staging, name, '.release')
        try:
            with open(marker) as stream:
                owner = stream.read().strip()
            modified = os.path.getmtime(marker)
        except OSError:
            continue
        entries.append((name, owner))
        releases[owner] = max(releases.get(owner, 0), modified)
    keep = set(sorted(releases, key=lambda owner: -releases[owner])[:KEEP_RELEASES]) | {release}
    for name, owner in entries:
        if owner not in keep:
            shutil.rmtree(os.path.join(staging, name), ignore_errors=True)


def main():
    request = json.load(sys.stdin)
    staging = os.path.join(request['deploy_root'], 'staging')
    os.makedirs(staging, 0o700, exist_ok=True)
    if request.get('probe_only'):
        name = request['files'][0]['name']
        candidates, scores = rank(request['urls'], name)
        emit('probe', reachable=len(candidates), scores=dict((re.sub(r'/[^/]*$', '', key), value) for key, value in scores.items()))
        return 0 if candidates else 3
    limiter = Limiter(request.get('rate_limit') or 0)
    try:
        for item in request['files']:
            download(item, staging, request, limiter)
            with open(os.path.join(staging, item['sha256'], '.release'), 'w') as stream:
                stream.write(request['release'])
    except (OSError, ValueError) as error:
        emit('failed', reason=str(error))
        return 3
    prune(staging, request['release'])
    emit('finished')
    return 0


sys.exit(main())
