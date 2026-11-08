import type { TradableInstrument } from "@/api/trade";

// 小数字符串转成放大后的整数，避免浮点误差；非法输入返回 null。
function scaled(value: string, scale: number): bigint | null {
  const match = /^(\d+)(?:\.(\d+))?$/.exec(value.trim());
  if (!match) return null;
  const fraction = match[2] || "";
  if (fraction.replace(/0+$/, "").length > scale) return null;
  return BigInt(match[1] + fraction.padEnd(scale, "0"));
}

function fractionDigits(...values: string[]) {
  return Math.max(...values.map(value => (value.split(".")[1] || "").length));
}

function multipleOf(value: string, step: string) {
  const digits = fractionDigits(value, step);
  const left = scaled(value, digits);
  const right = scaled(step, digits);
  if (left === null || right === null) return false;
  return right === 0n || left % right === 0n;
}

function lessThan(value: string, minimum: string) {
  const digits = fractionDigits(value, minimum);
  const left = scaled(value, digits);
  const right = scaled(minimum, digits);
  return left !== null && right !== null && left < right;
}

export function instrumentRuleHint(instrument?: TradableInstrument) {
  if (!instrument) return "";
  const notional = Number(instrument.min_notional) > 0 ? `，最小金额 ${instrument.min_notional}` : "";
  return `数量步长 ${instrument.quantity_step}，最小数量 ${instrument.min_quantity}${notional}，价格步长 ${instrument.price_tick}`;
}

// 提交前先按交易规则检查，最终结果仍以服务端校验为准。
// oddLotSell 为真（A 股卖出）时不检查整手：余下不足一手的零股可以一次卖完，是否合规由服务端按持仓判断。
export function orderRuleError(
  quantity: string,
  limitPrice: string | undefined,
  instrument?: TradableInstrument,
  oddLotSell = false
) {
  if (!/^\d+(\.\d+)?$/.test(quantity.trim()) || Number(quantity) <= 0) return "数量必须是正数";
  if (!instrument) return "";
  if (!oddLotSell && !multipleOf(quantity, instrument.quantity_step))
    return `数量必须是步长 ${instrument.quantity_step} 的整数倍`;
  if (!oddLotSell && lessThan(quantity, instrument.min_quantity)) return `数量不能小于 ${instrument.min_quantity}`;
  if (limitPrice === undefined) return "";
  if (!/^\d+(\.\d+)?$/.test(limitPrice.trim()) || Number(limitPrice) <= 0) return "限价必须是正数";
  if (!multipleOf(limitPrice, instrument.price_tick)) return `限价必须是价格步长 ${instrument.price_tick} 的整数倍`;
  return "";
}
