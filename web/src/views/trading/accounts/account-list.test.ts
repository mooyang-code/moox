import { describe, expect, it } from "vitest";
import type { EquityPoint, LogicalAccount, TradingAccount } from "@/api/trade/types";
import { accountMapOf, comboRows, equityChange, executionRows } from "./account-list";

const account = (id: string, extra: Partial<TradingAccount> = {}) =>
  ({
    trading_account_id: id,
    name: id,
    exchange: 1,
    markets: [1],
    execution_mode: 1,
    status: "ENABLED",
    snapshot: { equity: "100" },
    ...extra
  }) as TradingAccount;
const logical = (id: string, members: string[], extra: Partial<LogicalAccount> = {}) =>
  ({
    logical_account_id: id,
    name: id,
    execution_mode: 1,
    settlement_asset: "USDT",
    members: members.map((trading_account_id, priority) => ({ trading_account_id, enabled: true, priority })),
    ...extra
  }) as LogicalAccount;

describe("账户列表模型", () => {
  it("按环境筛选组合，成员权益求和，交易所去重", () => {
    const accounts = accountMapOf([
      account("a"),
      account("b", { exchange: 2, snapshot: { equity: "50" } as TradingAccount["snapshot"] }),
      account("t", { execution_mode: 2, live: { environment: 1, credential_secret_id: "s" } })
    ]);
    const logicals = [logical("p", ["a", "b"]), logical("n", ["t"], { execution_mode: 2 })];
    const paper = comboRows(logicals, accounts, "paper");
    expect(paper).toHaveLength(1);
    expect(paper[0]).toMatchObject({ equity: 150, exchangesText: "币安、OKX" });
    expect(paper[0].members.map(item => item.trading_account_id)).toEqual(["a", "b"]);
    expect(comboRows(logicals, accounts, "testnet").map(row => row.key)).toEqual(["n"]);
    expect(comboRows(logicals, accounts, "prod")).toEqual([]);
  });

  it("没有任何成员给出权益时权益为空，而不是 0", () => {
    const accounts = accountMapOf([account("a", { snapshot: undefined })]);
    expect(comboRows([logical("p", ["a"])], accounts, "paper")[0].equity).toBeNull();
  });

  it("执行账户按环境列出，带上所属组合和成员设置（含停用的成员），没加入的组合为空", () => {
    const logicals = [logical("p", ["a"])];
    logicals[0].members.push({ trading_account_id: "off", enabled: false, priority: 9 });
    const accounts = [account("a"), account("off"), account("free"), account("live", { execution_mode: 2 })];
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
      account("live-off", { execution_mode: 2, status: "DISABLED" })
    ];
    expect(executionRows(accounts, [], "paper").map(row => row.key)).toEqual(["free"]);
    expect(executionRows(accounts, [], "prod").map(row => row.key)).toEqual(["live-off"]);
  });

  it("近 24 小时变化用窗口内首尾两点，点数不足或起点为 0 时为空", () => {
    const points = (values: number[]) =>
      values.map((equity, index) => ({ bucket_time: String(index), equity: String(equity) }) as EquityPoint);
    expect(equityChange(points([100, 90, 110]))).toBeCloseTo(0.1);
    expect(equityChange(points([100]))).toBeNull();
    expect(equityChange(points([0, 5]))).toBeNull();
  });
});
