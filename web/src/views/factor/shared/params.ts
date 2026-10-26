import type { FactorCategory, FactorParamDecl, ParamValue } from "@/api/factor/types";
import { UNCATEGORIZED } from "@/api/factor/types";

/** 一个可调参数的一行说明：名称、类型、范围与默认值。 */
export function describeParam(param: FactorParamDecl) {
  // ProtoJSON 省略值为 0 的数值字段：缺省按 0 展示（例如 min: 0、default: 0）。
  const { min = 0, max = 0, default_value: defaultValue = 0 } = param;
  return `${param.name}（${param.type === "int" ? "整数" : "小数"}，${min} ~ ${max}，默认 ${defaultValue}）`;
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

/** 一个参数取值的文本：统一值，有按标的覆盖时附上覆盖个数。 */
export function paramValueText(value: ParamValue) {
  const overrides = Object.keys(value.overrides ?? {}).length;
  const defaultValue = value.default_value ?? 0; // 值为 0 时 ProtoJSON 省略该字段
  return overrides ? `${defaultValue}（${overrides} 个标的覆盖）` : String(defaultValue);
}

/** 成员参数的展示文本（按参数名排序，如 span=20, warmup=50000）；没有参数时为空。 */
export function paramsText(params?: Record<string, number>): string {
  if (!params) return "";
  return Object.keys(params)
    .sort()
    .map(name => `${name}=${params[name]}`)
    .join(", ");
}
