/**
 * 测试网、生产环境下会作用到交易所账户的设置类操作：确认框的标题和正文、要不要输入名称。
 * 模拟盘不走这里，保持一键。下单、撤单、清仓这类交易动作另有确认，不在这里。
 */
export interface RiskyConfirm {
  title: string;
  lines: string[];
  /** 生产环境是否要输入名称才能确认；朝安全方向或不会交易的操作不需要 */
  nameRequired: boolean;
  danger: boolean;
  confirmText: string;
}

/** 账户默认杠杆的键：设置它影响全部标的。 */
export const DEFAULT_LEVERAGE_KEY = "*";

/**
 * 接管已有持仓后会发生什么（和交易模块的目标执行一致）：只有策略驱动的组合会按目标收敛；
 * 永续按账户里的全部仓位收敛，不在目标里的按 0 平掉；现货只调整受管资产，也就是目标里的币和 MooX 成交过的币。
 */
export const ADOPT_EFFECT_TEXT =
  "策略驱动的组合里，永续：账户里的仓位都交给组合管理，不在策略目标里的会被平掉；现货：策略目标里的币和 MooX 成交过的币按目标调整，其余现货不动。";

/**
 * 杠杆标的的写法和后端一致：两段写法（BTC-USDT）补成永续的三段写法（BTC-USDT-SWAP），
 * 这样确认框查到的当前倍数和真正要改的是同一个标的。
 */
export function normalizeLeverageInstrument(raw: string): string {
  const value = raw.trim().toUpperCase();
  if (value === DEFAULT_LEVERAGE_KEY) return value;
  const parts = value.split("-");
  return parts.length === 2 && parts[0] && parts[1] ? `${value}-SWAP` : value;
}

/** 杠杆倍数只接受正整数；返回错误文案，合法时返回空串。 */
export function leverageError(value: string): string {
  const text = value.trim();
  if (!text) return "请填写倍数";
  return /^[1-9]\d*$/.test(text) ? "" : "倍数必须是正整数";
}

/** 设置杠杆：后端只写库，下次开仓前才按新倍数设置到交易所。 */
export function leverageConfirm(input: { instrument: string; current?: string; next: string }): RiskyConfirm {
  const isDefault = input.instrument === DEFAULT_LEVERAGE_KEY;
  const target = isDefault ? "账户默认杠杆" : input.instrument;
  const lines = [`${target}：${input.current ? `${input.current}×` : "未设置"} → ${input.next}×`];
  if (isDefault) lines.push("默认杠杆影响全部标的。");
  lines.push("保存后，下次开仓前按新倍数设置到交易所。");
  return { title: `设置杠杆：${target}`, lines, nameRequired: true, danger: false, confirmText: "设置" };
}

/** 更换交易所密钥：之后改用新密钥连接交易所。 */
export function secretChangeConfirm(input: { accountName: string; from: string; to: string }): RiskyConfirm {
  return {
    title: `更换「${input.accountName}」的交易所密钥`,
    lines: [
      `交易所密钥：${input.from || "未设置"} → ${input.to}`,
      "保存后改用新密钥连接交易所。新密钥属于另一个交易所账号时，系统记录的持仓和订单会和交易所对不上。"
    ],
    nameRequired: true,
    danger: true,
    confirmText: "更换"
  };
}

/** 未加入组合的执行账户启用、停用：都不会下单，生产环境也不用输入名称。 */
export function accountStatusConfirm(input: { accountName: string; enable: boolean }): RiskyConfirm {
  return input.enable
    ? {
        title: `启用执行账户「${input.accountName}」`,
        lines: ["启用后开始同步余额和持仓；加入组合前不会下单。"],
        nameRequired: false,
        danger: false,
        confirmText: "启用"
      }
    : {
        title: `停用执行账户「${input.accountName}」`,
        lines: ["停用后不再同步余额和持仓，组合也不能用它。"],
        nameRequired: false,
        danger: true,
        confirmText: "停用"
      };
}

export interface MemberSettings {
  enabled: boolean;
  priority: number;
}

/**
 * 加入组合或修改组合成员时要确认的内容；没有需要确认的改动时返回 null。
 * 接管已有持仓会让策略调整账户里已有的仓位，生产环境要输入组合名称；只改启停、优先级不用。
 */
export function memberChangeConfirm(input: {
  accountName: string;
  comboName: string;
  /** 新增成员时为空 */
  original: MemberSettings | null;
  next: MemberSettings;
  adopt: boolean;
}): RiskyConfirm | null {
  const lines: string[] = [];
  if (input.original) {
    if (input.original.enabled !== input.next.enabled) {
      lines.push(`启用：${input.original.enabled ? "是" : "否"} → ${input.next.enabled ? "是" : "否"}`);
      lines.push(input.next.enabled ? "启用后策略会用它下单。" : "停用后策略不再用它下单。");
    }
    if (input.original.priority !== input.next.priority) {
      lines.push(`优先级：${input.original.priority} → ${input.next.priority}（数字越小越先使用）`);
    }
  }
  if (input.adopt) {
    lines.push(`把「${input.accountName}」里已有的持仓交给组合「${input.comboName}」管理。`);
    lines.push(ADOPT_EFFECT_TEXT);
  }
  if (!lines.length) return null;
  const disabling = Boolean(input.original?.enabled && !input.next.enabled);
  return {
    title: input.adopt ? `接管「${input.accountName}」已有的持仓` : `修改「${input.accountName}」在组合里的设置`,
    lines,
    nameRequired: input.adopt,
    danger: input.adopt || disabling,
    confirmText: input.adopt ? "确认接管" : "保存"
  };
}
