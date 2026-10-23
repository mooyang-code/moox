// 规范频率，与 packages/frequency 保持一致：由语法定义，而不是一张枚举。
// 日内周期写成 "<N>m" 或 "<N>h" 且必须能整除一天；"30s"、"1d"、"1w"、"1mo" 是固定写法。
// 同一个周期只有一种规范写法（60m → 1h、1440m → 1d），别名只在输入边界归一。

/** 标准档：下拉候选与快捷项；其他整除一天的周期（如 45m、2h）同样合法。 */
export const STANDARD_FREQUENCIES = ["30s", "1m", "5m", "15m", "30m", "1h", "4h", "1d", "1w", "1mo"] as const;

export type StandardFrequency = (typeof STANDARD_FREQUENCIES)[number];

const MINUTES_PER_DAY = 24 * 60;

// "1M" 是月（交易所惯例），"1m" 是分钟；45M 这类大写 M 不是合法写法。
const FIXED_ALIASES: Record<string, string> = {
  "30S": "30s",
  "1D": "1d",
  "1W": "1w",
  "1M": "1mo",
  "1MO": "1mo",
  "1Mo": "1mo"
};

function fromMinutes(minutes: number): string {
  if (minutes === MINUTES_PER_DAY) return "1d";
  if (minutes % 60 === 0) return `${minutes / 60}h`;
  return `${minutes}m`;
}

/** 解析日内写法 "<N>m" / "<N>h" / "<N>H"，返回规范值；不是整除一天的合法写法时返回 undefined。 */
function parseIntraday(value: string): string | undefined {
  const match = /^([1-9][0-9]{0,4})(m|h|H)$/.exec(value);
  if (!match) return undefined;
  const number = Number(match[1]);
  const minutes = match[2] === "m" ? number : number * 60;
  if (minutes <= 0 || minutes > MINUTES_PER_DAY || MINUTES_PER_DAY % minutes !== 0) return undefined;
  return fromMinutes(minutes);
}

/** 返回 value 的规范频率；不是合法频率时返回 undefined。 */
export function parseFrequency(value: string): string | undefined {
  const trimmed = value.trim();
  if (FIXED_ALIASES[trimmed]) return FIXED_ALIASES[trimmed];
  if (trimmed === "30s" || trimmed === "1d" || trimmed === "1w" || trimmed === "1mo") return trimmed;
  return parseIntraday(trimmed);
}

/** value 已经是合法频率的唯一规范写法。 */
export function isCanonicalFrequency(value: string): boolean {
  return parseFrequency(value) === value;
}

/** 返回 value 的规范频率；不是合法频率时返回去掉空白的原值，交给校验去拒绝。 */
export function normalizeFrequency(value: string): string {
  const trimmed = value.trim();
  return parseFrequency(trimmed) ?? trimmed;
}

/** 规范频率的时长（分钟）；月线、周线、秒级与非法值返回 0。 */
export function frequencyMinutes(value: string): number {
  const canonical = parseFrequency(value);
  if (!canonical || canonical === "30s" || canonical === "1w" || canonical === "1mo") return 0;
  if (canonical === "1d") return MINUTES_PER_DAY;
  const number = Number(canonical.slice(0, -1));
  return canonical.endsWith("h") ? number * 60 : number;
}
