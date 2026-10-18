import { flushPromises } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => {
  const close = vi.fn();
  return {
    setInstanceEnabled: vi.fn(),
    confirm: vi.fn(),
    close,
    Message: { success: vi.fn(), error: vi.fn(), info: vi.fn(), loading: vi.fn(() => ({ close })) }
  };
});
vi.mock("@/api/strategy", () => ({ setInstanceEnabled: api.setInstanceEnabled }));
vi.mock("@arco-design/web-vue", () => ({ Message: api.Message, Modal: { confirm: api.confirm } }));

import { TOGGLE_CANCELED_TIP, confirmInstanceToggle } from "./instance-toggle";

function lastDialog() {
  return api.confirm.mock.calls.at(-1)![0];
}

describe("instance toggle flow", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    api.setInstanceEnabled.mockReset().mockResolvedValue({});
  });

  it("asks for explicit confirmation before sending anything", () => {
    confirmInstanceToggle("i-1", false);
    expect(api.confirm).toHaveBeenCalledTimes(1);
    expect(lastDialog().title).toContain("停用实例");
    expect(lastDialog().content).toContain("不会自动清仓");
    expect(api.setInstanceEnabled).not.toHaveBeenCalled();
  });

  it("tells the user nothing changed when the dialog is dismissed", () => {
    const hooks = { onStart: vi.fn(), onSettled: vi.fn() };
    confirmInstanceToggle("i-1", true, hooks);
    lastDialog().onCancel();
    expect(api.Message.info).toHaveBeenCalledWith(TOGGLE_CANCELED_TIP);
    expect(api.setInstanceEnabled).not.toHaveBeenCalled();
    expect(hooks.onStart).not.toHaveBeenCalled();
    expect(hooks.onSettled).not.toHaveBeenCalled();
  });

  it("shows progress and success after confirming", async () => {
    const hooks = { onStart: vi.fn(), onFailed: vi.fn(), onSettled: vi.fn() };
    confirmInstanceToggle("i-1", true, hooks);
    lastDialog().onOk();
    expect(hooks.onStart).toHaveBeenCalledTimes(1);
    expect(api.Message.loading).toHaveBeenCalledTimes(1);
    await flushPromises();
    expect(api.setInstanceEnabled).toHaveBeenCalledWith("i-1", true);
    expect(api.Message.success).toHaveBeenCalledWith("实例已启用");
    expect(api.close).toHaveBeenCalled();
    expect(hooks.onFailed).not.toHaveBeenCalled();
    expect(hooks.onSettled).toHaveBeenCalledWith(true);
  });

  it("shows the error and reports failure when the request fails", async () => {
    const failure = new Error("实例不存在");
    api.setInstanceEnabled.mockRejectedValue(failure);
    const hooks = { onFailed: vi.fn(), onSettled: vi.fn() };
    confirmInstanceToggle("i-1", false, hooks);
    lastDialog().onOk();
    await flushPromises();
    expect(api.Message.error).toHaveBeenCalledWith(expect.stringContaining("实例不存在"));
    expect(api.Message.success).not.toHaveBeenCalled();
    expect(api.close).toHaveBeenCalled();
    expect(hooks.onFailed).toHaveBeenCalledWith(failure);
    expect(hooks.onSettled).toHaveBeenCalledWith(false);
  });
});
