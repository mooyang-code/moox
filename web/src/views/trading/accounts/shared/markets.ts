import type { MarketType } from "@/api/trade/types";

const marketNames: Record<number, string> = { 1: "现货", 2: "永续", 3: "股票" };

export function marketLabel(market: number | undefined): string {
  return (market && marketNames[market]) || "-";
}

export function marketsText(markets: readonly number[] | undefined): string {
  return markets?.length ? markets.map(marketLabel).join("、") : "-";
}

/** A 股组合：按结算币判断（人民币结算只有 A 股），不看成员开通的市场——成员全停用时市场并集是空的。 */
export function isAShareLogical(logical: { settlement_asset?: string }): boolean {
  return logical.settlement_asset === "CNY";
}

/** 持仓数的合计说明，按实际开通的市场写：现货合计、股票合计、现货和永续合计；没有市场时为空串。 */
export function marketsTotalText(markets: readonly number[] | undefined): string {
  return markets?.length ? `${markets.map(marketLabel).join("和")}合计` : "";
}

/** 内部标的 ID 带 -SPOT、-SWAP 后缀（如 BTC-USDT-SWAP），股票是原始代码。 */
export function marketOfInstrument(instrumentId: string): MarketType {
  if (instrumentId.endsWith("-SWAP")) return 2;
  if (instrumentId.endsWith("-SPOT")) return 1;
  return 3;
}

/** 标的的展示名：去掉市场后缀，市场另外显示，如 BTC-USDT、永续。 */
export function instrumentName(instrumentId: string): string {
  return instrumentId.replace(/-(SPOT|SWAP)$/, "");
}

/** 币：BTC-USDT-SWAP 的 BTC；股票等没有分隔符的原样返回。 */
export function coinOf(instrumentId: string): string {
  return instrumentName(instrumentId).split("-")[0];
}

/** 只开通了股票市场（A 股组合）。 */
export function isStockOnly(markets: readonly number[]): boolean {
  return markets.length === 1 && markets[0] === 3;
}
