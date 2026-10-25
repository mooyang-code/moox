import type { FactorParamDecl } from "@/api/factor/types";

/** 成员别名的最大长度（与服务端 domain.MaxAliasLength 一致）。 */
export const MAX_ALIAS_LENGTH = 32;

/**
 * 参数值写进别名的片段：十进制（不出现指数）、整数不带小数点、-0 按 0；小数点换成 p、负号换成 n（0.5 → 0p5，-1 → n1）。
 * 与服务端 domain.FormatParamValue 一致，两边共用 modules/factor/testdata/alias_vectors.json 的测试向量。
 */
export function formatParamValue(value: number): string {
  const normalized = Object.is(value, -0) || Number.isNaN(value) ? 0 : value;
  // String() 给出最短的往返表示，与 Go 的 FormatFloat(v, 'f', -1, 64) 的数字一致；只有指数形式需要展开成十进制。
  let text = String(normalized);
  if (/e/i.test(text)) text = expandExponent(text);
  return text.replace(/\./g, "p").replace(/-/g, "n");
}

/** 把 1e-7、1.5e+21 这样的指数形式展开成十进制文本。 */
function expandExponent(text: string): string {
  const match = /^(-?)(\d+)(?:\.(\d+))?e([+-]?\d+)$/i.exec(text);
  if (!match) return text;
  const [, sign, integer, fraction = "", exponent] = match;
  const digits = integer + fraction;
  const point = integer.length + Number(exponent);
  let body: string;
  if (point <= 0) body = `0.${"0".repeat(-point)}${digits}`;
  else if (point >= digits.length) body = digits + "0".repeat(point - digits.length);
  else body = `${digits.slice(0, point)}.${digits.slice(point)}`;
  return sign + body;
}

/** 用定义声明的默认值把给出的参数补全成完整参数。 */
export function completeMemberParams(decls: Pick<FactorParamDecl, "name" | "default_value">[], given: Record<string, number> = {}) {
  const out: Record<string, number> = {};
  for (const decl of decls) out[decl.name] = decl.name in given ? given[decl.name] : decl.default_value;
  return out;
}

export interface DerivedAlias {
  alias: string;
  /** 按参数生成的别名超过长度上限，需要手动指定。 */
  tooLong: boolean;
}

/**
 * 按参数生成成员别名：全部参数等于默认值（或定义没有参数）时沿用因子 ID，否则是“因子ID_参数1_参数2…”，
 * 参数值按定义声明的顺序全部写出。与服务端 domain.DeriveMemberAlias 一致。
 */
export function deriveMemberAlias(
  factorId: string,
  decls: Pick<FactorParamDecl, "name" | "default_value">[],
  params: Record<string, number> = {}
): DerivedAlias {
  const full = completeMemberParams(decls, params);
  if (decls.every(decl => full[decl.name] === decl.default_value)) return { alias: factorId, tooLong: false };
  const alias = `${factorId}_${decls.map(decl => formatParamValue(full[decl.name])).join("_")}`;
  return { alias, tooLong: alias.length > MAX_ALIAS_LENGTH };
}

/** 成员固定参数的展示文本（按参数名排序，如 span=20, warmup=50000）；没有固定参数时为空。 */
export function fixedParamsText(params?: Record<string, number>): string {
  if (!params) return "";
  return Object.keys(params)
    .sort()
    .map(name => `${name}=${params[name]}`)
    .join(", ");
}
