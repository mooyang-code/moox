import { describe, expect, it } from "vitest";
import type { LogicalAccount, TradingAccount } from "@/api/trade/types";
import { environmentPrefix, logicalAccountOptions, resolvedMarkets } from "./logical-account-options";

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

  it("A 股只列开通了股票市场的组合", () => {
    const markets = resolvedMarkets('{"market":{"select":"equity","prefer":"equity"}}');
    expect(markets.types).toEqual([3]);
    const options = logicalAccountOptions(
      [account({ logical_account_id: "crypto", markets: [1] }), account({ logical_account_id: "ashare", markets: [3] })],
      markets
    );
    expect(options.map(item => item.account.logical_account_id)).toEqual(["ashare"]);
  });

  it("已被其他实例绑定的显示为不可选并写明实例", () => {
    const [option] = logicalAccountOptions([account({ owner_instance_id: "inst-1" })]);
    expect(option.disabledReason).toContain("inst-1");
    expect(option.label).toContain("inst-1");
  });

  it("从校验结果读市场模式", () => {
    expect(resolvedMarkets('{"market":{"select":"swap","prefer":"swap"}}')).toEqual({ types: [2] });
    expect(resolvedMarkets('{"market":{"select":"spot","prefer":"swap"}}').types).toEqual([1, 2]);
    expect(resolvedMarkets("bad")).toEqual({});
    expect(resolvedMarkets()).toEqual({});
  });
});

describe("组合账户选项前的环境", () => {
  const testnetMember = new Map([
    ["ta-t", { trading_account_id: "ta-t", execution_mode: 2, live: { environment: 1 } } as unknown as TradingAccount]
  ]);

  it("模拟盘、测试网、生产分别写明；执行账户没读到时实盘只写“实盘”", () => {
    expect(environmentPrefix(account({ execution_mode: 1 }), null)).toBe("模拟盘");
    const live = account({ execution_mode: 2, members: [{ trading_account_id: "ta-t", enabled: true, priority: 1 }] });
    expect(environmentPrefix(live, testnetMember)).toBe("测试网");
    expect(environmentPrefix(live, null)).toBe("实盘");
    expect(environmentPrefix(account({ execution_mode: 2 }), new Map())).toBe("生产环境");
  });

  it("选项文字带环境前缀", () => {
    const [option] = logicalAccountOptions([account({ execution_mode: 1, name: "模拟组合" })], {}, null);
    expect(option.label.startsWith("模拟盘 · 模拟组合")).toBe(true);
  });
});
