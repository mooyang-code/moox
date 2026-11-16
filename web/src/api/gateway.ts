function withLeadingSlash(path: string): string {
  return path.startsWith("/") ? path : `/${path}`;
}

/** 管理台接口与页面同源：会话 Cookie 是 SameSite=Strict，跨域发不出去。本机开发由 vite 开发服务器代理到控制机。 */
export function gatewayOrigin(): string {
  return typeof window === "undefined" ? "" : window.location.origin;
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
  return gatewayURL(pathOrURL)
    .replace(/^http:/i, "ws:")
    .replace(/^https:/i, "wss:");
}
