import { beforeEach, describe, expect, it, vi } from "vitest";

const { callControl, ControlRequestError } = vi.hoisted(() => ({
  callControl: vi.fn(),
  ControlRequestError: class ControlRequestError extends Error {
    constructor(
      message: string,
      public readonly response?: unknown
    ) {
      super(message);
    }
  }
}));
vi.mock("@/api/admin/http", () => ({ callControl, ControlRequestError }));

import {
  createInstance,
  getReplay,
  getStrategyDisplaySettings,
  getStrategyResult,
  setStrategyDisplaySettings,
  listInstances,
  listReplays,
  listStrategies,
  listStrategyResults,
  listStrategyTargets,
  listReplayBars,
  startReplay,
  validateStrategy
} from "./strategy";

describe("strategy API", () => {
  beforeEach(() => callControl.mockReset());

  it("lists definitions and keeps service errors visible", async () => {
    callControl.mockResolvedValueOnce({
      strategies: [{ strategy_id: "momentum", name: "动量", dsl_yaml: "name: 动量", dsl_hash: "sha256:abc" }],
      total: 1,
      page: 1,
      page_size: 20
    });
    await expect(listStrategies({ page: 1, page_size: 20 })).resolves.toMatchObject({
      items: [{ strategy_id: "momentum", name: "动量", dsl_hash: "sha256:abc" }]
    });
    callControl.mockRejectedValueOnce(new ControlRequestError("upstream EOF", { ret_info: { code: 13, msg: "upstream EOF" } }));
    await expect(listStrategies()).rejects.toThrow("upstream EOF");
  });

  it("creates a disabled instance bound to one factor task", async () => {
    callControl.mockResolvedValueOnce({
      instance: { instance_id: "i-1", strategy_id: "s-1", space_id: "space-1", factor_task_id: "ftask_a", enabled: false }
    });
    const instance = await createInstance({ strategy_id: "s-1", factor_task_id: "ftask_a", logical_account_id: "" });
    expect(callControl).toHaveBeenCalledWith(
      "strategy",
      "CreateStrategyInstance",
      { instance: { strategy_id: "s-1", factor_task_id: "ftask_a", logical_account_id: "", enabled: false } },
      undefined
    );
    expect(instance).toMatchObject({ instance_id: "i-1", factor_task_id: "ftask_a", resolved_json: "{}", health: "ok" });
  });

  it("reads results with status, skip reason and explanation items", async () => {
    callControl.mockResolvedValueOnce({
      targets: [],
      session_id: "s1",
      bar_end_time: "2026-09-06T01:00:00Z",
      valid_until: "2026-09-06T03:00:00Z",
      result_id: "r1"
    });
    callControl.mockResolvedValueOnce({
      results: [
        { result_id: "r1", bar_end_time: "2026-09-06T01:00:00Z", status: "skipped", skip_reason: "factor_missing", targets: [] }
      ],
      total: 1
    });
    callControl.mockResolvedValueOnce({
      result: { result_id: "r1", status: "ok" },
      items: [{ rule_id: "r", instrument_id: "BTC-USDT", stage: "weighted", rank: 1, weight: "0.5" }],
      dsl_yaml: "name: a",
      resolved_json: '{"view_id":"v"}'
    });
    await expect(listStrategyTargets("i-1")).resolves.toMatchObject({ session_id: "s1", result_id: "r1" });
    const page = await listStrategyResults("i-1", { session_id: "s1", page: 2, page_size: 10 });
    expect(page.items[0]).toMatchObject({ status: "skipped", skip_reason: "factor_missing", summary_json: "{}" });
    const detail = await getStrategyResult("r1");
    expect(detail.items[0]).toMatchObject({ rule_id: "r", rank: 1, weight: "0.5" });
    expect(detail.dsl_yaml).toBe("name: a");
    expect(callControl).toHaveBeenNthCalledWith(
      2,
      "strategy",
      "ListStrategyResults",
      { instance_id: "i-1", session_id: "s1", page: { page: 2, page_size: 10 } },
      undefined
    );
  });

  it("returns the server's diagnostics when validation is rejected as an invalid parameter", async () => {
    callControl.mockRejectedValueOnce(
      new ControlRequestError("列 momentum_99 不存在于 View view_a", {
        ret_info: { code: 1, msg: "列 momentum_99 不存在于 View view_a" },
        diagnostics: ["列 momentum_99 不存在于 View view_a", "规则 r2 引用了未知列", ""]
      })
    );
    const result = await validateStrategy("name: x", "ftask_a");
    expect(result.diagnostics).toEqual(["列 momentum_99 不存在于 View view_a", "规则 r2 引用了未知列"]);
    expect(result.trial).toBeNull();
    expect(callControl).toHaveBeenCalledWith("strategy", "ValidateStrategy", { dsl_yaml: "name: x", factor_task_id: "ftask_a" });
  });

  it("falls back to the error message when a rejected validation carries no diagnostics", async () => {
    callControl.mockRejectedValueOnce(
      new ControlRequestError("YAML 无法解析", { ret_info: { code: "INVALID_PARAM", msg: "YAML 无法解析" } })
    );
    await expect(validateStrategy("::")).resolves.toMatchObject({ diagnostics: ["YAML 无法解析"] });
  });

  it("keeps transport, auth and server errors as request errors", async () => {
    const network = new ControlRequestError("Network Error");
    callControl.mockRejectedValueOnce(network);
    await expect(validateStrategy("name: x")).rejects.toBe(network);
    const internal = new ControlRequestError("内部错误", { ret_info: { code: 4, msg: "内部错误" } });
    callControl.mockRejectedValueOnce(internal);
    await expect(validateStrategy("name: x")).rejects.toBe(internal);
    const other = new Error("框架错误");
    callControl.mockRejectedValueOnce(other);
    await expect(validateStrategy("name: x")).rejects.toBe(other);
  });

  it("passes success-path diagnostics (trial notes) through untouched", async () => {
    callControl.mockResolvedValueOnce({ diagnostics: ["试算未完成：View 暂无数据"], resolved_json: '{"view_id":"v"}' });
    await expect(validateStrategy("name: x", "v")).resolves.toMatchObject({
      diagnostics: ["试算未完成：View 暂无数据"],
      resolved_json: '{"view_id":"v"}',
      trial: null
    });
  });

  it("omits the enabled filter unless it is a boolean", async () => {
    callControl.mockResolvedValue({ instances: [], total: 0 });
    await listInstances({ enabled: "" as unknown as boolean });
    await listInstances({ enabled: false });
    expect(callControl.mock.calls[0][2]).toMatchObject({ enabled: undefined });
    expect(callControl.mock.calls[1][2]).toMatchObject({ enabled: false });
  });

  it("starts a replay with the requested window and fee", async () => {
    callControl.mockResolvedValueOnce({
      replay: { replay_id: "p1", status: "pending", fee_bps: 10, dsl_hash: "sha256:x" },
      bar_count: 30,
      first_bar_end: "2026-09-01T01:00:00Z",
      last_bar_end: "2026-09-02T06:00:00Z"
    });
    await expect(
      startReplay({
        instance_id: "i-1",
        factor_task_id: "ftask_a",
        start_time: "2026-09-01T00:00:00Z",
        end_time: "2026-09-30T00:00:00Z",
        fee_bps: 10
      })
    ).resolves.toMatchObject({
      replay: { replay_id: "p1", status: "pending", fee_bps: 10, dsl_hash: "sha256:x" },
      bar_count: 30,
      last_bar_end: "2026-09-02T06:00:00Z"
    });
  });

  it("reads replay bars in brief or full pages", async () => {
    callControl.mockResolvedValueOnce({
      bars: [{ bar_end_time: "t", equity: 1.2, holdings: 3, skip_reason: "no_data" }],
      total: 1
    });
    const page = await listReplayBars("p1", { page: 2, page_size: 2000, brief: true });
    expect(callControl).toHaveBeenLastCalledWith(
      "strategy",
      "ListReplayBars",
      { replay_id: "p1", page: { page: 2, page_size: 2000 }, brief: true },
      undefined
    );
    expect(page.items[0]).toMatchObject({ equity: 1.2, holdings: 3, skip_reason: "no_data", targets: [] });
  });

  it("silences the global error toast for background polling requests", async () => {
    callControl.mockResolvedValueOnce({ replays: [], total: 0 });
    await listReplays({ page: 1, page_size: 20 }, { silent: true });
    expect(callControl).toHaveBeenLastCalledWith(
      "strategy",
      "ListReplays",
      { page: { page: 1, page_size: 20 } },
      { silentError: true }
    );
    callControl.mockResolvedValueOnce({ replay: { replay_id: "p1" } });
    await getReplay("p1", { silent: true });
    expect(callControl).toHaveBeenLastCalledWith("strategy", "GetReplay", { replay_id: "p1" }, { silentError: true });
  });
});

describe("strategy factor parameters API", () => {
  beforeEach(() => callControl.mockReset());

  it("reads and saves the display settings with empty defaults", async () => {
    callControl.mockResolvedValueOnce({});
    await expect(getStrategyDisplaySettings("i-1")).resolves.toEqual({ categories: [], members: [], columns: [] });
    expect(callControl).toHaveBeenLastCalledWith("strategy", "GetStrategyDisplaySettings", { instance_id: "i-1" }, undefined);

    const hidden = { categories: ["volume"], members: [], columns: ["fast_bias"] };
    callControl.mockResolvedValueOnce({ hidden });
    await expect(setStrategyDisplaySettings("i-1", hidden)).resolves.toEqual(hidden);
    expect(callControl).toHaveBeenLastCalledWith(
      "strategy",
      "SetStrategyDisplaySettings",
      { instance_id: "i-1", hidden },
      undefined
    );
  });
});
