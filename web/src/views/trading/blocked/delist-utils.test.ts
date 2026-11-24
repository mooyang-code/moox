import { describe, expect, it } from "vitest";
import type { DelistRecord } from "@/api/trade/delist";
import {
  DELIST_TIME_HINT,
  PAPER_ONLY_NOTE,
  actionStatusText,
  confirmCheckboxText,
  createLatestGuard,
  delistSnapshot,
  delistStatusText,
  delistTimeError,
  holderLine,
  isPastDelistTime,
  pastTimeNote,
  plannedRoundText,
  scopeLabel,
  skippedLine,
  sortDelists,
  summarizeHolders
} from "./delist-utils";

describe("手工登记下架", () => {
  const holder = (patch: Partial<Parameters<typeof summarizeHolders>[0][number]>) => ({
    space_id: "crypto",
    logical_account_id: "la-1",
    trading_account_id: "ta-1",
    quantity: "1",
    dust: false,
    paper: false,
    status: "ENABLED",
    note: "",
    ...patch
  });

  it("预览结果分成实盘、模拟盘和零头", () => {
    const summary = summarizeHolders([
      holder({ logical_account_id: "live" }),
      holder({ logical_account_id: "paper", paper: true }),
      holder({ logical_account_id: "dust", dust: true }),
      holder({ logical_account_id: "paper-dust", dust: true, paper: true })
    ]);
    expect(summary.live.map(item => item.logical_account_id)).toEqual(["live"]);
    expect(summary.paper.map(item => item.logical_account_id)).toEqual(["paper"]);
    expect(summary.dust.map(item => item.logical_account_id)).toEqual(["dust", "paper-dust"]);
  });

  it("持有者一行带上停用、出错的状态和标注；没能判断的账户单独成行", () => {
    expect(holderLine(holder({ quantity: "2" }))).toBe("crypto / la-1（执行账户 ta-1）数量 2");
    expect(holderLine(holder({ status: "DISABLED", note: "取不到报价" }))).toContain(
      "账户已停用，平仓会失败，需要人工处理；取不到报价"
    );
    expect(skippedLine({ trading_account_id: "ta-9", reason: "资金设置读不出来" })).toBe("执行账户 ta-9：资金设置读不出来");
  });

  it("二次确认的勾选框文字：几件要确认的事同时出现时合并成一句", () => {
    const base = { previewFailed: false, verified: true, skipped: 0, live: 0, willProcess: true };
    expect(confirmCheckboxText(base)).toBe("");
    expect(confirmCheckboxText({ ...base, previewFailed: true })).toBe("我知道无法确认持有情况，仍然登记");
    expect(confirmCheckboxText({ ...base, live: 1 })).toBe("我确认有实盘组合会被平仓，仍然登记");
    expect(confirmCheckboxText({ ...base, live: 1, willProcess: false })).toBe("我确认有实盘组合持有这个标的，仍然登记");
    expect(confirmCheckboxText({ ...base, live: 1, skipped: 2 })).toBe(
      "我确认有实盘组合会被平仓，并且有账户没能判断持有情况，仍然登记"
    );
  });

  it("下架时间必须填，允许补录过去的时间；预览时存下规整过的登记参数", () => {
    expect(delistTimeError(null)).toBe("请选择下架时间");
    expect(delistTimeError(1_700_000_000_000)).toBe("");
    expect(isPastDelistTime(1000, 2000)).toBe(true);
    const form = {
      exchange: "binance" as const,
      market: "SPOT" as const,
      exchange_symbol: " fooUsdt ",
      delist_at: 5000,
      paper_only: true
    };
    expect(delistSnapshot(form, 1000)).toEqual({
      snapshot: {
        exchange: "binance",
        market: "SPOT",
        exchange_symbol: "FOOUSDT",
        delist_at_ms: 5000,
        paper_only: true,
        past: false
      }
    });
    expect(delistSnapshot({ ...form, exchange_symbol: " " }, 1000)).toEqual({ error: "请填写交易所代码" });
  });

  it("只认最后一次请求：换号后旧序号作废", () => {
    const guard = createLatestGuard();
    const first = guard.next();
    const second = guard.next();
    expect(guard.isLatest(first)).toBe(false);
    guard.reset();
    expect(guard.isLatest(second)).toBe(false);
  });

  it("演练只对模拟盘生效；过去时间的说明区分真实下架与演练", () => {
    expect(PAPER_ONLY_NOTE).toContain("实盘组合照常买卖");
    expect(pastTimeNote(false)).toContain("名单保留、不会解除");
    expect(pastTimeNote(true)).toContain("演练核对结束后删掉");
    expect(DELIST_TIME_HINT).toContain("按 UTC 填写");
  });
});

describe("下架记录", () => {
  const record = (patch: Partial<DelistRecord>): DelistRecord => ({
    delist_id: "d",
    exchange: 1,
    market_type: 1,
    exchange_symbol: "FOOUSDT",
    scope: "ALL",
    delist_at_ms: 0,
    notice_source: "MANUAL",
    source_event_id: "e",
    status: "ACTIVE",
    planned_round: 0,
    note: "",
    result: "",
    created_at_ms: 0,
    finished_at_ms: 0,
    flattens: [],
    ...patch
  });

  it("进行中的排前面，其余按创建时间倒序", () => {
    const sorted = sortDelists([
      record({ delist_id: "old-done", status: "COMPLETED", created_at_ms: 1 }),
      record({ delist_id: "new-done", status: "FAILED", created_at_ms: 3 }),
      record({ delist_id: "active", status: "ACTIVE", created_at_ms: 2 })
    ]);
    expect(sorted.map(item => item.delist_id)).toEqual(["active", "new-done", "old-done"]);
  });

  it("状态、范围、轮次与动作状态的文字", () => {
    expect(delistStatusText("RESCHEDULED")).toBe("已改期，由新记录接着处理");
    expect(scopeLabel("PAPER_ONLY")).toBe("仅模拟盘");
    expect(scopeLabel("ALL")).toBe("全部");
    expect(plannedRoundText(2)).toBe("已按下架前 2 小时复查");
    expect(actionStatusText("")).toBe("还没发起");
    expect(actionStatusText("PARTIAL")).toBe("只平了一部分");
  });
});
