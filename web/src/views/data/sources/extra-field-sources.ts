import type { ExtraFieldOption } from "@/api/collector";
import { marketLabel } from "@/views/data/shared/series-market";

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
 * 提供方名称和字段显示名均直接取自接口（Collector 的 ProviderDescriptor 与字段规格），缺省时回落到 ID。
 */
export function buildExtraFieldSources(options: ReadonlyArray<ExtraFieldOption>, spaceId: string): ExtraFieldSourceRow[] {
  const space = spaceId.trim().toLowerCase();
  const grouped = new Map<
    string,
    { name: string; instruments: Set<string>; frequencies: Set<string>; fields: Map<string, string> }
  >();
  for (const option of options) {
    if ((option.market_id || "").trim().toLowerCase() !== space || !option.provider_id || !option.field_id) continue;
    const group = grouped.get(option.provider_id) || {
      name: "",
      instruments: new Set(),
      frequencies: new Set(),
      fields: new Map()
    };
    const providerName = (option.provider_name || "").trim();
    if (!group.name && providerName) group.name = providerName;
    group.instruments.add(option.instrument_type);
    for (const frequency of option.frequencies || []) group.frequencies.add(frequency);
    group.fields.set(option.field_id, option.display_name?.trim() || option.field_id);
    grouped.set(option.provider_id, group);
  }
  return [...grouped.entries()].map(([providerId, group]) => ({
    provider_id: providerId,
    name: group.name || providerId,
    instruments: [...group.instruments].map(item => marketLabel(item)).join("、"),
    frequencies: [...group.frequencies].join("、"),
    fields: [...group.fields].map(([field_id, name]) => ({ field_id, name }))
  }));
}
