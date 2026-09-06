import { beforeEach, describe, expect, it, vi } from "vitest";

const { callControl } = vi.hoisted(() => ({ callControl: vi.fn() }));
vi.mock("@/api/admin/http", () => ({ callControl }));

import { createInstance, listStrategies, listStrategyResults, listStrategyTargets } from "./strategy";

describe("modern strategy API", () => {
  beforeEach(() => callControl.mockReset());

  it("lists definitions and keeps service errors visible", async () => {
    callControl.mockResolvedValueOnce({ strategies: [{ strategy_id: "momentum", strategy_name: "动量", dsl_yaml: "name: 动量" }], total: 1, page: 1, page_size: 20 });
    await expect(listStrategies({ page: 1, page_size: 20 })).resolves.toMatchObject({ items: [{ strategy_id: "momentum", name: "动量" }] });
    callControl.mockResolvedValueOnce({ ret_info: { code: 13, msg: "upstream EOF" } });
    await expect(listStrategies()).rejects.toThrow("upstream EOF");
  });

  it("always creates a disabled instance and never sends runner fields", async () => {
    callControl.mockResolvedValueOnce({ instance: { instance_id: "i-1", strategy_id: "s-1", space_id: "space-1", input_bindings_json: "{}", enabled: false } });
    await createInstance({ instance_id: "i-1", strategy_id: "s-1", space_id: "space-1", input_bindings_json: "{}", logical_account_id: "" });
    expect(callControl).toHaveBeenCalledWith("strategy", "CreateStrategyInstance", {
      instance: { instance_id: "i-1", strategy_id: "s-1", space_id: "space-1", input_bindings_json: "{}", logical_account_id: "", enabled: false }
    });
  });

  it("uses instance and session for targets and result history", async () => {
    callControl.mockResolvedValueOnce({ targets: [], session_id: "s1", bar_end_time: "2026-09-06T01:00:00Z", valid_until: "2026-09-06T03:00:00Z" });
    callControl.mockResolvedValueOnce({ results: [{ result_id: "r1", period_time: "2026-09-06T01:00:00Z", targets: [] }], total: 1 });
    await expect(listStrategyTargets("i-1")).resolves.toMatchObject({ session_id: "s1", targets: [] });
    await listStrategyResults("i-1", { session_id: "s1", page: 2, page_size: 10 });
    expect(callControl).toHaveBeenNthCalledWith(1, "strategy", "ListStrategyTargets", { instance_id: "i-1" });
    expect(callControl).toHaveBeenNthCalledWith(2, "strategy", "ListStrategyResults", { instance_id: "i-1", session_id: "s1", page: { page: 2, page_size: 10 } });
  });
});
