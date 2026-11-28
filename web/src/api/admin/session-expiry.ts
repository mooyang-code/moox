import { AuthSessionExpiredError } from "./auth-errors";

let sessionExpiredListener: (() => void) | null = null;

/** 用户信息 store 在创建时登记：会话失效时清掉页面里的账号，路由守卫才不会把人从登录页推回首页。 */
export function setSessionExpiredListener(listener: (() => void) | null): void {
  sessionExpiredListener = listener;
}

/** 会话过期：清掉页面里的账号并带着当前地址去登录页，登录后回到原页面。 */
export function expireBrowserSession(message?: string): AuthSessionExpiredError {
  sessionExpiredListener?.();
  const current = window.location.hash.replace(/^#/, "");
  if (!current.startsWith("/login")) {
    window.location.hash = current && current !== "/" ? `#/login?redirect=${encodeURIComponent(current)}` : "#/login";
  }
  return new AuthSessionExpiredError(message);
}
