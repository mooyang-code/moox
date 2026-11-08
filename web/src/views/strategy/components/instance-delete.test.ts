import { flushPromises } from "@vue/test-utils";
import { beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => {
  const close = vi.fn();
  return {
    deleteInstance: vi.fn(),
    listFactorTasks: vi.fn(),
    confirm: vi.fn(),
    close,
    Message: { success: vi.fn(), error: vi.fn(), info: vi.fn(), loading: vi.fn(() => ({ close })) }
  };
});
vi.mock("@/api/strategy", () => ({
  deleteInstance: api.deleteInstance,
  isInvalidParam: (code: unknown) => code === 1 || code === "INVALID_PARAM"
}));
vi.mock("@/api/factor", () => ({ listFactorTasks: api.listFactorTasks }));
vi.mock("@arco-design/web-vue", () => ({ Message: api.Message, Modal: { confirm: api.confirm } }));

import { ControlRequestError } from "@/api/admin/http";
import type { StrategyInstance } from "@/api/strategy-types";
import {
  DELETE_CANCELED_TIP,
  DELETE_INCOMPLETE_TIP,
  canDeleteInstance,
  confirmInstanceDelete,
  instanceDeleteLines
} from "./instance-delete";

const instance = {
  instance_id: "i-1",
  enabled: false,
  session_id: "",
  factor_tasks: { spot: "ftask_spot", swap: "ftask_swap" }
} as unknown as StrategyInstance;

function lastDialog() {
  return api.confirm.mock.calls.at(-1)![0];
}

describe("instance delete flow", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    api.deleteInstance.mockReset().mockResolvedValue(undefined);
    api.listFactorTasks.mockReset().mockResolvedValue({
      factor_tasks: [{ factor_task: { task_id: "ftask_spot", name: "现货因子", result_dataset_id: "dataset_factor_spot" } }],
      page_result: { has_more: false }
    });
  });

  it("only offers deletion for stopped instances without a session", () => {
    expect(canDeleteInstance({ enabled: false, session_id: "" })).toBe(true);
    expect(canDeleteInstance({ enabled: true, session_id: "s" })).toBe(false);
    expect(canDeleteInstance({ enabled: false, session_id: "s" })).toBe(false);
  });

  it("lists deleted and kept tasks in two groups", () => {
    const lines = instanceDeleteLines("i-1", [
      {
        market: "spot",
        taskId: "ftask_spot",
        factorTask: { name: "现货因子", result_dataset_id: "dataset_factor_spot", bound_instance_id: "i-1" } as never
      },
      { market: "swap", taskId: "ftask_swap" },
      {
        market: "multi",
        taskId: "ftask_spot_1m",
        factorTask: { name: "公共", result_dataset_id: "dataset_shared", bound_instance_id: "" } as never
      }
    ]).join("\n");
    expect(lines).toContain("将删除的因子任务和数据集");
    expect(lines).toContain("ftask_spot「现货因子」，结果数据集 dataset_factor_spot");
    expect(lines).toContain("ftask_swap（已删除）");
    expect(lines).toContain("保留的因子任务");
    expect(lines).toContain("ftask_spot_1m，未绑定到此实例");
    expect(lines).not.toContain("dataset_shared");
    expect(lines).toContain("结果数据不可恢复");
  });

  it("says nothing is deleted when no task is bound to the instance", () => {
    const lines = instanceDeleteLines("i-1", [
      { market: "spot", taskId: "ftask_spot_1m", factorTask: { bound_instance_id: "" } as never }
    ]);
    expect(lines[1]).toBe("无");
    expect(lines.join("\n")).toContain("未绑定到此实例");
  });

  it("confirms before deleting and reports success", async () => {
    const hooks = { onStart: vi.fn(), onSettled: vi.fn() };
    await confirmInstanceDelete(instance, hooks);
    expect(api.confirm).toHaveBeenCalledTimes(1);
    expect(lastDialog().title).toContain("删除实例");
    expect(api.deleteInstance).not.toHaveBeenCalled();
    lastDialog().onOk();
    await flushPromises();
    expect(api.deleteInstance).toHaveBeenCalledWith("i-1");
    expect(api.Message.success).toHaveBeenCalledWith("实例已删除");
    expect(hooks.onSettled).toHaveBeenCalledWith(true);
  });

  it("does nothing when the dialog is dismissed", async () => {
    await confirmInstanceDelete(instance);
    lastDialog().onCancel();
    expect(api.Message.info).toHaveBeenCalledWith(DELETE_CANCELED_TIP);
    expect(api.deleteInstance).not.toHaveBeenCalled();
  });

  it("asks to retry when the request times out", async () => {
    api.deleteInstance.mockRejectedValue(new Error("timeout of 30000ms exceeded"));
    const hooks = { onSettled: vi.fn() };
    await confirmInstanceDelete(instance, hooks);
    lastDialog().onOk();
    await flushPromises();
    expect(api.Message.error.mock.calls[0][0]).toContain(DELETE_INCOMPLETE_TIP);
    expect(hooks.onSettled).toHaveBeenCalledWith(false);
  });

  it("treats framework errors as an unknown outcome", async () => {
    api.deleteInstance.mockRejectedValue(new Error("框架错误(101)"));
    await confirmInstanceDelete(instance);
    lastDialog().onOk();
    await flushPromises();
    expect(api.Message.error.mock.calls[0][0]).toContain(DELETE_INCOMPLETE_TIP);
  });

  it("shows a refusal as is", async () => {
    const message = "结果数据集 dataset_factor_spot 有归档任务，请先到归档页删除这些归档任务，再删除实例";
    api.deleteInstance.mockRejectedValue(new ControlRequestError(message, { ret_info: { code: 1, msg: message } } as never));
    await confirmInstanceDelete(instance);
    lastDialog().onOk();
    await flushPromises();
    expect(api.Message.error.mock.calls[0][0]).not.toContain(DELETE_INCOMPLETE_TIP);
    expect(api.Message.error.mock.calls[0][0]).toContain("归档任务");
  });
});
