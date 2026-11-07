/** 新账户的会话要等运行时的周期检查才建起来，期间加入组合会报“会话尚未配置”。 */
export const SESSION_RETRY_INTERVAL_MS = 2500;
export const SESSION_RETRY_LIMIT_MS = 20_000;

/** 后端的错误文案来自 runtime 的 ErrSessionConfig：ExchangeSession is not configured。 */
export function isSessionNotReady(error: unknown): boolean {
  return error instanceof Error && /ExchangeSession is not configured|会话尚未/.test(error.message);
}

export interface RetryOptions {
  intervalMs?: number;
  limitMs?: number;
  sleep?: (ms: number) => Promise<void>;
  now?: () => number;
  /** 每次决定再等一轮时调用，用来显示“正在等待账户会话建立” */
  onWaiting?: () => void;
  /** 对话框已经关闭等情况：返回 true 就不再重试，直接抛出上次的错误 */
  cancelled?: () => boolean;
}

/**
 * 执行 op；遇到“会话尚未建立”就隔几秒重试，最多等 limitMs。其他错误、超时和取消都抛出最后一次的错误，
 * 由界面显示并让用户手动重试。
 */
export async function retryWhileSessionStarting<T>(op: () => Promise<T>, options: RetryOptions = {}): Promise<T> {
  const interval = options.intervalMs ?? SESSION_RETRY_INTERVAL_MS;
  const limit = options.limitMs ?? SESSION_RETRY_LIMIT_MS;
  const sleep = options.sleep ?? ((ms: number) => new Promise<void>(resolve => setTimeout(resolve, ms)));
  const now = options.now ?? Date.now;
  const started = now();
  for (;;) {
    try {
      return await op();
    } catch (error) {
      if (!isSessionNotReady(error) || options.cancelled?.() || now() - started + interval > limit) throw error;
      options.onWaiting?.();
      await sleep(interval);
    }
  }
}
