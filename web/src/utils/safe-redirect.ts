/**
 * 登录后要回到的站内地址：只接受以 / 开头、不以 // 开头（那是外站地址）、也不是登录页本身的路径，
 * 其余一律返回空串，由调用方回首页。
 */
export function safeRedirect(value: unknown): string {
  const target = Array.isArray(value) ? value[0] : value;
  if (typeof target !== "string" || !target.startsWith("/") || target.startsWith("//")) return "";
  if (target === "/login" || target.startsWith("/login?") || target.startsWith("/login#")) return "";
  return target;
}

/** 路由守卫跳登录页的目标：带上原地址，原地址是首页或不安全时不带。 */
export function loginLocation(fullPath: string): string | { path: string; query: { redirect: string } } {
  const redirect = safeRedirect(fullPath);
  return redirect && redirect !== "/" && redirect !== "/home" ? { path: "/login", query: { redirect } } : "/login";
}
