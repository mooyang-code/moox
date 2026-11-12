import type { ExtraFieldOption } from "@/api/collector";
import { marketLabel } from "@/views/data/shared/series-market";

// 附加字段来源的中文名：这些提供方只给 K 线补字段，不登记在 Storage 的数据源里，名字写在这里。
const PROVIDER_NAMES: Record<string, string> = {
  sina_hfq: "复权因子（新浪）",
  swsresearch: "申万行业",
  sina_finance: "财务报表（新浪）",
  eastmoney_turnover: "换手率（东方财富）",
  eastmoney_select: "股本与股利（东方财富选股器）",
  binance: "资金费率（币安）"
};

export interface ExtraFieldSourceRow {
  provider_id: string;
  name: string;
  /** 适用的品种，中文名用顿号连接 */
  instruments: string;
  /** 适用的 K 线频率，用顿号连接 */
  frequencies: string;
  fields: Array<{ field_id: string; name: string }>;
}

/**
 * 把接口给出的附加字段按提供方归并成“附加字段来源”的行：只取当前空间的，保持接口给出的顺序。
 * 字段显示名取 Storage 字段目录，目录里没有的显示字段 ID。
 */
export function buildExtraFieldSources(
  options: ReadonlyArray<ExtraFieldOption>,
  spaceId: string,
  fieldNames: ReadonlyMap<string, string>
): ExtraFieldSourceRow[] {
  const space = spaceId.trim().toLowerCase();
  const grouped = new Map<string, { instruments: Set<string>; frequencies: Set<string>; fields: Set<string> }>();
  for (const option of options) {
    if ((option.market_id || "").trim().toLowerCase() !== space || !option.provider_id || !option.field_id) continue;
    const group = grouped.get(option.provider_id) || { instruments: new Set(), frequencies: new Set(), fields: new Set() };
    group.instruments.add(option.instrument_type);
    for (const frequency of option.frequencies || []) group.frequencies.add(frequency);
    group.fields.add(option.field_id);
    grouped.set(option.provider_id, group);
  }
  return [...grouped.entries()].map(([providerId, group]) => ({
    provider_id: providerId,
    name: PROVIDER_NAMES[providerId] || providerId,
    instruments: [...group.instruments].map(item => marketLabel(item)).join("、"),
    frequencies: [...group.frequencies].join("、"),
    fields: [...group.fields].map(id => ({ field_id: id, name: fieldNames.get(id) || id }))
  }));
}
