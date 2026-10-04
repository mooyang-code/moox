import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("./http", () => ({ callFactor: vi.fn() }));

import { callFactor } from "./http";
import {
  cancelRecalcJob,
  createFactor,
  createFactorSet,
  deleteFactor,
  deleteFactorSet,
  getFactor,
  getFactorSet,
  getFactorStatus,
  getRecalcJob,
  listFactors,
  listFactorSets,
  recalcFactors,
  setFactorStatus,
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

    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, factor: { factor_id: "f1" } });
    await createFactor({ factor_id: "f1", set_id: "set-1" } as never);
    expect(call).toHaveBeenLastCalledWith("CreateFactor", { factor: { factor_id: "f1", set_id: "set-1" } });

    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, factors: [] });
    await listFactors({ set_id: "set-1", status: "enabled", page: { page: 1, size: 20 } });
    expect(call).toHaveBeenLastCalledWith("ListFactors", { set_id: "set-1", status: "enabled", page: { page: 1, size: 20 } });
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

    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, job: { job_id: "job-1", status: "running" } });
    await getRecalcJob("job-1");
    expect(call).toHaveBeenLastCalledWith("GetRecalcJob", { job_id: "job-1" });

    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, factor: { factor_id: "f1", status: "disabled" } });
    await setFactorStatus("f1", "disabled");
    expect(call).toHaveBeenLastCalledWith("SetFactorStatus", { factor_id: "f1", status: "disabled" });

    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, consumer_running: true });
    await getFactorStatus();
    expect(call).toHaveBeenLastCalledWith("GetStatus", {});
  });

  it("exposes set and factor detail, update, and delete operations", async () => {
    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, factor_set: { set_id: "set-1" } });
    await updateFactorSet("set-1", "include", ["BTC-USDT"]);
    expect(call).toHaveBeenLastCalledWith("UpdateFactorSet", { set_id: "set-1", subject_mode: "include", subjects: ["BTC-USDT"] });

    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, factor_set: { set_id: "set-1" }, factors: [], last_run: undefined });
    await getFactorSet("set-1");
    expect(call).toHaveBeenLastCalledWith("GetFactorSet", { set_id: "set-1" });

    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" } });
    await deleteFactorSet("set-1", true);
    expect(call).toHaveBeenLastCalledWith("DeleteFactorSet", { set_id: "set-1", purge: true });

    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, factor: { factor_id: "f1" } });
    await updateFactor({ factor_id: "f1", set_id: "set-1" } as never);
    expect(call).toHaveBeenLastCalledWith("UpdateFactor", { factor: { factor_id: "f1", set_id: "set-1" } });

    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" }, factor: { factor_id: "f1" } });
    await getFactor("f1");
    expect(call).toHaveBeenLastCalledWith("GetFactor", { factor_id: "f1" });

    call.mockResolvedValueOnce({ ret_info: { code: 0, msg: "" } });
    await deleteFactor("f1");
    expect(call).toHaveBeenLastCalledWith("DeleteFactor", { factor_id: "f1" });
  });
});
