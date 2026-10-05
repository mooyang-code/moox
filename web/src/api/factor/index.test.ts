import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("./http", () => ({ callFactor: vi.fn() }));

import { callFactor } from "./http";
import {
  addFactorToSet,
  cancelRecalcJob,
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
  setFactorMemberStatus,
  setFactorSetStatus,
  updateFactor,
  updateFactorSet
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

  it("listFactors can request source explicitly", async () => {
    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, factors: [] });
    await listFactors({ include_source: true });
    expect(call).toHaveBeenLastCalledWith("ListFactors", { include_source: true });
  });

  it("createFactor payload has no set_id or status", async () => {
    const factor = {
      factor_id: "f1",
      name: "F1",
      factor_type: "timeseries" as const,
      source_code: "def compute(df, params, context):\n    return df",
      input_columns: ["close"],
      outputs: ["f1"],
      params_json: "{}",
      lookback_periods: 1
    };
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

  it("removeFactorFromSet posts set_id and factor_id", async () => {
    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" } });
    await removeFactorFromSet("set-1", "f1");
    expect(call).toHaveBeenLastCalledWith("RemoveFactorFromSet", { set_id: "set-1", factor_id: "f1" });
  });

  it("setFactorMemberStatus returns the backfill job", async () => {
    const member = { set_id: "set-1", factor_id: "f1", status: "enabled" };
    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, member, backfill_job: { job_id: "factor-enable-1" } });
    await expect(setFactorMemberStatus("set-1", "f1", "enabled")).resolves.toEqual({
      member,
      backfill_job: { job_id: "factor-enable-1" }
    });
    expect(call).toHaveBeenLastCalledWith("SetFactorMemberStatus", { set_id: "set-1", factor_id: "f1", status: "enabled" });
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
    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, factor_set: { set_id: "set-1" } });
    await updateFactorSet("set-1", "include", ["BTC-USDT"]);
    expect(call).toHaveBeenLastCalledWith("UpdateFactorSet", {
      set_id: "set-1",
      subject_mode: "include",
      subjects: ["BTC-USDT"]
    });

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
