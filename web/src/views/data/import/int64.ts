const INT64_MIN = -(2n ** 63n);
const INT64_MAX = 2n ** 63n - 1n;

// parseInt64 把 CSV 单元格解析为十进制整数字符串。
// 不经过 Number：超过 2^53 的合法 INT64 值会被静默改写，必须以字符串提交，后端按 int64 解析。
export function parseInt64(raw: string): string {
  const text = raw.trim();
  if (text === "") return "0";
  if (!/^[+-]?\d+$/.test(text)) throw new Error(`整数格式无效：${raw}`);
  const value = BigInt(text);
  if (value < INT64_MIN || value > INT64_MAX) throw new Error(`整数超出 INT64 范围：${raw}`);
  return value.toString();
}
