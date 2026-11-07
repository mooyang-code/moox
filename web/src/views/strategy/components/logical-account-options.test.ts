import { describe, expect, it } from "vitest";
import type { LogicalAccount } from "@/api/trade/types";
import { logicalAccountOptions, resolvedMarkets } from "./logical-account-options";

const account = (patch: Partial<LogicalAccount>) =>
  ({
    logical_account_id: "a",
    name: "组合",
    control_mode: 1,
    markets: [2],
    ready: true,
    members: [],
    ...patch
  }) as LogicalAccount;

describe("策略绑定组合账户选项", () => {
  it("只列策略驱动且可交易市场包含策略输出市场的账户", () => {
    const options = logicalAccountOptions(
      [
        account({ logical_account_id: "swap" }),
        account({ logical_account_id: "spot", markets: [1] }),
        account({ logical_account_id: "both", markets: [1, 2] }),
        account({ logical_account_id: "manual", control_mode: 2 })
      ],
      { types: [2] }
    );
    expect(options.map(item => item.account.logical_account_id)).toEqual(["swap", "both"]);
  });

  it("混合市场策略要求账户同时可交易现货和永续", () => {
    const options = logicalAccountOptions(
      [
        account({ logical_account_id: "swap" }),
        account({ logical_account_id: "spot", markets: [1] }),
        account({ logical_account_id: "both", markets: [1, 2] }),
        account({ logical_account_id: "empty", markets: [] })
      ],
      resolvedMarkets('{"market":{"select":"both","prefer":"spot"}}')
    );
    expect(options.map(item => item.account.logical_account_id)).toEqual(["both"]);
  });

  it("A 股没有交易账户，列表为空并给出提示", () => {
    const markets = resolvedMarkets('{"market":{"select":"equity","prefer":"equity"}}');
    expect(markets.noAccount).toBe("A 股暂无交易账户");
    expect(logicalAccountOptions([account({}), account({ markets: [1] })], markets)).toEqual([]);
  });

  it("已被其他实例绑定的显示为不可选并写明实例", () => {
    const [option] = logicalAccountOptions([account({ owner_instance_id: "inst-1" })]);
    expect(option.disabledReason).toContain("inst-1");
    expect(option.label).toContain("inst-1");
  });

  it("从校验结果读市场模式", () => {
    expect(resolvedMarkets('{"market":{"select":"swap","prefer":"swap"}}')).toEqual({ types: [2], noAccount: undefined });
    expect(resolvedMarkets('{"market":{"select":"spot","prefer":"swap"}}').types).toEqual([1, 2]);
    expect(resolvedMarkets("bad")).toEqual({});
    expect(resolvedMarkets()).toEqual({});
  });
});
