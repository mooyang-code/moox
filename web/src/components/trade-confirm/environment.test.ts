import { describe, expect, it } from "vitest";
import { AccountEnvironment, ExecutionMode, type TradingAccount } from "@/api/trade/types";
import {
  accountEnvironment,
  confirmTier,
  environmentMenus,
  environmentOfRoute,
  logicalDetailPath,
  logicalEnvironment,
  logicalInEnvironment,
  nameConfirmed,
  toAccountEnvironment
} from "./environment";

const live = (environment: AccountEnvironment) =>
  ({
    trading_account_id: "x",
    execution_mode: ExecutionMode.LIVE,
    live: { environment, credential_secret_id: "s" }
  }) as TradingAccount;

describe("环境映射", () => {
  it("执行账户区分模拟盘、测试网、生产环境；实盘没写明环境时按生产", () => {
    expect(accountEnvironment({ execution_mode: ExecutionMode.PAPER } as TradingAccount)).toBe("paper");
    expect(accountEnvironment(live(AccountEnvironment.TESTNET))).toBe("testnet");
    expect(accountEnvironment(live(AccountEnvironment.PRODUCTION))).toBe("prod");
    expect(accountEnvironment(live(AccountEnvironment.UNSPECIFIED))).toBe("prod");
    expect(accountEnvironment({ execution_mode: ExecutionMode.LIVE } as TradingAccount)).toBe("prod");
  });

  it("组合账户的环境直接取后端返回的三值 environment，并在对应菜单精确匹配", () => {
    expect(logicalEnvironment({ environment: AccountEnvironment.PAPER })).toBe("paper");
    expect(logicalEnvironment({ environment: AccountEnvironment.TESTNET })).toBe("testnet");
    expect(logicalEnvironment({ environment: AccountEnvironment.PRODUCTION })).toBe("prod");

    const inEnv = (environment: AccountEnvironment) =>
      (["paper", "testnet", "prod"] as const).filter(env => logicalInEnvironment({ environment }, env));
    expect(inEnv(AccountEnvironment.PAPER)).toEqual(["paper"]);
    expect(inEnv(AccountEnvironment.TESTNET)).toEqual(["testnet"]);
    expect(inEnv(AccountEnvironment.PRODUCTION)).toEqual(["prod"]);
  });

  it("前端环境与后端协议枚举双向一致", () => {
    expect(toAccountEnvironment("paper")).toBe(AccountEnvironment.PAPER);
    expect(toAccountEnvironment("testnet")).toBe(AccountEnvironment.TESTNET);
    expect(toAccountEnvironment("prod")).toBe(AccountEnvironment.PRODUCTION);
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
