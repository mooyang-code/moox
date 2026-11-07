import { describe, expect, it, vi } from "vitest";
import { isSessionNotReady, retryWhileSessionStarting } from "./join-retry";

const notReady = () => new Error("trade runtime: ExchangeSession is not configured");

describe("加入组合时等待账户会话建立", () => {
  it("识别会话尚未建立的错误，其他错误不算", () => {
    expect(isSessionNotReady(notReady())).toBe(true);
    expect(isSessionNotReady(new Error("先暂停自动执行"))).toBe(false);
    expect(isSessionNotReady("ExchangeSession is not configured")).toBe(false);
  });

  it("会话没建好就隔几秒重试，建好后成功返回，并通知界面在等待", async () => {
    const op = vi.fn().mockRejectedValueOnce(notReady()).mockRejectedValueOnce(notReady()).mockResolvedValue("ok");
    const sleep = vi.fn(async () => undefined);
    const onWaiting = vi.fn();
    await expect(retryWhileSessionStarting(op, { sleep, onWaiting })).resolves.toBe("ok");
    expect(op).toHaveBeenCalledTimes(3);
    expect(sleep).toHaveBeenCalledWith(2500);
    expect(onWaiting).toHaveBeenCalledTimes(2);
  });

  it("超过大约 20 秒仍失败就抛出最后一次的错误，交给用户手动重试", async () => {
    let clock = 0;
    const op = vi.fn().mockRejectedValue(notReady());
    const sleep = vi.fn(async (ms: number) => {
      clock += ms;
    });
    await expect(retryWhileSessionStarting(op, { sleep, now: () => clock })).rejects.toThrow("ExchangeSession");
    // 20 秒里每 2.5 秒一次：首次加上最多 8 次重试
    expect(op.mock.calls.length).toBeGreaterThanOrEqual(8);
    expect(op.mock.calls.length).toBeLessThanOrEqual(9);
    expect(clock).toBeLessThanOrEqual(20_000);
  });

  it("其他错误立即抛出，不重试；对话框取消后不再重试", async () => {
    const op = vi.fn().mockRejectedValue(new Error("先暂停自动执行"));
    await expect(retryWhileSessionStarting(op, { sleep: async () => undefined })).rejects.toThrow("先暂停");
    expect(op).toHaveBeenCalledTimes(1);
    const op2 = vi.fn().mockRejectedValue(notReady());
    await expect(retryWhileSessionStarting(op2, { sleep: async () => undefined, cancelled: () => true })).rejects.toThrow();
    expect(op2).toHaveBeenCalledTimes(1);
  });
});
