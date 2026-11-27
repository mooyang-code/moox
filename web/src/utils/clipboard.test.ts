import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const messageMocks = vi.hoisted(() => ({
  success: vi.fn(),
  warning: vi.fn()
}));

vi.mock("@arco-design/web-vue", () => ({
  Message: messageMocks
}));

import { copyText } from "./clipboard";

describe("copyText", () => {
  const originalClipboard = navigator.clipboard;
  const originalExecCommand = document.execCommand;

  beforeEach(() => {
    messageMocks.success.mockReset();
    messageMocks.warning.mockReset();
  });

  afterEach(() => {
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: originalClipboard });
    Object.defineProperty(document, "execCommand", { configurable: true, writable: true, value: originalExecCommand });
  });

  it("ignores empty input", async () => {
    await expect(copyText("   ")).resolves.toBe(false);
    expect(messageMocks.success).not.toHaveBeenCalled();
    expect(messageMocks.warning).not.toHaveBeenCalled();
  });

  it("uses navigator.clipboard.writeText when available", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    await expect(copyText("close")).resolves.toBe(true);
    expect(writeText).toHaveBeenCalledWith("close");
    expect(messageMocks.success).toHaveBeenCalledWith("已复制 close");
  });

  it("falls back to execCommand when navigator.clipboard is unavailable or rejected", async () => {
    const execCommand = vi.fn().mockReturnValue(true);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: undefined });
    Object.defineProperty(document, "execCommand", { configurable: true, writable: true, value: execCommand });

    await expect(copyText("funding_rate", "字段 ID")).resolves.toBe(true);
    expect(execCommand).toHaveBeenCalledWith("copy");
    expect(messageMocks.success).toHaveBeenCalledWith("已复制 字段 ID");
    expect(document.querySelectorAll("textarea")).toHaveLength(0);

    const writeText = vi.fn().mockRejectedValue(new Error("denied"));
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    await expect(copyText("ma__5")).resolves.toBe(true);
    expect(writeText).toHaveBeenCalledWith("ma__5");
    expect(execCommand).toHaveBeenCalledTimes(2);
  });

  it("warns when both clipboard API and execCommand fail", async () => {
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText: vi.fn().mockRejectedValue(new Error("denied")) }
    });
    Object.defineProperty(document, "execCommand", { configurable: true, writable: true, value: vi.fn().mockReturnValue(false) });

    await expect(copyText("open")).resolves.toBe(false);
    expect(messageMocks.warning).toHaveBeenCalledWith("当前环境不支持一键复制，请手动复制");
    expect(document.querySelectorAll("textarea")).toHaveLength(0);
  });
});
