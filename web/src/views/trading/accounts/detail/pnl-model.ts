import type { PnlAmount, SummarizeAccountPnlReq, SummarizeAccountPnlRsp } from "@/api/trade";
import { fmtClock, pnlClass } from "../shared/format";
import { rangeParams, recordRanges, type RecordRange } from "./record-range";

/** 收益拆解可选的时间范围：不含“全部”（不带时间时后端只算近 7 天，界面写“全部”会误导），资金费最长 90 天。 */
export const pnlRanges = recordRanges.filter(item => item.ms > 0);
export type PnlRange = Exclude<RecordRange, "all">;

export function pnlRequest(logicalAccountId: string, range: PnlRange, now = Date.now()): SummarizeAccountPnlReq {
  return { logical_account_id: logicalAccountId, ...rangeParams(range, now) };
}

/** 金额：千分位，小数最多 8 位、至少 2 位（手续费常是 BNB 之类的小数）；带符号时正数加 +。 */
export function formatPnlAmount(value: string, signed: boolean): string {
  const n = Number(value);
  if (value === "" || !Number.isFinite(n)) return "—";
  const text = n.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 8 });
  return signed && n > 0 ? `+${text}` : text;
}

export interface PnlAmountView {
  asset: string;
  text: string;
  /** 盈亏着色；手续费正数是支付，不着色 */
  tone: "tx-pos" | "tx-neg" | "";
}

export interface PnlRow {
  key: "fees" | "realized" | "funding";
  label: string;
  hint: string;
  amounts: PnlAmountView[];
  /** 没有金额时显示的字 */
  empty: string;
}

/** 说明条：warn 表示汇总不完整，info 表示没有数据的原因 */
export interface PnlNotice {
  level: "info" | "warn";
  text: string;
}

export interface PnlView {
  rows: PnlRow[];
  fillCount: number;
  notice: PnlNotice | null;
}

function amountViews(list: PnlAmount[] | undefined, signed: boolean, colored: boolean): PnlAmountView[] {
  return (list ?? []).map(item => ({
    asset: item.asset,
    text: formatPnlAmount(item.amount, signed),
    tone: colored ? pnlClass(item.amount) : ""
  }));
}

function fundingNotice(rsp: SummarizeAccountPnlRsp): PnlNotice | null {
  const note = rsp.funding_note?.trim() ?? "";
  if (!rsp.funding_available) {
    return { level: "info", text: `没有统计资金费：${note || "这个账户没有资金费数据"}` };
  }
  if (rsp.funding_truncated) {
    const from = Number(rsp.funding_from) > 0 ? `从 ${fmtClock(rsp.funding_from, true)} 起` : "只统计了其中一段";
    return { level: "warn", text: `资金费只是一部分（${from}），不是所选时间范围的完整合计${note ? `。${note}` : ""}` };
  }
  return note ? { level: "info", text: note } : null;
}

/** 后端是 proto3 JSON：空数组、false、0 都不出现，这里统一补成空数组和 false。 */
export function buildPnlView(rsp: SummarizeAccountPnlRsp): PnlView {
  const fundingAvailable = Boolean(rsp.funding_available);
  return {
    rows: [
      { key: "fees", label: "手续费", hint: "支付", amounts: amountViews(rsp.fees, false, false), empty: "无" },
      {
        key: "realized",
        label: "已实现盈亏",
        hint: "",
        amounts: amountViews(rsp.realized_pnl, true, true),
        empty: "无"
      },
      {
        key: "funding",
        label: "资金费",
        hint: "收到为正",
        amounts: fundingAvailable ? amountViews(rsp.funding_fees, true, true) : [],
        empty: fundingAvailable ? "无" : "—"
      }
    ],
    fillCount: Number(rsp.fill_count) || 0,
    notice: fundingNotice(rsp)
  };
}
