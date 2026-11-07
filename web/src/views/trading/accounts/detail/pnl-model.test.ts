import { describe, expect, it } from "vitest";
import type { SummarizeAccountPnlRsp } from "@/api/trade";
import { buildPnlView, formatPnlAmount, pnlRanges, pnlRequest } from "./pnl-model";

const rsp = (extra: Partial<SummarizeAccountPnlRsp> = {}): SummarizeAccountPnlRsp => ({
  ret_info: { code: 0, msg: "ok" },
  ...extra
});

describe("收益拆解的时间范围和请求", () => {
  it("不含“全部”：不带时间时后端只算近 7 天，写“全部”会误导", () => {
    expect(pnlRanges.map(item => item.key)).toEqual(["1d", "7d", "30d"]);
  });

  it("按组合和所选范围换算起止毫秒", () => {
    expect(pnlRequest("logical-1", "1d", 100_000_000)).toEqual({
      logical_account_id: "logical-1",
      start_time: "13600000",
      end_time: "100000000"
    });
  });
});

describe("收益拆解金额", () => {
  it("小数最多 8 位，盈亏带正号，读不出来显示破折号", () => {
    expect(formatPnlAmount("1234.5", false)).toBe("1,234.50");
    expect(formatPnlAmount("0.00012345", false)).toBe("0.00012345");
    expect(formatPnlAmount("12", true)).toBe("+12.00");
    expect(formatPnlAmount("-3.5", true)).toBe("-3.50");
    expect(formatPnlAmount("", true)).toBe("—");
    expect(formatPnlAmount("abc", true)).toBe("—");
  });
});

describe("收益拆解视图", () => {
  it("各资产分别一行，不折算；手续费正数是支付，不着色，盈亏和资金费按正负着色", () => {
    const view = buildPnlView(
      rsp({
        fees: [
          { asset: "USDT", amount: "1.5" },
          { asset: "BNB", amount: "0.002" }
        ],
        realized_pnl: [
          { asset: "USDT", amount: "20" },
          { asset: "USDC", amount: "-3" }
        ],
        funding_fees: [{ asset: "USDT", amount: "-0.8" }],
        fill_count: "12",
        funding_available: true
      })
    );
    expect(view.fillCount).toBe(12);
    const [fees, realized, funding] = view.rows;
    expect(fees.amounts).toEqual([
      { asset: "USDT", text: "1.50", tone: "" },
      { asset: "BNB", text: "0.002", tone: "" }
    ]);
    expect(realized.amounts).toEqual([
      { asset: "USDT", text: "+20.00", tone: "tx-pos" },
      { asset: "USDC", text: "-3.00", tone: "tx-neg" }
    ]);
    expect(funding.amounts).toEqual([{ asset: "USDT", text: "-0.80", tone: "tx-neg" }]);
    expect(view.notice).toBeNull();
  });

  it("proto3 省略零值：没有数组、没有布尔时按空和 false 处理", () => {
    const view = buildPnlView(rsp());
    expect(view.fillCount).toBe(0);
    expect(view.rows.map(row => row.amounts)).toEqual([[], [], []]);
    // funding_available 缺省就是 false：说明没统计资金费，不是资金费为 0
    expect(view.rows[2].empty).toBe("—");
    expect(view.notice?.level).toBe("info");
  });

  it("资金费不可用时不显示金额，用 funding_note 说明原因", () => {
    const view = buildPnlView(
      rsp({ funding_available: false, funding_fees: [{ asset: "USDT", amount: "9" }], funding_note: "模拟盘没有资金费流水" })
    );
    expect(view.rows[2].amounts).toEqual([]);
    expect(view.notice).toEqual({ level: "info", text: "没有统计资金费：模拟盘没有资金费流水" });
  });

  it("资金费被截断时给出警告，带起点和后端说明", () => {
    const view = buildPnlView(
      rsp({
        funding_available: true,
        funding_truncated: true,
        funding_from: String(new Date(2026, 9, 1, 8, 30, 15).getTime()),
        funding_note: "时间窗超过 90 天，只统计最近 90 天",
        funding_fees: [{ asset: "USDT", amount: "1" }]
      })
    );
    expect(view.rows[2].amounts).toHaveLength(1);
    expect(view.notice?.level).toBe("warn");
    expect(view.notice?.text).toContain("10-01 08:30:15 起");
    expect(view.notice?.text).toContain("不是所选时间范围的完整合计");
    expect(view.notice?.text).toContain("时间窗超过 90 天，只统计最近 90 天");
  });

  it("截断但没给起点时也要警告", () => {
    const view = buildPnlView(rsp({ funding_available: true, funding_truncated: true }));
    expect(view.notice?.level).toBe("warn");
    expect(view.notice?.text).toContain("只统计了其中一段");
  });

  it("资金费完整但后端附了说明时，作为灰字提示", () => {
    const view = buildPnlView(rsp({ funding_available: true, funding_note: "按 UTC 结算" }));
    expect(view.notice).toEqual({ level: "info", text: "按 UTC 结算" });
  });
});
