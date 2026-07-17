import { beforeEach, describe, expect, it, vi } from "vitest";

const { callControl } = vi.hoisted(() => ({ callControl: vi.fn() }));
vi.mock("@/api/admin/http", () => ({ callControl }));

import { getStrategyCapabilities, normalizePerformance } from "./strategy";

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
});
