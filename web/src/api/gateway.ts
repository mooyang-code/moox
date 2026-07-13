function withLeadingSlash(path: string): string {
  return path.startsWith('/') ? path : `/${path}`;
}

export function gatewayOrigin(): string {
  const developmentOverride = import.meta.env.DEV ? String(import.meta.env.VITE_ADMIN_ORIGIN || '').trim() : '';
  if (developmentOverride) return developmentOverride.replace(/\/$/, '');
  return typeof window === 'undefined' ? '' : window.location.origin;
}

export function gatewayURL(pathOrURL: string): string {
  if (/^https?:\/\//i.test(pathOrURL)) {
    return pathOrURL;
  }
  return `${gatewayOrigin()}${withLeadingSlash(pathOrURL)}`;
}

export function gatewayWebSocketURL(pathOrURL: string): string {
  if (/^wss?:\/\//i.test(pathOrURL)) {
    return pathOrURL;
  }
  return gatewayURL(pathOrURL).replace(/^http:/i, 'ws:').replace(/^https:/i, 'wss:');
}
