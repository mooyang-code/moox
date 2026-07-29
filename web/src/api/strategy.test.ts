import { beforeEach, describe, expect, it, vi } from "vitest";

const { callControl } = vi.hoisted(() => ({ callControl: vi.fn() }));
vi.mock("@/api/admin/http", () => ({ callControl }));

import { getStrategyCapabilities, normalizePerformance, setExecutionMode } from "./strategy";

describe("strategy api normalization", () => {
  beforeEach(() => callControl.mockReset());

  it("keeps performance sources separate", () => {
    const result = normalizePerformance({
      groups: [
        { performance_source: "paper", points: [] },
        { performance_source: "live", points: [] }
      ]
    });
    expect(result.groups.map(item => item.performance_source)).toEqual(["paper", "live"]);
  });

  it("treats only an explicit true capability as enabled", async () => {
    callControl.mockResolvedValueOnce({ live_execution_enabled: true });
    await expect(getStrategyCapabilities()).resolves.toEqual({ live_execution_enabled: true });
    callControl.mockResolvedValueOnce({ live_execution_enabled: "true" });
    await expect(getStrategyCapabilities()).resolves.toEqual({ live_execution_enabled: false });
  });

  it("sends the exact execution binding and Exchange account when changing mode", async () => {
    callControl.mockResolvedValueOnce({ ret_info: { code: 0 } });
    await setExecutionMode("binding-1", "paper", "test", "operation-1", {
      execution_binding_id: "execution-1",
      exchange_account_id: "account-1"
    });
    expect(callControl).toHaveBeenCalledWith("strategy", "SetExecutionMode", {
      binding_id: "binding-1",
      mode: "paper",
      reason: "test",
      operation_id: "operation-1",
      execution_binding_id: "execution-1",
      exchange_account_id: "account-1"
    });
  });
});
