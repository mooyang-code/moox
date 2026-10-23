import type { FactorCategory, FactorParamDecl, ParamInterval, ParamValue, ParamsSource } from "@/api/factor/types";
import { UNCATEGORIZED } from "@/api/factor/types";

/** 一个可调参数的一行说明：名称、类型、范围与默认值。 */
export function describeParam(param: FactorParamDecl) {
  return `${param.name}（${param.type === "int" ? "整数" : "小数"}，${param.min} ~ ${param.max}，默认 ${param.default_value}）`;
}

/** 没有可调参数时返回“无”；否则按声明顺序用分号连接。 */
export function describeParams(params?: FactorParamDecl[]) {
  return params?.length ? params.map(describeParam).join("；") : "无（回看与取值固定）";
}

/** 分类的展示名；分类被删除或没有记录时回落为“未分类”。 */
export function categoryName(categories: FactorCategory[], categoryId?: string) {
  const id = categoryId || UNCATEGORIZED;
  return categories.find(category => category.category_id === id)?.name ?? (id === UNCATEGORIZED ? "未分类" : id);
}

export const PARAMS_SOURCE_TEXT: Record<ParamsSource, string> = {
  requested: "提交生效",
  carried: "沿用上期",
  carried_offline: "离线沿用",
  default: "默认值"
};

export function paramsSourceText(source: string) {
  return PARAMS_SOURCE_TEXT[source as ParamsSource] ?? source;
}

/** 一个参数取值的文本：统一值，有按标的覆盖时附上覆盖个数。 */
export function paramValueText(value: ParamValue) {
  const overrides = Object.keys(value.overrides ?? {}).length;
  return overrides ? `${value.default_value}（${overrides} 个标的覆盖）` : String(value.default_value);
}

export interface ParamTimelineRow {
  key: string;
  member: string;
  start: number;
  end: number;
  periods: number;
  source: string;
  sourceText: string;
  params: string;
}

/** 把 int64（ProtoJSON 输出字符串）转成数字。 */
const toNumber = (value: number | string) => Number(value);

/**
 * 冻结区间转成时间线行（新的在前）：每行是快照与来源相同的连续期次。periodSeconds 用来估算区间覆盖的期数。
 */
export function paramTimeline(intervals: ParamInterval[], periodSeconds: number): ParamTimelineRow[] {
  return intervals
    .map(interval => {
      const start = toNumber(interval.start_period);
      const end = toNumber(interval.end_period);
      return {
        key: `${interval.member_alias}@${start}`,
        member: interval.member_alias,
        start,
        end,
        periods: periodSeconds > 0 ? Math.floor((end - start) / periodSeconds) + 1 : 1,
        source: interval.source,
        sourceText: paramsSourceText(interval.source),
        params: Object.entries(interval.params ?? {})
          .sort(([a], [b]) => a.localeCompare(b))
          .map(([name, value]) => `${name}=${paramValueText(value)}`)
          .join("，")
      };
    })
    .sort((a, b) => b.start - a.start || a.member.localeCompare(b.member));
}
