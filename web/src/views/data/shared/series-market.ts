// 序列所属的市场：与后端 packages/seriesset 的 SeriesTagMarket 同一规则。
// 采集写行时生成的 series_tag 形如 `venue:binance|market:swap|source:swap_http`，按 `|` 分段，取 `market:` 段的值（小写）；
// 没有这一段（例如 `venue:binance`、`default`）的视为现货。

const MARKET_SEGMENT = "market:";

/** series_tag 里显式写出的市场；没有 `market:` 段时为 null。 */
export function seriesTagExplicitMarket(tag: string | undefined | null): string | null {
  for (const raw of (tag ?? "").split("|")) {
    const segment = raw.trim();
    if (segment.length >= MARKET_SEGMENT.length && segment.slice(0, MARKET_SEGMENT.length).toLowerCase() === MARKET_SEGMENT) {
      const value = segment.slice(MARKET_SEGMENT.length).trim().toLowerCase();
      if (value) return value;
    }
  }
  return null;
}

export function seriesTagMarket(tag: string | undefined | null): string {
  return seriesTagExplicitMarket(tag) ?? "spot";
}

const MARKET_LABELS: Record<string, string> = {
  spot: "现货",
  swap: "永续",
  equity: "股票"
};

/** 市场的中文名；不认识的市场原样返回。 */
export function marketLabel(market: string | undefined | null): string {
  const value = (market ?? "").trim().toLowerCase();
  return MARKET_LABELS[value] || value || "-";
}

/**
 * 序列标签列的展示文字：tag 里写了市场的显示成“现货”“永续”（原始 tag 放在悬浮提示里）；
 * 没写市场的（例如 A 股的 default、空标签）原样显示，不能硬说成现货。
 */
export function seriesTagDisplay(tag: string | undefined | null): string {
  const market = seriesTagExplicitMarket(tag);
  if (market) return marketLabel(market);
  return (tag ?? "").trim() || "-";
}

/** 统一数据集：同一个数据集里同时放现货、永续两条序列，数据集的 market_type 是 multi。 */
export const UNIFIED_MARKET_TYPE = "multi";

export function isUnifiedMarketType(marketType: string | undefined | null): boolean {
  return (marketType ?? "").trim().toLowerCase() === UNIFIED_MARKET_TYPE;
}

/** 数据集市场类型的展示文字：统一数据集写成“统一数据集（现货、永续）”。 */
export function datasetMarketText(marketType: string | undefined | null): string {
  if (isUnifiedMarketType(marketType)) return "统一数据集（现货、永续）";
  return marketLabel(marketType);
}
