import { describe, expect, it } from "vitest";
import { actualFromHoldings, buildComparison } from "./target-comparison";
import type { Holding } from "@/api/trade";

describe("目标与实际持仓对照", () => {
  it("计算差额并标出状态", () => {
    const rows = buildComparison(
      [
        { instrument_id: "BTC-USDT-SWAP", quantity: "1" },
        { instrument_id: "ETH-USDT-SWAP", quantity: "2" },
        { instrument_id: "SOL-USDT-SWAP", quantity: "0", hold: true }
      ],
      [{ instrument_id: "ETH-USDT-SWAP", quantity: "2", reason: "余额不足" }],
      { "BTC-USDT-SWAP": 0.4, "SOL-USDT-SWAP": 3 }
    );
    expect(rows[0]).toMatchObject({ difference: "0.6", status: "收敛中" });
    expect(rows[1].status).toBe("已阻塞：余额不足");
    expect(rows[2]).toMatchObject({ target: "保持", status: "保持当前持仓" });
  });

  it("被每小时上限顺延的标的显示“顺延”，即使目标已定为已收敛也不显示已到位", () => {
    const deferred = [
      { instrument_id: "BTC-USDT-SWAP", quantity: "1", reason: "单币每小时上限已用完，顺延到下一个整点继续", deferred: true }
    ];
    const [row] = buildComparison([{ instrument_id: "BTC-USDT-SWAP", quantity: "1" }], deferred, {}, { converged: true });
    expect(row.status).toBe("顺延：单币每小时上限已用完，顺延到下一个整点继续");
    expect(row.status).not.toContain("阻塞");
    // 无法映射到标的、只出现在列表里的顺延项同样不算阻塞。
    const [orphan] = buildComparison([], deferred, {});
    expect(orphan.status.startsWith("顺延：")).toBe(true);
  });

  it("列出目标之外仍持有的标的", () => {
    const rows = buildComparison([], [], { "BTC-USDT-SWAP": 1 }, { includeUntargeted: true });
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ target: "0", difference: "-1" });
  });

  it("现货目标之外的币不列出，收敛后零头算已到位", () => {
    expect(buildComparison([], [], { "BNB-USDT-SPOT": 1 })).toHaveLength(0);
    const [row] = buildComparison(
      [{ instrument_id: "BTC-USDT-SWAP", quantity: "1.0004" }],
      [],
      { "BTC-USDT-SWAP": 1 },
      { converged: true }
    );
    expect(row.status).toBe("已到位");
  });

  it("没有标的的资产不参与对照", () => {
    const holdings = [
      { instrument_id: "", quantity: "9", asset: "USDT" },
      { instrument_id: "BTC-USDT-SPOT", quantity: "1", asset: "BTC" }
    ] as Holding[];
    expect(actualFromHoldings(holdings)).toEqual({ "BTC-USDT-SPOT": 1 });
  });
});
