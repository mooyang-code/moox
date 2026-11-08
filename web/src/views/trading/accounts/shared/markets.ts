import type { MarketType } from "@/api/trade/types";

const marketNames: Record<number, string> = { 1: "现货", 2: "永续", 3: "股票" };

export function marketLabel(market: number | undefined): string {
  return (market && marketNames[market]) || "-";
}

export function marketsText(markets: readonly number[] | undefined): string {
  return markets?.length ? markets.map(marketLabel).join("、") : "-";
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
