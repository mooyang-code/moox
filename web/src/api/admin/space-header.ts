let selectedSpaceIdCache = '';

export function setSelectedSpaceIdCache(spaceId: string): void {
  selectedSpaceIdCache = spaceId || '';
}

export function readSelectedSpaceId(): string {
  if (selectedSpaceIdCache) return selectedSpaceIdCache;
  try {
    if (typeof localStorage === 'undefined') return '';
    const raw = localStorage.getItem('spaceStore');
    if (!raw) return '';
    const parsed = JSON.parse(raw) as { selectedSpaceId?: string; state?: { selectedSpaceId?: string } };
    return parsed.selectedSpaceId || parsed.state?.selectedSpaceId || '';
  } catch {
    return '';
  }
}

export function withSelectedSpaceHeader(headers: Record<string, string | undefined> = {}): Record<string, string | undefined> {
  if (headers['X-Space-Id']) {
    return headers;
  }
  const spaceId = readSelectedSpaceId();
  return spaceId ? { ...headers, 'X-Space-Id': spaceId } : headers;
}
