import { beforeEach, describe, expect, it, vi } from "vitest";

const { callControl } = vi.hoisted(() => ({ callControl: vi.fn() }));
vi.mock("@/api/admin/http", () => ({ callControl }));

import { createInstance, listInstances, listRunners, listStrategies, listStrategyResults, listStrategyTargets, setInstanceEnabled, setRunnerStatus } from "./strategy";

describe("strategy API", () => {
  beforeEach(() => callControl.mockReset());

  it("maps Strategy list responses", async () => {
    callControl.mockResolvedValueOnce({ strategies: [{ strategy_id: "momentum" }], total: 1, page: 1, page_size: 20 });
    await expect(listStrategies({ page: 1, page_size: 20 })).resolves.toMatchObject({
      items: [{ strategy_id: "momentum" }],
      page: { total: 1 }
    });
    expect(callControl).toHaveBeenCalledWith("strategy", "ListStrategies", { page: { page: 1, page_size: 20 } });
  });

  it("sends runner filters and reads results", async () => {
    callControl
      .mockResolvedValueOnce({ runners: [{ runner_id: "runner-1" }], total: 1 })
      .mockResolvedValueOnce({ results: [{ result_id: "result-1" }], total: 1 });
    await listRunners({ strategy_id: "momentum", status: "ENABLED" });
    await listStrategyResults("runner-1", { page: 2, page_size: 10 });
    expect(callControl).toHaveBeenNthCalledWith(1, "strategy", "ListRunners", {
      page: { page: 1, page_size: 20 },
      strategy_id: "momentum",
      space_id: undefined,
      status: "ENABLED"
    });
    expect(callControl).toHaveBeenNthCalledWith(2, "strategy", "ListStrategyResults", {
      runner_id: "runner-1",
      page: { page: 2, page_size: 10 }
    });
  });

  it("uses target weights for the current FULL target and controls only Runner status", async () => {
    callControl
      .mockResolvedValueOnce({ targets: [{ instrument_id: "BTC-USDT-SPOT", target_weight: "0.1" }], command_sequence: "7" })
      .mockResolvedValueOnce({ runner: { runner_id: "runner-1", status: "ENABLED" } });
    await expect(listStrategyTargets("runner-1")).resolves.toEqual({
      targets: [{ instrument_id: "BTC-USDT-SPOT", target_weight: "0.1" }],
      command_sequence: "7"
    });
    await setRunnerStatus("runner-1", "ENABLED");
    expect(callControl).toHaveBeenLastCalledWith("strategy", "SetRunnerStatus", {
      runner_id: "runner-1",
      status: "ENABLED"
    });
  });

  it("uses instance/session fields for the modern strategy contract", async () => {
    callControl
      .mockResolvedValueOnce({ instance: { instance_id: "i-1", strategy_id: "s-1", space_id: "space-1", input_bindings_json: "{}", enabled: false } })
      .mockResolvedValueOnce({ instances: [{ instance_id: "i-1", enabled: true }], total: 1 })
      .mockResolvedValueOnce({ instance: { instance_id: "i-1", enabled: true, session_id: "session-1" } });
    await createInstance({ instance_id: "i-1", strategy_id: "s-1", space_id: "space-1", input_bindings_json: "{}", logical_account_id: "", enabled: false });
    await listInstances({ strategy_id: "s-1", space_id: "space-1", enabled: true });
    await setInstanceEnabled("i-1", true);
    expect(callControl).toHaveBeenNthCalledWith(1, "strategy", "CreateStrategyInstance", {
      instance: { instance_id: "i-1", strategy_id: "s-1", space_id: "space-1", input_bindings_json: "{}", logical_account_id: "", enabled: false }
    });
    expect(callControl).toHaveBeenNthCalledWith(2, "strategy", "ListStrategyInstances", {
      page: { page: 1, page_size: 20 }, strategy_id: "s-1", space_id: "space-1", enabled: true
    });
    expect(callControl).toHaveBeenNthCalledWith(3, "strategy", "SetStrategyInstanceEnabled", { instance_id: "i-1", enabled: true });
  });
});
