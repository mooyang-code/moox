import { beforeEach, describe, expect, it, vi } from "vitest";

const { call } = vi.hoisted(() => ({ call: vi.fn() }));
vi.mock("@/api/http", async importOriginal => ({
  ...(await importOriginal<typeof import("@/api/http")>()),
  call
}));

import { updateLogicalAccountFundSettings, updateLogicalAccountOrderSettings } from "./settings";

describe("Trade settings API", () => {
  beforeEach(() => call.mockReset().mockResolvedValue({ ret_info: { code: 0, msg: "ok" } }));

  it("sends the settings under the logical account id", async () => {
    const order = { resting_ratio: "0.3" } as Parameters<typeof updateLogicalAccountOrderSettings>[1];
    const fund = { reserve_ratio: "0.05" } as Parameters<typeof updateLogicalAccountFundSettings>[1];
    await updateLogicalAccountOrderSettings("logical-1", order);
    await updateLogicalAccountFundSettings("logical-1", fund);
    expect(call).toHaveBeenNthCalledWith(1, "trade", "UpdateLogicalAccountOrderSettings", {
      logical_account_id: "logical-1",
      settings: order
    });
    expect(call).toHaveBeenNthCalledWith(2, "trade", "UpdateLogicalAccountFundSettings", {
      logical_account_id: "logical-1",
      settings: fund
    });
  });

  it("资金设置里的保证金率监控字段原样带给后端", async () => {
    const fund = {
      reserve_ratio: "0.05",
      margin_ratio_threshold: "0.4",
      unified_mmr_threshold: "1.3",
      margin_reduce_ratio: "0.2",
      margin_monitor_disabled: true
    } as Parameters<typeof updateLogicalAccountFundSettings>[1];
    await updateLogicalAccountFundSettings("logical-1", fund);
    expect(call).toHaveBeenLastCalledWith("trade", "UpdateLogicalAccountFundSettings", {
      logical_account_id: "logical-1",
      settings: fund
    });
  });
});
