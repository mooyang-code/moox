import { beforeEach, describe, expect, it, vi } from "vitest";

const { call, RequestError } = vi.hoisted(() => ({
  call: vi.fn(),
  RequestError: class RequestError extends Error {
    constructor(
      message: string,
      public readonly response?: unknown
    ) {
      super(message);
    }
  }
}));
vi.mock("@/api/http", () => ({ call, RequestError }));

import {
  confirmInstance,
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
  beforeEach(() => call.mockReset());

  it("lists definitions and keeps service errors visible", async () => {
    call.mockResolvedValueOnce({
      strategies: [{ strategy_id: "momentum", name: "动量", dsl_yaml: "name: 动量", dsl_hash: "sha256:abc" }],
      total: 1,
      page: 1,
      page_size: 20
    });
    await expect(listStrategies({ page: 1, page_size: 20 })).resolves.toMatchObject({
      items: [{ strategy_id: "momentum", name: "动量", dsl_hash: "sha256:abc" }]
    });
    call.mockRejectedValueOnce(new RequestError("upstream EOF", { ret_info: { code: 13, msg: "upstream EOF" } }));
    await expect(listStrategies()).rejects.toThrow("upstream EOF");
  });

  it("creates a disabled instance bound to one factor task", async () => {
    call.mockResolvedValueOnce({
      instance: { instance_id: "i-1", strategy_id: "s-1", space_id: "space-1", factor_tasks: { swap: "ftask_a" }, enabled: false }
    });
    const instance = await createInstance({ strategy_id: "s-1", factor_tasks: { swap: "ftask_a" }, logical_account_id: "" });
    expect(call).toHaveBeenCalledWith("strategy", "CreateStrategyInstance", {
      instance: { strategy_id: "s-1", factor_tasks: { swap: "ftask_a" }, logical_account_id: "", enabled: false }
    });
    expect(instance).toMatchObject({ instance_id: "i-1", factor_tasks: { swap: "ftask_a" }, resolved_json: "{}", health: "ok" });
  });

  it("confirms a trial instance for its session and keeps confirmed_at", async () => {
    call.mockResolvedValueOnce({
      instance: { instance_id: "i-1", enabled: true, logical_account_id: "la-1", confirmed_at: "2026-11-15T01:00:00Z" }
    });
    const instance = await confirmInstance("i-1", "session-1");
    expect(call).toHaveBeenCalledWith("strategy", "ConfirmStrategyInstance", { instance_id: "i-1", session_id: "session-1" });
    expect(instance.confirmed_at).toBe("2026-11-15T01:00:00Z");
    call.mockResolvedValueOnce({ instances: [{ instance_id: "i-2", enabled: true }], total: 1 });
    const page = await listInstances();
    expect(page.items[0].confirmed_at).toBe("");
  });

  it("reads results with status, skip reason and explanation items", async () => {
    call.mockResolvedValueOnce({
      targets: [],
      session_id: "s1",
      bar_end_time: "2026-09-06T01:00:00Z",
      valid_until: "2026-09-06T03:00:00Z",
      result_id: "r1"
    });
    call.mockResolvedValueOnce({
      results: [
        { result_id: "r1", bar_end_time: "2026-09-06T01:00:00Z", status: "skipped", skip_reason: "factor_missing", targets: [] }
      ],
      total: 1
    });
    call.mockResolvedValueOnce({
      result: { result_id: "r1", status: "ok" },
      items: [
        { rule_id: "r", instrument_id: "BTC-USDT", stage: "weighted", rank: 1, weight: "0.5", scale: "0.8" },
        { rule_id: "r", instrument_id: "ETH-USDT", stage: "weighted", rank: 2, weight: "0.5" }
      ],
      dsl_yaml: "name: a",
      resolved_json: '{"view_id":"v"}'
    });
    await expect(listStrategyTargets("i-1")).resolves.toMatchObject({ session_id: "s1", result_id: "r1" });
    const page = await listStrategyResults("i-1", { session_id: "s1", page: 2, page_size: 10 });
    expect(page.items[0]).toMatchObject({ status: "skipped", skip_reason: "factor_missing", summary_json: "{}" });
    const detail = await getStrategyResult("r1");
    expect(detail.items[0]).toMatchObject({ rule_id: "r", rank: 1, weight: "0.5", scale: "0.8" });
    // 规则没写 scale 时为空串，系数列据此隐藏
    expect(detail.items[1].scale).toBe("");
    expect(detail.dsl_yaml).toBe("name: a");
    expect(call).toHaveBeenNthCalledWith(2, "strategy", "ListStrategyResults", {
      instance_id: "i-1",
      session_id: "s1",
      page: { page: 2, page_size: 10 }
    });
  });

  it("returns the server's diagnostics when validation is rejected as an invalid parameter", async () => {
    call.mockRejectedValueOnce(
      new RequestError("列 momentum_99 不存在于 View view_a", {
        ret_info: { code: 1, msg: "列 momentum_99 不存在于 View view_a" },
        diagnostics: ["列 momentum_99 不存在于 View view_a", "规则 r2 引用了未知列", ""]
      })
    );
    const result = await validateStrategy("name: x", { spot: "ftask_a" });
    expect(result.diagnostics).toEqual(["列 momentum_99 不存在于 View view_a", "规则 r2 引用了未知列"]);
    expect(result.trial).toBeNull();
    expect(call).toHaveBeenCalledWith("strategy", "ValidateStrategy", {
      dsl_yaml: "name: x",
      factor_tasks: { spot: "ftask_a" }
    });
  });

  it("falls back to the error message when a rejected validation carries no diagnostics", async () => {
    call.mockRejectedValueOnce(new RequestError("YAML 无法解析", { ret_info: { code: "INVALID_PARAM", msg: "YAML 无法解析" } }));
    await expect(validateStrategy("::")).resolves.toMatchObject({ diagnostics: ["YAML 无法解析"] });
  });

  it("keeps transport, auth and server errors as request errors", async () => {
    const network = new RequestError("Network Error");
    call.mockRejectedValueOnce(network);
    await expect(validateStrategy("name: x")).rejects.toBe(network);
    const internal = new RequestError("内部错误", { ret_info: { code: 4, msg: "内部错误" } });
    call.mockRejectedValueOnce(internal);
    await expect(validateStrategy("name: x")).rejects.toBe(internal);
    const other = new Error("框架错误");
    call.mockRejectedValueOnce(other);
    await expect(validateStrategy("name: x")).rejects.toBe(other);
  });

  it("passes success-path diagnostics (trial notes) through untouched", async () => {
    call.mockResolvedValueOnce({ diagnostics: ["试算未完成：View 暂无数据"], resolved_json: '{"view_id":"v"}' });
    await expect(validateStrategy("name: x", "v")).resolves.toMatchObject({
      diagnostics: ["试算未完成：View 暂无数据"],
      resolved_json: '{"view_id":"v"}',
      trial: null
    });
  });

  it("omits the enabled filter unless it is a boolean", async () => {
    call.mockResolvedValue({ instances: [], total: 0 });
    await listInstances({ enabled: "" as unknown as boolean });
    await listInstances({ enabled: false });
    expect(call.mock.calls[0][2]).toMatchObject({ enabled: undefined });
    expect(call.mock.calls[1][2]).toMatchObject({ enabled: false });
  });

  it("starts a replay with the requested window and fee", async () => {
    call.mockResolvedValueOnce({
      replay: {
        replay_id: "p1",
        status: "pending",
        spot_fee_bps: 10,
        tasks: { spot: { factor_task_id: "ftask_a", view_id: "view_factor_a" } },
        dsl_hash: "sha256:x"
      },
      bar_count: 30,
      first_bar_end: "2026-09-01T01:00:00Z",
      last_bar_end: "2026-09-02T06:00:00Z"
    });
    await expect(
      startReplay({
        instance_id: "i-1",
        factor_tasks: { spot: "ftask_a" },
        start_time: "2026-09-01T00:00:00Z",
        end_time: "2026-09-30T00:00:00Z",
        spot_fee_bps: 10,
        swap_fee_bps: 0
      })
    ).resolves.toMatchObject({
      replay: {
        replay_id: "p1",
        status: "pending",
        spot_fee_bps: 10,
        tasks: { spot: { factor_task_id: "ftask_a" } },
        dsl_hash: "sha256:x"
      },
      bar_count: 30,
      last_bar_end: "2026-09-02T06:00:00Z"
    });
  });

  it("reads replay bars in brief or full pages", async () => {
    call.mockResolvedValueOnce({
      bars: [{ bar_end_time: "t", equity: 1.2, holdings: 3, skip_reason: "no_data" }],
      total: 1
    });
    const page = await listReplayBars("p1", { page: 2, page_size: 2000, brief: true });
    expect(call).toHaveBeenLastCalledWith("strategy", "ListReplayBars", {
      replay_id: "p1",
      page: { page: 2, page_size: 2000 },
      brief: true
    });
    expect(page.items[0]).toMatchObject({ equity: 1.2, holdings: 3, skip_reason: "no_data", targets: [] });
  });

  it("silences the global error toast for background polling requests", async () => {
    call.mockResolvedValueOnce({ replays: [], total: 0 });
    await listReplays({ page: 1, page_size: 20 });
    expect(call).toHaveBeenLastCalledWith("strategy", "ListReplays", { page: { page: 1, page_size: 20 } });
    call.mockResolvedValueOnce({ replay: { replay_id: "p1" } });
    await getReplay("p1");
    expect(call).toHaveBeenLastCalledWith("strategy", "GetReplay", { replay_id: "p1" });
  });
});

describe("strategy factor parameters API", () => {
  beforeEach(() => call.mockReset());

  it("reads and saves the display settings with empty defaults", async () => {
    call.mockResolvedValueOnce({});
    await expect(getStrategyDisplaySettings("i-1")).resolves.toEqual({ categories: [], members: [], columns: [] });
    expect(call).toHaveBeenLastCalledWith("strategy", "GetStrategyDisplaySettings", { instance_id: "i-1" });

    const hidden = { categories: ["volume"], members: [], columns: ["fast_bias"] };
    call.mockResolvedValueOnce({ hidden });
    await expect(setStrategyDisplaySettings("i-1", hidden)).resolves.toEqual(hidden);
    expect(call).toHaveBeenLastCalledWith("strategy", "SetStrategyDisplaySettings", { instance_id: "i-1", hidden });
  });
});
