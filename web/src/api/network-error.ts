/** 浏览器没有收到任何响应（连接被拒、被重置、TLS 校验失败等）时的提示：这类失败最常见的原因是浏览器不信任管理台的 HTTPS 证书。 */
export const NETWORK_ERROR_MESSAGE =
  "无法连接管理台。请检查网络；如果浏览器提示证书不安全，请先安装管理台的根证书（登录页或“秘钥管理”页的“HTTPS 根证书”），然后彻底重启浏览器";

/** 请求没有拿到响应（axios 的 ERR_NETWORK / “Network Error”），区别于服务端返回了错误。 */
export function isNetworkFailure(error: unknown): boolean {
  const failure = error as { response?: unknown; code?: unknown; message?: unknown } | null;
  if (!failure || failure.response) return false;
  return failure.code === "ERR_NETWORK" || failure.message === "Network Error";
}

/**
 * 请求通道的错误提示文案。网络层失败换成可操作的中文提示，并把原错误对象的 message 一并改写：
 * 调用方随后在自己的 catch 里展示 error.message 时，说的是同一句话（相同文案的提示只会显示一条）。
 */
export function explainTransportError(error: unknown, fallback: string): string {
  if (isNetworkFailure(error)) {
    try {
      (error as { message: string }).message = NETWORK_ERROR_MESSAGE;
    } catch {
      // 只读的错误对象不改写，提示文案仍然是友好的
    }
    return NETWORK_ERROR_MESSAGE;
  }
  return (error as { message?: string } | null)?.message || fallback;
}
