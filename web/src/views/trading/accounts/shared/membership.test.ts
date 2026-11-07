import { describe, expect, it } from "vitest";
import type { LogicalAccount, TradingAccount } from "@/api/trade/types";
import { eligibleAccounts, joinableLogicals, nextPriority } from "./membership";

const logical = (id: string, extra: Partial<LogicalAccount> = {}) =>
  ({
    logical_account_id: id,
    execution_mode: 1,
    settlement_asset: "USDT",
    automation_state: "PAUSED",
    members: [],
    ...extra
  }) as LogicalAccount;
const account = (id: string, extra: Partial<TradingAccount> = {}) =>
  ({
    trading_account_id: id,
    execution_mode: 1,
    settlement_asset: "USDT",
    status: "ENABLED",
    markets: [1],
    ...extra
  }) as TradingAccount;

describe("成员资格", () => {
  it("候选执行账户：环境、模拟或实盘、结算资产一致，已启用，且不在任何组合里；不再限制市场", () => {
    const target = logical("target");
    const other = logical("other", { members: [{ trading_account_id: "taken", enabled: false, priority: 0 }] });
    const list = [
      account("ok", { markets: [2] }),
      account("taken"),
      account("disabled", { status: "DISABLED" }),
      account("asset", { settlement_asset: "USDC" }),
      account("live", { execution_mode: 2 })
    ];
    expect(eligibleAccounts(list, [target, other], target, "paper").map(item => item.trading_account_id)).toEqual(["ok"]);
  });

  it("只能加入同环境、同结算资产、已暂停的组合，优先级排在成员之后", () => {
    const list = [
      logical("ok"),
      logical("running", { automation_state: "ACTIVE" }),
      logical("other-asset", { settlement_asset: "USDC" }),
      logical("live", { execution_mode: 2 })
    ];
    expect(joinableLogicals(list, new Map(), "paper", "usdt").map(item => item.logical_account_id)).toEqual(["ok"]);
    expect(nextPriority(logical("x", { members: [{ trading_account_id: "a", enabled: true, priority: 3 }] }))).toBe(4);
    expect(nextPriority(logical("y"))).toBe(1);
  });
});
