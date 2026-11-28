import { describe, expect, it } from "vitest";
import { AccountEnvironment, ExecutionMode, type LogicalAccount, type TradingAccount } from "@/api/trade/types";
import { accountMapOf, comboRows, executionRows } from "./account-list";

const account = (id: string, extra: Partial<TradingAccount> = {}) =>
  ({
    trading_account_id: id,
    name: id,
    exchange: "binance",
    markets: ["spot"],
    execution_mode: ExecutionMode.PAPER,
    status: "ENABLED",
    snapshot: { equity: "100" },
    ...extra
  }) as TradingAccount;
const logical = (id: string, members: string[], extra: Partial<LogicalAccount> = {}) =>
  ({
    logical_account_id: id,
    name: id,
    execution_mode: ExecutionMode.PAPER,
    environment: AccountEnvironment.PAPER,
    settlement_asset: "USDT",
    members: members.map((trading_account_id, priority) => ({ trading_account_id, enabled: true, priority })),
    ...extra
  }) as LogicalAccount;

describe("账户列表模型", () => {
  it("按后端 environment 筛选组合，直接使用后端 total_equity，交易所去重", () => {
    const accounts = accountMapOf([
      account("a"),
      account("b", { exchange: "okx" }),
      account("t", {
        execution_mode: ExecutionMode.LIVE,
        live: { environment: AccountEnvironment.TESTNET, credential_secret_id: "s" }
      })
    ]);
    const logicals = [
      logical("p", ["a", "b"], { total_equity: "150" }),
      logical("n", ["t"], { execution_mode: ExecutionMode.LIVE, environment: AccountEnvironment.TESTNET, total_equity: "0" })
    ];
    const paper = comboRows(logicals, accounts, "paper");
    expect(paper).toHaveLength(1);
    expect(paper[0]).toMatchObject({ equity: 150, exchangesText: "币安、OKX" });
    expect(paper[0].members.map(item => item.trading_account_id)).toEqual(["a", "b"]);
    const testnet = comboRows(logicals, accounts, "testnet");
    expect(testnet.map(row => [row.key, row.equity])).toEqual([["n", 0]]);
    expect(comboRows(logicals, accounts, "prod")).toEqual([]);
  });

  it("后端未给出 total_equity（成员未同步）时权益为空，区别于权益为零", () => {
    const accounts = accountMapOf([account("a", { snapshot: undefined })]);
    expect(comboRows([logical("p", ["a"])], accounts, "paper")[0].equity).toBeNull();
    expect(comboRows([logical("z", ["a"], { total_equity: "0" })], accounts, "paper")[0].equity).toBe(0);
  });

  it("执行账户按环境列出，带上所属组合和成员设置（含停用的成员），没加入的组合为空", () => {
    const logicals = [logical("p", ["a"])];
    logicals[0].members.push({ trading_account_id: "off", enabled: false, priority: 9 });
    const accounts = [account("a"), account("off"), account("free"), account("live", { execution_mode: ExecutionMode.LIVE })];
    const paper = executionRows(accounts, logicals, "paper");
    expect(paper.map(row => [row.key, row.logical?.logical_account_id ?? null, row.priority, row.enabled])).toEqual([
      ["a", "p", 0, true],
      ["off", "p", 9, false],
      ["free", null, null, null]
    ]);
    expect(executionRows(accounts, logicals, "prod").map(row => row.key)).toEqual(["live"]);
  });

  it("已关闭的模拟账户（已停用）不列，停用的实盘账户还在", () => {
    const accounts = [
      account("closed", { status: "DISABLED" }),
      account("free"),
      account("live-off", { execution_mode: ExecutionMode.LIVE, status: "DISABLED" })
    ];
    expect(executionRows(accounts, [], "paper").map(row => row.key)).toEqual(["free"]);
    expect(executionRows(accounts, [], "prod").map(row => row.key)).toEqual(["live-off"]);
  });

  it("从列表响应直接解析持仓标的数与近 24 小时权益变化比例，缺失或非法时回退为空和 0", () => {
    const accounts = accountMapOf([account("a")]);
    const rows = comboRows(
      [
        logical("p1", ["a"], { position_count: 3, equity_change_24h: "10", equity_change_pct_24h: "0.1" }),
        logical("p2", ["a"], { equity_change_pct_24h: "" }),
        logical("p3", ["a"], { position_count: 1, equity_change_pct_24h: "not-a-number" })
      ],
      accounts,
      "paper"
    );
    expect(rows.map(row => [row.key, row.positionCount, row.equityChangePct24h])).toEqual([
      ["p1", 3, 0.1],
      ["p2", 0, null],
      ["p3", 1, null]
    ]);
  });
});
