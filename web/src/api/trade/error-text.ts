import type { RetInfo } from "@/api/http";

/**
 * 后端还没改成中文的错误：按错误常量开头对照成中文，只用于展示，不拿来做逻辑判断
 * （逻辑判断看错误码）。后端错误常量改成中文后，这张表里对应的项就删掉。
 * 第三项说明常量前后接的具体原因怎么显示，不写时显示“text（原因）”，没有原因时显示 text：
 *   - "chinese"：这个常量只是外层包装（例如 "trade store: invalid record: 修改组合成员前要先暂停组合"），
 *     原因含中文时只显示原因；原因是英文或没有原因时照常显示“text（原因）”或 text。
 *   - "hide"：只显示 text。原因是给排查用的内部说明（例如冲突时的 stale order version），对用户没有帮助。
 * 常量后面可以接半角冒号或全角冒号“：”。
 */
const tradeMessageTexts: readonly [prefix: string, text: string, detail?: "chinese" | "hide"][] = [
  ["trade: invalid trading account", "执行账户的参数不合法"],
  ["trade: trading account is not executable", "执行账户当前不能下单"],
  ["trade: invalid order", "订单参数不合法"],
  ["trade: invalid order specification", "订单参数不合法"],
  ["trade: invalid operator action", "操作参数不合法"],
  ["trade: invalid decimal", "数值格式不合法"],
  ["trade target: invalid target", "交易目标不合法"],
  ["trade target: target validity window elapsed", "交易目标已过有效期"],
  ["trade store: target expired", "交易目标已过有效期"],
  ["trade Exchange: invalid instrument ID", "标的 ID 不合法"],
  ["trade store: conflict", "数据已经被修改，刷新后再试", "hide"],
  ["trade store: invalid record", "数据不合法", "chinese"]
];

/**
 * 错误常量在消息里的位置：后面必须是结尾或冒号（半角或全角），避免 "trade: invalid order" 命中 "trade: invalid order transition"
 * （空白不算：那样就会命中 transition）。
 */
function constantAt(raw: string, prefix: string): number {
  for (let at = raw.indexOf(prefix); at >= 0; at = raw.indexOf(prefix, at + 1)) {
    const next = raw.charAt(at + prefix.length);
    if (next === "" || next === ":" || next === "：") return at;
  }
  return -1;
}

/** 网关转发到交易服务超时、交易服务内部超时的原文（英文的网络层错误），页面统一显示一句中文。 */
const TIMEOUT_TEXT = /i\/o timeout|context deadline exceeded|deadline_exceeded/i;

/** 含汉字：标为 "chinese" 的常量，原因只有是中文时才单独显示。 */
const HAN = /[一-鿿]/;

/**
 * 交易服务错误的展示文字：已知的英文错误常量换成中文，后端包装进去的具体原因（如 "%w: 具体原因"）按表里的设定接在括号里、
 * 单独显示或不显示；其余原样返回；没有消息时给出错误码。
 */
export function tradeErrorText(retInfo: RetInfo): string {
  const raw = retInfo.msg || "";
  if (!raw) return `交易服务请求失败（错误码 ${retInfo.code ?? "未知"}）`;
  if (TIMEOUT_TEXT.test(raw)) return "交易服务响应超时，请稍后重试";
  for (const [prefix, text, mode] of tradeMessageTexts) {
    const at = constantAt(raw, prefix);
    if (at < 0) continue;
    const before = raw.slice(0, at).replace(/[:：\s]+$/, "");
    const after = raw.slice(at + prefix.length).replace(/^[:：\s]+/, "");
    const detail = [before, after].filter(Boolean).join("；");
    if (!detail || mode === "hide") return text;
    return mode === "chinese" && HAN.test(detail) ? detail : `${text}（${detail}）`;
  }
  return raw;
}
