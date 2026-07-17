import { createPinia, setActivePinia } from "pinia";
import { beforeEach, describe, expect, it, vi } from "vitest";

const { getStrategyCapabilities } = vi.hoisted(() => ({ getStrategyCapabilities: vi.fn() }));
vi.mock("@/api/strategy", async importOriginal => ({
  ...(await importOriginal<typeof import("@/api/strategy")>()),
  getStrategyCapabilities
}));

import { useStrategyStore } from "./strategy";

describe("strategy capability loading", () => {
  beforeEach(() => {
    setActivePinia(createPinia());
    getStrategyCapabilities.mockReset();
  });

  it("enables Live only for an explicit true response", async () => {
    getStrategyCapabilities.mockResolvedValue({ live_execution_enabled: true });
    const store = useStrategyStore();
    await store.loadCapabilities();
    expect(store.liveExecutionEnabled).toBe(true);
  });

  it("fails closed when capability loading fails", async () => {
    getStrategyCapabilities.mockRejectedValue(new Error("network unavailable"));
    const store = useStrategyStore();
    store.liveExecutionEnabled = true;
    await store.loadCapabilities();
    expect(store.liveExecutionEnabled).toBe(false);
  });
});
