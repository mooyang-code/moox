import { describe, expect, it } from "vitest";
import type { LogicalAccount, TradingAccount } from "@/api/trade/types";
import {
  accountEnvironment,
  confirmTier,
  environmentMenus,
  environmentOfRoute,
  logicalDetailPath,
  logicalEnvironment,
  logicalInEnvironment,
  nameConfirmed
} from "./environment";

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

  it("没有可判断成员的实盘组合按进来的页签显示，不知道从哪进来时按生产；有成员时以成员为准", () => {
    const accounts = new Map([["p", { ...live(0), trading_account_id: "p" }]]);
    const empty = { execution_mode: 2, members: [] } as unknown as LogicalAccount;
    expect(logicalEnvironment(empty, accounts, "testnet")).toBe("testnet");
    expect(logicalEnvironment(empty, accounts, "prod")).toBe("prod");
    expect(logicalEnvironment(empty, accounts, "paper")).toBe("prod");
    expect(logicalEnvironment(empty, accounts)).toBe("prod");
    const withProd = { execution_mode: 2, members: [{ trading_account_id: "p", enabled: true, priority: 0 }] } as LogicalAccount;
    expect(logicalEnvironment(withProd, accounts, "testnet")).toBe("prod");
    expect(logicalEnvironment({ execution_mode: 1, members: [] }, accounts, "testnet")).toBe("paper");
  });

  it("没有可判断成员的实盘组合在测试网和生产环境两个页签都列出，有成员的只在成员所在环境", () => {
    const accounts = new Map([["t", { ...live(1), trading_account_id: "t" }]]);
    const empty = { execution_mode: 2, members: [] } as unknown as LogicalAccount;
    const inEnv = (logical: LogicalAccount) =>
      (["paper", "testnet", "prod"] as const).filter(env => logicalInEnvironment(logical, accounts, env));
    expect(inEnv(empty)).toEqual(["testnet", "prod"]);
    expect(
      inEnv({ execution_mode: 2, members: [{ trading_account_id: "t", enabled: true, priority: 0 }] } as LogicalAccount)
    ).toEqual(["testnet"]);
    expect(inEnv({ execution_mode: 1, members: [] } as unknown as LogicalAccount)).toEqual(["paper"]);
  });

  it("页面所属环境读路由 meta.env，没写或不认识时为空", () => {
    expect(environmentOfRoute({ meta: { env: "testnet" } })).toBe("testnet");
    expect(environmentOfRoute({ meta: { env: "prod" } })).toBe("prod");
    expect(environmentOfRoute({ meta: { env: "live" } })).toBeUndefined();
    expect(environmentOfRoute({ meta: {} })).toBeUndefined();
    expect(environmentOfRoute({})).toBeUndefined();
  });

  it("三个环境各一个菜单，详情地址在菜单地址下面", () => {
    expect(Object.values(environmentMenus).map(item => item.title)).toEqual(["模拟账户", "测试账户", "实盘账户"]);
    expect(logicalDetailPath("paper", "paper-logical-1")).toBe("/trading/paper/paper-logical-1");
    expect(logicalDetailPath("prod", "a/b")).toBe("/trading/live/a%2Fb");
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
