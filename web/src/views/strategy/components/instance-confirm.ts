import type { ShadowTarget } from "@/api/trade/types";
import { percent } from "@/views/strategy/model";

/** 确认实盘对话框的说明文案。 */
export const CONFIRM_COPY = {
  effect: "确认后，下一期起策略目标交给组合账户按目标下单。停用再启用会回到试运行，要重新确认。",
  noTargets:
    "本次启用还没有影子目标（还没到第一期收盘，或目标被交易模块拒绝，拒绝原因见事件中心）。确认后第一期就直接按目标下单。",
  canceled: "已取消，实例仍在试运行",
  done: "已确认实盘，下一期起按目标下单"
};

export interface ConfirmHooks {
  /** 确认成功、对话框关闭之后；或失败过一次之后取消（调用方据此重读实例）。 */
  onSettled?: (ok: boolean) => void;
}

/** 只留本次启用（当前会话）的影子目标：旧会话的是上一次试运行留下的，不能拿来判断这一次。 */
export function currentSessionTargets(targets: readonly ShadowTarget[], sessionId: string): ShadowTarget[] {
  return targets.filter(target => target.session_id === sessionId);
}

/**
 * 一条影子目标写成一行：标的、目标权重和换算出的数量，保持的标的写“保持”，空目标写“清仓”。
 * 例：BTC-USDT 60.00%（0.01）；ETH-USDT 保持
 */
export function shadowTargetSummary(target: ShadowTarget): string {
  const weights = target.weights || [];
  if (!weights.length) return "清仓（目标为空）";
  const quantities = new Map((target.targets || []).map(item => [item.instrument_id, item]));
  return weights
    .map(weight => {
      if (weight.hold) return `${weight.instrument_id} 保持`;
      const quantity = quantities.get(weight.instrument_id)?.quantity;
      return `${weight.instrument_id} ${percent(weight.target_weight)}${quantity ? `（${quantity}）` : ""}`;
    })
    .join("；");
}
