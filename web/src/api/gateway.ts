const DEFAULT_GATEWAY_PORT = '11000';

function gatewayPort(): string {
  const port = String(import.meta.env.VITE_GATEWAY_PORT || DEFAULT_GATEWAY_PORT).trim();
  return port || DEFAULT_GATEWAY_PORT;
}

function withLeadingSlash(path: string): string {
  return path.startsWith('/') ? path : `/${path}`;
}

export function gatewayOrigin(): string {
  const protocol = typeof window === 'undefined' ? 'http:' : window.location.protocol;
  const hostname = typeof window === 'undefined' ? 'localhost' : window.location.hostname || 'localhost';
  return `${protocol}//${hostname}:${gatewayPort()}`;
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
