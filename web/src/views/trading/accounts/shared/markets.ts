import type { MarketType } from "@/api/trade/types";

/** 市场的固定顺序：现货、永续、股票（与 Trade 存市场集合的顺序一致）。 */
export const MARKET_ORDER: readonly MarketType[] = ["spot", "swap", "equity"];

/** 交易页市场的中文名，按接口里的小写取值取；交易页只从这里取。 */
const marketNames: Record<string, string> = { spot: "现货", swap: "永续", equity: "股票" };

export function marketLabel(market: string | undefined): string {
  return (market && marketNames[market]) || "-";
}

/** 按固定顺序排列市场集合。 */
export function sortMarkets(markets: readonly MarketType[]): MarketType[] {
  return [...markets].sort((a, b) => MARKET_ORDER.indexOf(a) - MARKET_ORDER.indexOf(b));
}

export function marketsText(markets: readonly string[] | undefined): string {
  return markets?.length ? markets.map(marketLabel).join("、") : "-";
}

/** 股票组合（A 股、港股）：按结算币判断（人民币结算只有 A 股，港元结算只有港股），不看成员开通的市场——成员全停用时市场并集是空的。 */
export function isStockLogical(logical: { settlement_asset?: string }): boolean {
  return logical.settlement_asset === "CNY" || logical.settlement_asset === "HKD";
}

/** 持仓数的合计说明，按实际开通的市场写：现货合计、股票合计、现货和永续合计；没有市场时为空串。 */
export function marketsTotalText(markets: readonly string[] | undefined): string {
  return markets?.length ? `${markets.map(marketLabel).join("和")}合计` : "";
}

/** 内部标的 ID 带 -SPOT、-SWAP 后缀（如 BTC-USDT-SWAP），股票是原始代码。 */
export function marketOfInstrument(instrumentId: string): MarketType {
  if (instrumentId.endsWith("-SWAP")) return "swap";
  if (instrumentId.endsWith("-SPOT")) return "spot";
  return "equity";
}

/** 标的的展示名：去掉市场后缀，市场另外显示，如 BTC-USDT、永续。 */
export function instrumentName(instrumentId: string): string {
  return instrumentId.replace(/-(SPOT|SWAP)$/, "");
}

/** 币：BTC-USDT-SWAP 的 BTC；股票等没有分隔符的原样返回。 */
export function coinOf(instrumentId: string): string {
  return instrumentName(instrumentId).split("-")[0];
}

/** 只开通了股票市场（A 股、港股组合）。 */
export function isStockOnly(markets: readonly string[]): boolean {
  return markets.length === 1 && markets[0] === "equity";
}
