import type { BlockedTarget, Holding, InstrumentTarget, Position } from "@/api/trade";

export interface ComparisonRow {
  instrument_id: string;
  target: string;
  actual: string;
  difference: string;
  status: string;
}

export function actualFromPositions(positions: Position[]) {
  const actual: Record<string, number> = {};
  for (const item of positions) actual[item.instrument_id] = (actual[item.instrument_id] || 0) + Number(item.signed_quantity);
  return actual;
}

export function actualFromHoldings(holdings: Holding[]) {
  const actual: Record<string, number> = {};
  for (const item of holdings) {
    if (item.instrument_id) actual[item.instrument_id] = (actual[item.instrument_id] || 0) + Number(item.quantity);
  }
  return actual;
}

// 小数相减的浮点误差只影响显示，这里保留 8 位有效数字。
function display(value: number) {
  return String(Number(value.toPrecision(8)));
}

function statusOf(target: InstrumentTarget, difference: number, blocked: BlockedTarget[]) {
  if (target.hold) return "保持当前持仓";
  const reason = blocked.find(item => item.instrument_id === target.instrument_id)?.reason;
  if (reason) return `已阻塞：${reason}`;
  return difference === 0 ? "已到位" : "收敛中";
}

// 目标里没有、实际还持有的受管标的也要列出来：它们的目标是 0。
export function buildComparison(targets: InstrumentTarget[], blocked: BlockedTarget[], actual: Record<string, number>) {
  const listed = new Set(targets.map(item => item.instrument_id));
  const rows = [...targets];
  for (const [instrument_id, quantity] of Object.entries(actual)) {
    if (!listed.has(instrument_id) && quantity !== 0) rows.push({ instrument_id, quantity: "0" });
  }
  const compared = rows.map<ComparisonRow>(item => {
    const current = actual[item.instrument_id] || 0;
    const difference = item.hold ? 0 : Number(item.quantity) - current;
    return {
      instrument_id: item.instrument_id,
      target: item.hold ? "保持" : item.quantity,
      actual: display(current),
      difference: display(difference),
      status: statusOf(item, difference, blocked)
    };
  });
  // 无法映射到标的的敞口只出现在阻塞列表里。
  for (const item of blocked) {
    if (!compared.some(row => row.instrument_id === item.instrument_id)) {
      compared.push({
        instrument_id: item.instrument_id,
        target: "-",
        actual: "-",
        difference: item.quantity,
        status: `已阻塞：${item.reason}`
      });
    }
  }
  return compared;
}
