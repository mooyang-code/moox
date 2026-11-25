import { describe, expect, it } from "vitest";
import type { StrategyInstance } from "@/api/strategy-types";
import type { LogicalAccount, TradingAccount } from "@/api/trade/types";
import { enabledInstanceAccounts, environmentPrefix, logicalAccountOptions, resolvedMarkets } from "./logical-account-options";

const account = (patch: Partial<LogicalAccount>) =>
  ({
    logical_account_id: "a",
    name: "组合",
    control_mode: 1,
    markets: ["swap"],
    ready: true,
    members: [],
    ...patch
  }) as LogicalAccount;

describe("策略绑定组合账户选项", () => {
  it("只列策略驱动且可交易市场包含策略输出市场的账户", () => {
    const options = logicalAccountOptions(
      [
        account({ logical_account_id: "swap" }),
        account({ logical_account_id: "spot", markets: ["spot"] }),
        account({ logical_account_id: "both", markets: ["spot", "swap"] }),
        account({ logical_account_id: "manual", control_mode: 2 })
      ],
      { types: ["swap"] }
    );
    expect(options.map(item => item.account.logical_account_id)).toEqual(["swap", "both"]);
  });

  it("混合市场策略要求账户同时可交易现货和永续", () => {
    const options = logicalAccountOptions(
      [
        account({ logical_account_id: "swap" }),
        account({ logical_account_id: "spot", markets: ["spot"] }),
        account({ logical_account_id: "both", markets: ["spot", "swap"] }),
        account({ logical_account_id: "empty", markets: [] })
      ],
      resolvedMarkets('{"market":{"select":"both","prefer":"spot"}}')
    );
    expect(options.map(item => item.account.logical_account_id)).toEqual(["both"]);
  });

  it("A 股只列开通了股票市场的组合", () => {
    const markets = resolvedMarkets('{"market":{"select":"equity","prefer":"equity"}}');
    expect(markets.types).toEqual(["equity"]);
    const options = logicalAccountOptions(
      [
        account({ logical_account_id: "crypto", markets: ["spot"] }),
        account({ logical_account_id: "ashare", markets: ["equity"] })
      ],
      markets
    );
    expect(options.map(item => item.account.logical_account_id)).toEqual(["ashare"]);
  });

  it("已被启用实例绑定的显示为不可选并写明实例，绑定关系取自 Strategy 的实例列表", () => {
    const instances = [
      { instance_id: "inst-off", logical_account_id: "a", enabled: false },
      { instance_id: "inst-1", logical_account_id: "a", enabled: true },
      { instance_id: "inst-2", logical_account_id: "b", enabled: true }
    ] as StrategyInstance[];
    const bound = enabledInstanceAccounts(instances);
    expect([...bound]).toEqual([
      ["a", "inst-1"],
      ["b", "inst-2"]
    ]);
    const [option, free] = logicalAccountOptions([account({}), account({ logical_account_id: "c" })], {}, null, bound);
    expect(option.disabledReason).toContain("inst-1");
    expect(option.label).toContain("inst-1");
    expect(free.disabledReason).toBe("");
  });

  it("实例列表读不到时不置灰，由启用时的服务端检查兜底", () => {
    const [option] = logicalAccountOptions([account({})], {}, null, null);
    expect(option.disabledReason).toBe("");
  });

  it("从校验结果读市场模式", () => {
    expect(resolvedMarkets('{"market":{"select":"swap","prefer":"swap"}}')).toEqual({ types: ["swap"] });
    expect(resolvedMarkets('{"market":{"select":"spot","prefer":"swap"}}').types).toEqual(["spot", "swap"]);
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
