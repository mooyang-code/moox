import { describe, expect, it } from "vitest";
import type { Holding, LogicalAccountTarget, OperatorAction, Position, TradingAccount } from "@/api/trade";
import { flattenPreview, isFlattenRunning, memberTotals, paperInitialBalance, tabFromQuery, targetExpired } from "./detail-model";

const account = (equity: string, extra: Partial<TradingAccount["snapshot"]> = {}) =>
  ({ snapshot: { equity, available_funds: "10", used_margin: "2", unrealized_pnl: "-1", ...extra } }) as TradingAccount;

describe("详情页模型", () => {
  it("页签取自查询参数，未知值回到概览", () => {
    expect(tabFromQuery("positions")).toBe("positions");
    expect(tabFromQuery("settings")).toBe("settings");
    expect(tabFromQuery("nope")).toBe("overview");
    expect(tabFromQuery(undefined)).toBe("overview");
  });

  it("成员资金合计；没有快照时权益为空", () => {
    expect(memberTotals([account("100"), account("50")])).toEqual({
      equity: 150,
      available: 20,
      margin: 4,
      unrealizedPnl: -2
    });
    expect(memberTotals([{} as TradingAccount]).equity).toBeNull();
    expect(paperInitialBalance([{ paper: { initial_balance: "1000" } } as TradingAccount])).toBe(1000);
    expect(paperInitialBalance([{} as TradingAccount])).toBeNull();
  });

  it("清仓预览按执行账户限定范围，永续取标记价、现货取估值", () => {
    const snapshot = {
      swapPositions: [
        { trading_account_id: "a", signed_quantity: "-2", mark_price: "10" },
        { trading_account_id: "b", signed_quantity: "1", mark_price: "5" }
      ] as Position[],
      spotHoldings: [
        { trading_account_id: "a", instrument_id: "BTC-USDT-SPOT", quantity: "1", market_value: "30" },
        { trading_account_id: "a", instrument_id: "", quantity: "100", market_value: "100" }
      ] as Holding[]
    };
    expect(flattenPreview(snapshot)).toEqual({ positions: 3, value: 55 });
    expect(flattenPreview(snapshot, "a")).toEqual({ positions: 2, value: 50 });
  });

  it("目标过期：状态为过期或有效期已过", () => {
    const target = (extra: Partial<LogicalAccountTarget>) => ({ status: "CONVERGING", ...extra }) as LogicalAccountTarget;
    expect(targetExpired(null)).toBe(false);
    expect(targetExpired(target({ status: "EXPIRED" }))).toBe(true);
    expect(targetExpired(target({ valid_until: 1000 }), 2000)).toBe(true);
    expect(targetExpired(target({ valid_until: 3000 }), 2000)).toBe(false);
  });

  it("只有进行中的清仓动作算清仓进行中", () => {
    const action = (action_type: string, status: string) => ({ action_type, status }) as OperatorAction;
    expect(isFlattenRunning(action("FLATTEN", "RUNNING"))).toBe(true);
    expect(isFlattenRunning(action("FLATTEN", "COMPLETED"))).toBe(false);
    expect(isFlattenRunning(action("SUBMIT_ORDER", "RUNNING"))).toBe(false);
    expect(isFlattenRunning(null)).toBe(false);
  });
});
