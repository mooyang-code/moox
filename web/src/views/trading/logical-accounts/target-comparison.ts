import type { BlockedTarget, Holding, InstrumentTarget, Position } from "@/api/trade";

export interface ComparisonRow {
  instrument_id: string;
  target: string;
  actual: string;
  difference: string;
  status: string;
  members?: string;
}

// 各成员持仓，键是标的和账户。
export type MemberPositions = Record<string, Record<string, number>>;

export function memberPositionsOf(items: { instrument_id: string; trading_account_id: string; quantity: number }[]) {
  const result: MemberPositions = {};
  for (const item of items) {
    if (!item.instrument_id) continue;
    const current = (result[item.instrument_id] ||= {});
    current[item.trading_account_id] = (current[item.trading_account_id] || 0) + item.quantity;
  }
  return result;
}

export function withMemberDetail(rows: ComparisonRow[], members: MemberPositions, accountName: (id: string) => string) {
  return rows.map(row => ({
    ...row,
    members: Object.entries(members[row.instrument_id] || {})
      .map(([id, quantity]) => `${accountName(id)}：${display(quantity)}`)
      .join("；")
  }));
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

// 被每小时上限顺延的标的：目标虽然定为已收敛，但这一项还有差额没下完，不能显示“已到位”。
function isDeferred(blocked: BlockedTarget[], instrumentId: string) {
  return blocked.some(item => item.instrument_id === instrumentId && item.deferred);
}

function statusOf(target: InstrumentTarget, difference: number, blocked: BlockedTarget[]) {
  if (target.hold) return "保持当前持仓";
  const item = blocked.find(entry => entry.instrument_id === target.instrument_id);
  if (item?.deferred) return `顺延：${item.reason}`;
  if (item?.reason) return `已阻塞：${item.reason}`;
  return difference === 0 ? "已到位" : "收敛中";
}

// 目标里没有、实际还持有的受管标的也要列出来：它们的目标是 0。
// 现货里目标之外的币可能不受管，系统不会动，所以只有合约才列出。
export function buildComparison(
  targets: InstrumentTarget[],
  blocked: BlockedTarget[],
  actual: Record<string, number>,
  options: { converged?: boolean; includeUntargeted?: boolean } = {}
) {
  const listed = new Set(targets.map(item => item.instrument_id));
  const rows = [...targets];
  for (const [instrument_id, quantity] of Object.entries(actual)) {
    if (options.includeUntargeted && !listed.has(instrument_id) && quantity !== 0) rows.push({ instrument_id, quantity: "0" });
  }
  const compared = rows.map<ComparisonRow>(item => {
    const current = actual[item.instrument_id] || 0;
    const difference = item.hold ? 0 : Number(item.quantity) - current;
    return {
      instrument_id: item.instrument_id,
      target: item.hold ? "保持" : item.quantity,
      actual: display(current),
      difference: display(difference),
      // 目标保留 18 位小数，收敛后差额是下不了单的零头，以目标状态为准。
      status:
        options.converged && !item.hold && !isDeferred(blocked, item.instrument_id)
          ? "已到位"
          : statusOf(item, difference, blocked)
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
        status: item.deferred ? `顺延：${item.reason}` : `已阻塞：${item.reason}`
      });
    }
  }
  return compared;
}
