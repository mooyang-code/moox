import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("./http", () => ({ callFactor: vi.fn() }));

import { callFactor } from "./http";
import {
  addFactorToSet,
  cancelRecalcJob,
  deleteFactorCategory,
  getFactorParams,
  listFactorCategories,
  upsertFactorCategory,
  createFactor,
  createFactorSet,
  deleteFactor,
  deleteFactorSet,
  getFactor,
  getFactorStatus,
  listFactors,
  listFactorSets,
  recalcFactors,
  removeFactorFromSet,
  setFactorCategory,
  setFactorMemberStatus,
  setFactorSetStatus,
  updateFactor,
  validateFactorSpec,
  previewFactor,
  listFactorFunctions
} from "./index";

const call = vi.mocked(callFactor);

describe("FactorMgr factor-set API", () => {
  afterEach(() => vi.resetAllMocks());

  it("creates factor sets through CreateFactorSet", async () => {
    const factorSet = { set_id: "set-1", status: "pending" };
    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, factor_set: factorSet });

    await expect(createFactorSet(factorSet as never)).resolves.toEqual(factorSet);
    expect(call).toHaveBeenCalledWith("CreateFactorSet", { factor_set: factorSet });
  });

  it("lists sets and factor definitions with their new request fields", async () => {
    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, factor_sets: [] });
    await listFactorSets({ status: "enabled", page: { page: 1, size: 20 } });
    expect(call).toHaveBeenLastCalledWith("ListFactorSets", { status: "enabled", page: { page: 1, size: 20 } });

    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, factors: [] });
    await listFactors({ set_id: "set-1", status: "enabled", page: { page: 1, size: 20 } });
    expect(call).toHaveBeenLastCalledWith("ListFactors", { set_id: "set-1", status: "enabled", page: { page: 1, size: 20 } });
  });

  it("listFactors without set_id requests all definitions", async () => {
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
    await previewFactor({ set_id: "fset_a", spec: "outputs:\n  x: close\n", periods: 10 });
    expect(call).toHaveBeenLastCalledWith("PreviewFactor", { set_id: "fset_a", spec: "outputs:\n  x: close\n", periods: 10 });
    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, functions: [], expr_version: "expr/1" });
    await listFactorFunctions();
    expect(call).toHaveBeenLastCalledWith("ListFactorFunctions", {});
  });

  it("createFactor payload has no set_id or status", async () => {
    const factor = { factor_id: "f1", name: "F1", spec: "outputs:\n  f1: close\n" };
    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, factor });
    await createFactor(factor);
    const payload = call.mock.calls.at(-1)?.[1] as { factor: Record<string, unknown> };
    expect(call).toHaveBeenLastCalledWith("CreateFactor", { factor });
    expect(payload.factor).not.toHaveProperty("set_id");
    expect(payload.factor).not.toHaveProperty("status");
  });

  it("addFactorToSet posts set_id and factor_id", async () => {
    const member = { set_id: "set-1", factor_id: "f1", status: "disabled" };
    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, member });
    await expect(addFactorToSet("set-1", "f1")).resolves.toEqual(member);
    expect(call).toHaveBeenLastCalledWith("AddFactorToSet", { set_id: "set-1", factor_id: "f1" });
  });

  it("removeFactorFromSet posts set_id and the member alias", async () => {
    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" } });
    await removeFactorFromSet("set-1", "f1");
    expect(call).toHaveBeenLastCalledWith("RemoveFactorFromSet", { set_id: "set-1", member_alias: "f1" });
  });

  it("setFactorMemberStatus returns the member", async () => {
    const member = { set_id: "set-1", factor_id: "f1", status: "enabled" };
    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, member });
    await expect(setFactorMemberStatus("set-1", "f1", "enabled")).resolves.toEqual({ member });
    expect(call).toHaveBeenLastCalledWith("SetFactorMemberStatus", { set_id: "set-1", member_alias: "f1", status: "enabled" });
  });

  it("addFactorToSet sends the alias and fixed params only when given", async () => {
    call.mockResolvedValue({ ret_info: { code: 0, msg: "" }, member: { set_id: "set-1" } });
    await addFactorToSet("set-1", "bias");
    expect(call).toHaveBeenLastCalledWith("AddFactorToSet", { set_id: "set-1", factor_id: "bias" });
    await addFactorToSet("set-1", "bias", "fast");
    expect(call).toHaveBeenLastCalledWith("AddFactorToSet", { set_id: "set-1", factor_id: "bias", member_alias: "fast" });
    await addFactorToSet("set-1", "Ema", undefined, { span: 20, warmup: 50000 });
    expect(call).toHaveBeenLastCalledWith("AddFactorToSet", { set_id: "set-1", factor_id: "Ema", params: { span: 20, warmup: 50000 } });
    await addFactorToSet("set-1", "Ema", "ema20", {});
    expect(call).toHaveBeenLastCalledWith("AddFactorToSet", { set_id: "set-1", factor_id: "Ema", member_alias: "ema20" });
  });

  it("manages categories, factor category and parameter history", async () => {
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

    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, intervals: [], frozen_through: "180" });
    const rsp = await getFactorParams({ set_id: "set-1", include_requests: true });
    expect(call).toHaveBeenLastCalledWith("GetFactorParams", { set_id: "set-1", include_requests: true });
    expect(rsp.frozen_through).toBe("180");
  });

  it("getFactor returns usages", async () => {
    call.mockResolvedValueOnce({
      ret_info: { code: 0, msg: "" },
      factor: { factor_id: "f1" },
      usages: [{ set_id: "set-1", status: "enabled" }]
    });
    const rsp = await getFactor("f1");
    expect(call).toHaveBeenLastCalledWith("GetFactor", { factor_id: "f1" });
    expect(rsp.usages).toEqual([{ set_id: "set-1", status: "enabled" }]);
  });

  it("sets factor-set status and submits/cancels set-scoped recalculation", async () => {
    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, factor_set: { set_id: "set-1", status: "disabled" } });
    await setFactorSetStatus("set-1", "disabled");
    expect(call).toHaveBeenLastCalledWith("SetFactorSetStatus", { set_id: "set-1", status: "disabled" });

    const req = { set_id: "set-1", factor_ids: ["f1"], subjects: ["BTC-USDT"], start_time: "a", end_time: "b" };
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
    await deleteFactorSet("set-1", true);
    expect(call).toHaveBeenLastCalledWith("DeleteFactorSet", { set_id: "set-1", purge: true });

    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, factor: { factor_id: "f1" } });
    await updateFactor({ factor_id: "f1" } as never);
    expect(call).toHaveBeenLastCalledWith("UpdateFactor", { factor: { factor_id: "f1" } });

    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" } });
    await deleteFactor("f1");
    expect(call).toHaveBeenLastCalledWith("DeleteFactor", { factor_id: "f1" });
  });
});
