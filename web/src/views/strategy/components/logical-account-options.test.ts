import { describe, expect, it } from "vitest";
import type { StrategyInstance } from "@/api/strategy-types";
import { AccountEnvironment, ControlMode, type LogicalAccount } from "@/api/trade/types";
import { enabledInstanceAccounts, environmentPrefix, logicalAccountOptions, marketsToResolved } from "./logical-account-options";

const account = (patch: Partial<LogicalAccount>) =>
  ({
    logical_account_id: "a",
    name: "组合",
    control_mode: ControlMode.STRATEGY,
    environment: AccountEnvironment.PAPER,
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
      marketsToResolved(["spot", "swap"])
    );
    expect(options.map(item => item.account.logical_account_id)).toEqual(["both"]);
  });

  it("A 股只列开通了股票市场的组合", () => {
    const markets = marketsToResolved(["equity"]);
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
    const [option, free] = logicalAccountOptions([account({}), account({ logical_account_id: "c" })], {}, bound);
    expect(option.disabledReason).toContain("inst-1");
    expect(option.label).toContain("inst-1");
    expect(free.disabledReason).toBe("");
  });

  it("实例列表读不到时不置灰，由启用时的服务端检查兜底", () => {
    const [option] = logicalAccountOptions([account({})], {}, null);
    expect(option.disabledReason).toBe("");
  });

  it("过滤未知市场，仅保留交易模块支持的市场", () => {
    expect(marketsToResolved(["spot", "swap", "unknown"])).toEqual({ types: ["spot", "swap"] });
    expect(marketsToResolved([])).toEqual({ types: [] });
  });
});

describe("组合账户选项前的环境", () => {
  it("模拟盘、测试网、生产直接取后端 environment 字段", () => {
    expect(environmentPrefix(account({ environment: AccountEnvironment.PAPER }))).toBe("模拟盘");
    expect(environmentPrefix(account({ environment: AccountEnvironment.TESTNET }))).toBe("测试网");
    expect(environmentPrefix(account({ environment: AccountEnvironment.PRODUCTION }))).toBe("生产环境");
  });

  it("选项文字带环境前缀", () => {
    const [option] = logicalAccountOptions([account({ environment: AccountEnvironment.PAPER, name: "模拟组合" })], {}, null);
    expect(option.label.startsWith("模拟盘 · 模拟组合")).toBe(true);
  });
});
