import { describe, expect, it } from "vitest";
import { AccountEnvironment, ControlMode, ExecutionMode, type LogicalAccount, type TradingAccount } from "@/api/trade/types";
import { eligibleAccounts, joinableLogicals, nextPriority } from "./membership";

const logical = (id: string, extra: Partial<LogicalAccount> = {}) =>
  ({
    logical_account_id: id,
    execution_mode: ExecutionMode.PAPER,
    environment: AccountEnvironment.PAPER,
    settlement_asset: "USDT",
    automation_state: "PAUSED",
    members: [],
    ...extra
  }) as LogicalAccount;
const account = (id: string, extra: Partial<TradingAccount> = {}) =>
  ({
    trading_account_id: id,
    execution_mode: ExecutionMode.PAPER,
    settlement_asset: "USDT",
    status: "ENABLED",
    markets: ["spot"],
    ...extra
  }) as TradingAccount;

describe("成员资格", () => {
  it("候选执行账户：环境、模拟或实盘、结算资产一致，已启用，且不在任何组合里；不再限制市场", () => {
    const target = logical("target");
    const other = logical("other", { members: [{ trading_account_id: "taken", enabled: false, priority: 0 }] });
    const list = [
      account("ok", { markets: ["swap"] }),
      account("taken"),
      account("disabled", { status: "DISABLED" }),
      account("asset", { settlement_asset: "USDC" }),
      account("live", { execution_mode: ExecutionMode.LIVE })
    ];
    expect(eligibleAccounts(list, [target, other], target).map(item => item.trading_account_id)).toEqual(["ok"]);
  });

  it("空组合按自身后端 environment 匹配同环境的执行账户", () => {
    const live = (id: string, environment: typeof AccountEnvironment.TESTNET | typeof AccountEnvironment.PRODUCTION) =>
      account(id, { execution_mode: ExecutionMode.LIVE, live: { environment, credential_secret_id: "s" } });
    const list = [live("testnet", AccountEnvironment.TESTNET), live("prod", AccountEnvironment.PRODUCTION), account("paper")];
    const emptyTestnet = logical("empty-t", { execution_mode: ExecutionMode.LIVE, environment: AccountEnvironment.TESTNET });
    const emptyProd = logical("empty-p", { execution_mode: ExecutionMode.LIVE, environment: AccountEnvironment.PRODUCTION });
    expect(eligibleAccounts(list, [emptyTestnet], emptyTestnet).map(item => item.trading_account_id)).toEqual(["testnet"]);
    expect(eligibleAccounts(list, [emptyProd], emptyProd).map(item => item.trading_account_id)).toEqual(["prod"]);
  });

  it("只能加入同环境、同结算资产、可编辑成员（自主下单或已暂停）的组合，优先级排在成员之后", () => {
    const list = [
      logical("ok"),
      logical("manual", { control_mode: ControlMode.MANUAL, automation_state: "" }),
      logical("running", { control_mode: ControlMode.STRATEGY, automation_state: "ACTIVE" }),
      logical("other-asset", { settlement_asset: "USDC" }),
      logical("live", { execution_mode: ExecutionMode.LIVE, environment: AccountEnvironment.PRODUCTION })
    ];
    expect(joinableLogicals(list, "paper", "usdt").map(item => item.logical_account_id)).toEqual(["ok", "manual"]);
    expect(nextPriority(logical("x", { members: [{ trading_account_id: "a", enabled: true, priority: 3 }] }))).toBe(4);
    expect(nextPriority(logical("y"))).toBe(1);
  });
});
