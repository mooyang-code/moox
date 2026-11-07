import { beforeEach, describe, expect, it, vi } from "vitest";

const { callTrade } = vi.hoisted(() => ({ callTrade: vi.fn() }));
vi.mock("./http", async importOriginal => ({
  ...(await importOriginal<typeof import("./http")>()),
  callTrade
}));

import { updateLogicalAccountFundSettings, updateLogicalAccountOrderSettings } from "./settings";

describe("Trade settings API", () => {
  beforeEach(() => callTrade.mockReset().mockResolvedValue({ ret_info: { code: 0, msg: "ok" } }));

  it("sends the settings under the logical account id", async () => {
    const order = { resting_ratio: "0.3" } as Parameters<typeof updateLogicalAccountOrderSettings>[1];
    const fund = { reserve_ratio: "0.05" } as Parameters<typeof updateLogicalAccountFundSettings>[1];
    await updateLogicalAccountOrderSettings("logical-1", order);
    await updateLogicalAccountFundSettings("logical-1", fund);
    expect(callTrade).toHaveBeenNthCalledWith(
      1,
      "console",
      "UpdateLogicalAccountOrderSettings",
      { logical_account_id: "logical-1", settings: order },
      { silent: true }
    );
    expect(callTrade).toHaveBeenNthCalledWith(
      2,
      "console",
      "UpdateLogicalAccountFundSettings",
      { logical_account_id: "logical-1", settings: fund },
      { silent: true }
    );
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
    expect(callTrade).toHaveBeenLastCalledWith(
      "console",
      "UpdateLogicalAccountFundSettings",
      { logical_account_id: "logical-1", settings: fund },
      { silent: true }
    );
  });
});
