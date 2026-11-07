import { describe, expect, it } from "vitest";
import type { LogicalAccount, TradingAccount } from "@/api/trade/types";
import { accountEnvironment, confirmTier, environmentFromQuery, logicalEnvironment, nameConfirmed } from "./environment";

const live = (environment: 0 | 1 | 2) =>
  ({ trading_account_id: "x", execution_mode: 2, live: { environment, credential_secret_id: "s" } }) as TradingAccount;

describe("环境推导", () => {
  it("模拟盘、测试网、生产环境；实盘没写明环境时按生产", () => {
    expect(accountEnvironment({ execution_mode: 1 } as TradingAccount)).toBe("paper");
    expect(accountEnvironment(live(1))).toBe("testnet");
    expect(accountEnvironment(live(2))).toBe("prod");
    expect(accountEnvironment(live(0))).toBe("prod");
    expect(accountEnvironment({ execution_mode: 2 } as TradingAccount)).toBe("prod");
  });

  it("组合的环境取成员的环境；实盘组合没有成员时按生产", () => {
    const accounts = new Map([["t", { ...live(1), trading_account_id: "t" }]]);
    const members = (id: string) => [{ trading_account_id: id, enabled: true, priority: 0 }];
    expect(logicalEnvironment({ execution_mode: 1, members: [] }, accounts)).toBe("paper");
    expect(logicalEnvironment({ execution_mode: 2, members: members("t") } as LogicalAccount, accounts)).toBe("testnet");
    expect(logicalEnvironment({ execution_mode: 2, members: [] } as unknown as LogicalAccount, accounts)).toBe("prod");
    expect(logicalEnvironment({ execution_mode: 2, members: members("missing") } as LogicalAccount, accounts)).toBe("prod");
  });

  it("查询参数只认 testnet、prod，其余回到模拟盘", () => {
    expect(environmentFromQuery("testnet")).toBe("testnet");
    expect(environmentFromQuery("prod")).toBe("prod");
    expect(environmentFromQuery("live")).toBe("paper");
    expect(environmentFromQuery(undefined)).toBe("paper");
  });

  it("三档确认：只有生产环境要输入组合名称", () => {
    expect(confirmTier("paper")).toMatchObject({ banner: "", requiresName: false });
    expect(confirmTier("testnet")).toMatchObject({ requiresName: false });
    expect(confirmTier("testnet").banner).not.toBe("");
    expect(confirmTier("prod").requiresName).toBe(true);
    expect(nameConfirmed(confirmTier("prod"), "组合", "组合")).toBe(true);
    expect(nameConfirmed(confirmTier("prod"), "组", "组合")).toBe(false);
    expect(nameConfirmed(confirmTier("paper"), "", "组合")).toBe(true);
  });
});
