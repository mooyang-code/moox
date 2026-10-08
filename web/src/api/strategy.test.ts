import { beforeEach, describe, expect, it, vi } from "vitest";

const { callControl } = vi.hoisted(() => ({ callControl: vi.fn() }));
vi.mock("@/api/admin/http", () => ({ callControl }));

import {
  createInstance,
  getStrategyResult,
  listStrategies,
  listStrategyResults,
  listStrategyTargets,
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
    callControl.mockResolvedValueOnce({ ret_info: { code: 13, msg: "upstream EOF" } });
    await expect(listStrategies()).rejects.toThrow("upstream EOF");
  });

  it("creates a disabled instance bound to one View", async () => {
    callControl.mockResolvedValueOnce({
      instance: { instance_id: "i-1", strategy_id: "s-1", space_id: "space-1", view_id: "view_a", enabled: false }
    });
    const instance = await createInstance({ strategy_id: "s-1", view_id: "view_a", logical_account_id: "" });
    expect(callControl).toHaveBeenCalledWith("strategy", "CreateStrategyInstance", {
      instance: { strategy_id: "s-1", view_id: "view_a", logical_account_id: "", enabled: false }
    });
    expect(instance).toMatchObject({ instance_id: "i-1", view_id: "view_a", resolved_json: "{}", health: "ok" });
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
    expect(callControl).toHaveBeenNthCalledWith(2, "strategy", "ListStrategyResults", {
      instance_id: "i-1",
      session_id: "s1",
      page: { page: 2, page_size: 10 }
    });
  });

  it("returns diagnostics instead of throwing when validation fails", async () => {
    callControl.mockResolvedValueOnce({
      ret_info: { code: 1, msg: "列 momentum_99 不存在于 View view_a" },
      diagnostics: ["列 momentum_99 不存在于 View view_a"]
    });
    const result = await validateStrategy("name: x", "view_a");
    expect(result.diagnostics[0]).toContain("momentum_99");
    expect(result.trial).toBeNull();
    expect(callControl).toHaveBeenCalledWith("strategy", "ValidateStrategy", { dsl_yaml: "name: x", view_id: "view_a" });
  });

  it("starts a replay with the requested window and fee", async () => {
    callControl.mockResolvedValueOnce({ replay: { replay_id: "p1", status: "pending", fee_bps: 10 } });
    await expect(
      startReplay({
        strategy_id: "s-1",
        view_id: "view_a",
        start_time: "2026-09-01T00:00:00Z",
        end_time: "2026-09-30T00:00:00Z",
        fee_bps: 10
      })
    ).resolves.toMatchObject({ replay_id: "p1", status: "pending", fee_bps: 10 });
  });
});
