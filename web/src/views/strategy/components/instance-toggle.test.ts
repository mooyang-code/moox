import { flushPromises } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => {
  const close = vi.fn();
  return {
    setInstanceEnabled: vi.fn(),
    confirm: vi.fn(),
    warning: vi.fn(),
    close,
    Message: { success: vi.fn(), error: vi.fn(), info: vi.fn(), loading: vi.fn(() => ({ close })) }
  };
});
vi.mock("@/api/strategy", () => ({ setInstanceEnabled: api.setInstanceEnabled }));
vi.mock("@arco-design/web-vue", () => ({ Message: api.Message, Modal: { confirm: api.confirm, warning: api.warning } }));

import { RequestError } from "@/api/http";
import { TOGGLE_CANCELED_TIP, TRADE_UNCONFIRMED_TIP, confirmInstanceRedisable, confirmInstanceToggle } from "./instance-toggle";

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

  it("启用确认框提示规则状态会沿用，停用确认框不提", () => {
    confirmInstanceToggle("i-1", true);
    expect(lastDialog().content).toContain("会沿用停用前的持有记录、止损入场价和冷却");
    expect(lastDialog().content).toContain("先清空规则状态再启用");
    expect(lastDialog().content).toContain("moox-cli strategy instance update 原样提交一次，或改一下策略 DSL");
    confirmInstanceToggle("i-1", false);
    expect(lastDialog().content).not.toContain("沿用");
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

  it("停用已落库、Trade 没确认时用对话框说明重试与立刻停手的办法", async () => {
    const failure = new RequestError("实例已停用，但 Trade 未确认关闭会话", {
      ret_info: { code: 1, msg: "实例已停用，但 Trade 未确认关闭会话" },
      instance: { instance_id: "i-1", enabled: false }
    } as never);
    api.setInstanceEnabled.mockRejectedValue(failure);
    const hooks = { onFailed: vi.fn(), onSettled: vi.fn() };
    confirmInstanceToggle("i-1", false, hooks);
    lastDialog().onOk();
    await flushPromises();
    expect(api.warning).toHaveBeenCalledTimes(1);
    const dialog = api.warning.mock.calls[0][0];
    expect(dialog.title).toContain("Trade 未确认");
    expect(dialog.content).toContain("实例已停用，但 Trade 未确认关闭会话");
    expect(dialog.content).toContain(TRADE_UNCONFIRMED_TIP);
    expect(TRADE_UNCONFIRMED_TIP).toContain("再次停用");
    expect(TRADE_UNCONFIRMED_TIP).toContain("暂停自动执行");
    expect(api.Message.error).not.toHaveBeenCalled();
    expect(hooks.onFailed).toHaveBeenCalledWith(failure);
  });

  it("停用失败而实例还启用着时照常报错", async () => {
    api.setInstanceEnabled.mockRejectedValue(
      new RequestError("实例的会话已变化", {
        ret_info: { code: 1 },
        instance: { instance_id: "i-1", enabled: true }
      } as never)
    );
    confirmInstanceToggle("i-1", false);
    lastDialog().onOk();
    await flushPromises();
    expect(api.warning).not.toHaveBeenCalled();
    expect(api.Message.error).toHaveBeenCalledWith(expect.stringContaining("实例的会话已变化"));
  });

  it("再次停用先确认，确认后发停用请求", async () => {
    confirmInstanceRedisable("i-1");
    expect(lastDialog().title).toContain("再次停用");
    expect(lastDialog().content).toContain("重新通知 Trade 关闭");
    expect(api.setInstanceEnabled).not.toHaveBeenCalled();
    lastDialog().onOk();
    await flushPromises();
    expect(api.setInstanceEnabled).toHaveBeenCalledWith("i-1", false);
  });
});
