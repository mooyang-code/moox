import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("./http", () => ({ callFactor: vi.fn() }));

import { callFactor } from "./http";
import {
  addFactorToTask,
  cancelRecalcJob,
  deleteFactorCategory,
  getFactorParams,
  listFactorCategories,
  upsertFactorCategory,
  createFactor,
  createFactorTask,
  deleteFactor,
  deleteFactorTask,
  getFactor,
  getFactorStatus,
  listFactors,
  listFactorTasks,
  recalcFactors,
  removeFactorFromTask,
  setFactorCategory,
  setFactorMemberStatus,
  setFactorTaskStatus,
  updateFactor,
  validateFactorSpec,
  previewFactor,
  listFactorFunctions
} from "./index";

const call = vi.mocked(callFactor);

describe("FactorMgr factor-set API", () => {
  afterEach(() => vi.resetAllMocks());

  it("creates factor tasks through CreateFactorTask", async () => {
    const factorTask = { task_id: "set-1", status: "pending" };
    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, factor_task: factorTask });

    await expect(createFactorTask(factorTask as never)).resolves.toEqual(factorTask);
    expect(call).toHaveBeenCalledWith("CreateFactorTask", { factor_task: factorTask });
  });

  it("lists sets and factor definitions with their new request fields", async () => {
    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, factor_tasks: [] });
    await listFactorTasks({ status: "enabled", page: { page: 1, size: 20 } });
    expect(call).toHaveBeenLastCalledWith("ListFactorTasks", { status: "enabled", page: { page: 1, size: 20 } });

    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, factors: [] });
    await listFactors({ task_id: "set-1", status: "enabled", page: { page: 1, size: 20 } });
    expect(call).toHaveBeenLastCalledWith("ListFactors", { task_id: "set-1", status: "enabled", page: { page: 1, size: 20 } });
  });

  it("listFactors without task_id requests all definitions", async () => {
    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, factors: [{ factor: { factor_id: "f1" }, usages: [] }] });
    const rsp = await listFactors();
    expect(call).toHaveBeenLastCalledWith("ListFactors", {});
    expect(rsp.factors[0].factor.factor_id).toBe("f1");
  });

  it("listFactors can request the spec explicitly", async () => {
    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, factors: [] });
    await listFactors({ include_spec: true });
    expect(call).toHaveBeenLastCalledWith("ListFactors", { include_spec: true });
  });

  it("validates, previews and lists functions through FactorMgr", async () => {
    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, valid: true });
    await validateFactorSpec("outputs:\n  x: close\n", true);
    expect(call).toHaveBeenLastCalledWith("ValidateFactorSpec", { spec: "outputs:\n  x: close\n", allow_partial_universe: true });
    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, columns: [], rows: [] });
    await previewFactor({ task_id: "ftask_a", spec: "outputs:\n  x: close\n", periods: 10 });
    expect(call).toHaveBeenLastCalledWith("PreviewFactor", { task_id: "ftask_a", spec: "outputs:\n  x: close\n", periods: 10 });
    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, functions: [], expr_version: "expr/1" });
    await listFactorFunctions();
    expect(call).toHaveBeenLastCalledWith("ListFactorFunctions", {});
  });

  it("createFactor payload has no task_id or status", async () => {
    const factor = { factor_id: "f1", name: "F1", spec: "outputs:\n  f1: close\n" };
    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, factor });
    await createFactor(factor);
    const payload = call.mock.calls.at(-1)?.[1] as { factor: Record<string, unknown> };
    expect(call).toHaveBeenLastCalledWith("CreateFactor", { factor });
    expect(payload.factor).not.toHaveProperty("task_id");
    expect(payload.factor).not.toHaveProperty("status");
  });

  it("addFactorToTask posts task_id and factor_id", async () => {
    const member = { task_id: "set-1", factor_id: "f1", status: "disabled" };
    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, member });
    await expect(addFactorToTask("set-1", "f1")).resolves.toEqual(member);
    expect(call).toHaveBeenLastCalledWith("AddTaskMember", { task_id: "set-1", factor_id: "f1", kind: "fixed" });
  });

  it("removeFactorFromTask posts task_id and the member key", async () => {
    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" } });
    await removeFactorFromTask("set-1", "f1");
    expect(call).toHaveBeenLastCalledWith("RemoveTaskMember", { task_id: "set-1", member_key: "f1" });
  });

  it("setFactorMemberStatus returns the member", async () => {
    const member = { task_id: "set-1", factor_id: "f1", status: "enabled" };
    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, member });
    await expect(setFactorMemberStatus("set-1", "f1", "enabled")).resolves.toEqual({ member });
    expect(call).toHaveBeenLastCalledWith("SetTaskMemberStatus", { task_id: "set-1", member_key: "f1", status: "enabled" });
  });

  it("addFactorToTask sends the kind and params only when given", async () => {
    call.mockResolvedValue({ ret_info: { code: 0, msg: "" }, member: { task_id: "set-1" } });
    await addFactorToTask("set-1", "bias");
    expect(call).toHaveBeenLastCalledWith("AddTaskMember", { task_id: "set-1", factor_id: "bias", kind: "fixed" });
    await addFactorToTask("set-1", "ema", "fixed", { span: 20, warmup: 50000 });
    expect(call).toHaveBeenLastCalledWith("AddTaskMember", {
      task_id: "set-1",
      factor_id: "ema",
      kind: "fixed",
      params: { span: 20, warmup: 50000 }
    });
    await addFactorToTask("set-1", "ema", "var", {});
    expect(call).toHaveBeenLastCalledWith("AddTaskMember", { task_id: "set-1", factor_id: "ema", kind: "var" });
    await addFactorToTask("set-1", "ema", "var", { span: 30 });
    expect(call).toHaveBeenLastCalledWith("AddTaskMember", {
      task_id: "set-1",
      factor_id: "ema",
      kind: "var",
      params: { span: 30 }
    });
  });

  it("manages categories, factor category and the submitted params", async () => {
    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, categories: [{ category_id: "momentum", name: "动量类" }] });
    await expect(listFactorCategories()).resolves.toEqual([{ category_id: "momentum", name: "动量类" }]);
    expect(call).toHaveBeenLastCalledWith("ListFactorCategories", {});

    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, category: { category_id: "volume", name: "成交量类" } });
    await upsertFactorCategory({ category_id: "volume", name: "成交量类" });
    expect(call).toHaveBeenLastCalledWith("UpsertFactorCategory", { category: { category_id: "volume", name: "成交量类" } });

    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" } });
    await deleteFactorCategory("volume");
    expect(call).toHaveBeenLastCalledWith("DeleteFactorCategory", { category_id: "volume" });

    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, factor: { factor_id: "bias", category_id: "momentum" } });
    await expect(setFactorCategory("bias", "momentum")).resolves.toEqual({ factor_id: "bias", category_id: "momentum" });
    expect(call).toHaveBeenLastCalledWith("SetFactorCategory", { factor_id: "bias", category_id: "momentum" });

    call.mockResolvedValueOnce({
      ret_info: { code: 0, msg: "" },
      members: [{ member_key: "bias__var", params: { window: { default_value: 20 } }, last_submitted_period: "180" }]
    });
    const rsp = await getFactorParams({ task_id: "set-1" });
    expect(call).toHaveBeenLastCalledWith("GetFactorParams", { task_id: "set-1" });
    expect(rsp.members[0]).toMatchObject({ member_key: "bias__var", last_submitted_period: "180" });
  });

  it("getFactor returns usages", async () => {
    call.mockResolvedValueOnce({
      ret_info: { code: 0, msg: "" },
      factor: { factor_id: "f1" },
      usages: [{ task_id: "set-1", status: "enabled" }]
    });
    const rsp = await getFactor("f1");
    expect(call).toHaveBeenLastCalledWith("GetFactor", { factor_id: "f1" });
    expect(rsp.usages).toEqual([{ task_id: "set-1", status: "enabled" }]);
  });

  it("sets factor-set status and submits/cancels set-scoped recalculation", async () => {
    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, factor_task: { task_id: "set-1", status: "disabled" } });
    await setFactorTaskStatus("set-1", "disabled");
    expect(call).toHaveBeenLastCalledWith("SetFactorTaskStatus", { task_id: "set-1", status: "disabled" });

    const req = { task_id: "set-1", factor_ids: ["f1"], subjects: ["BTC-USDT"], start_time: "a", end_time: "b" };
    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, job: { job_id: "job-1" } });
    await recalcFactors(req);
    expect(call).toHaveBeenLastCalledWith("RecalcFactors", req);

    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, job: { job_id: "job-1", status: "canceled" } });
    await cancelRecalcJob("job-1");
    expect(call).toHaveBeenLastCalledWith("CancelRecalcJob", { job_id: "job-1" });

    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, consumer_running: true });
    await getFactorStatus();
    expect(call).toHaveBeenLastCalledWith("GetStatus", {});
  });

  it("exposes set and factor detail, update, and delete operations", async () => {
    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" } });
    await deleteFactorTask("set-1", true);
    expect(call).toHaveBeenLastCalledWith("DeleteFactorTask", { task_id: "set-1", purge: true });

    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, factor: { factor_id: "f1" } });
    await updateFactor({ factor_id: "f1" } as never);
    expect(call).toHaveBeenLastCalledWith("UpdateFactor", { factor: { factor_id: "f1" } });

    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" } });
    await deleteFactor("f1");
    expect(call).toHaveBeenLastCalledWith("DeleteFactor", { factor_id: "f1" });
  });
});
