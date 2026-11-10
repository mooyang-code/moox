import type { MarketType } from "@/api/trade/types";

/**
 * 订单、成交的标的筛选：后端按内部标的 ID 精确匹配（BTC-USDT-SWAP），开通多个市场时必须带市场后缀。
 * 这里把用户输入的币（BTC）或交易对（BTC-USDT）补成完整的 ID：缺计价币时补结算资产，市场用所选的。
 * 输入已经带 -SPOT、-SWAP 后缀时原样使用。没有输入返回 undefined。
 * 股票代码（600519.XSHG）是原始标的 ID，不补计价币和后缀：输入带“.”或市场为股票（3）时只做去空格和大写。
 */
export function instrumentFilter(text: string, market: MarketType | undefined, settlementAsset: string): string | undefined {
  const value = text.trim().toUpperCase();
  if (!value) return undefined;
  if (market === 3 || value.includes(".") || /-(SPOT|SWAP)$/.test(value)) return value;
  const pair = value.includes("-") ? value : `${value}-${settlementAsset.toUpperCase()}`;
  if (market === 1) return `${pair}-SPOT`;
  if (market === 2) return `${pair}-SWAP`;
  return pair;
}
