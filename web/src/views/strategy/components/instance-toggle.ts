import { Message, Modal } from "@arco-design/web-vue";
import { setInstanceEnabled } from "@/api/strategy";

export const TOGGLE_CANCELED_TIP = "已取消，实例状态未改变";

/** 启用确认框里关于规则状态的提示：停用再启用会沿用，人工平过仓的要先清空（做法见 docs/模块/策略.md“想从头开始”）。 */
export const ENABLE_STATE_TIP =
  "DSL、绑定和成员都没变时，会沿用停用前的持有记录、止损入场价和冷却；人工平过仓或想从头开始，先清空规则状态再启用：用 moox-cli strategy instance update 原样提交一次，或改一下策略 DSL（详见策略文档“想从头开始”）。";

/**
 * 启停确认框的文案：停用强调不清仓；启用说明会固化快照，写了 factor_params 的实例会永久关联主帧任务。
 * 绑了组合账户的实例启用时不走这里，改用 instance-enable-dialog.vue（要写明账户和环境，生产要输入组合名称）。
 */
export function instanceToggleCopy(enable: boolean) {
  return enable
    ? {
        title: "启用实例",
        content: `启用会解析 View、列与因子指纹并固化会话快照；写了 factor_params 的实例会把主帧计算任务与本实例永久关联（已关联的不变），其余实例只读任务的列。${ENABLE_STATE_TIP}确认启用？`,
        okText: "确认启用"
      }
    : {
        title: "停用实例",
        content: "停用后不会再计算新目标，也不会自动清仓。确认停用？",
        okText: "确认停用"
      };
}

/** 启用绑了组合账户的实例时，确认框要用到的实例字段。 */
export interface EnableTarget {
  instance_id: string;
  logical_account_id: string;
  factor_tasks?: Record<string, string>;
}

export interface InstanceToggleHooks {
  /** 用户确认、请求发出前。 */
  onStart?: () => void;
  /** 请求失败（结果未知，调用方应重新读取实例状态）。 */
  onFailed?: (error: unknown) => void;
  /** 请求结束（成功或失败）后。 */
  onSettled?: (ok: boolean) => void;
}

/**
 * 启停实例的统一入口：显式确认对话框 + setInstanceEnabled。
 * 确认、取消（含点遮罩、按 Esc、点关闭）、成功、失败四种结果都给出可见提示，避免“点了没反应”。
 */
export function confirmInstanceToggle(instanceId: string, enable: boolean, hooks: InstanceToggleHooks = {}) {
  const copy = instanceToggleCopy(enable);
  Modal.confirm({
    title: `${copy.title}「${instanceId}」`,
    content: copy.content,
    okText: copy.okText,
    cancelText: "取消",
    okButtonProps: enable ? { status: "success" } : { status: "warning" },
    onOk: () => void toggleInstance(instanceId, enable, hooks),
    onCancel: () => Message.info(TOGGLE_CANCELED_TIP)
  });
}

/** 发启停请求并给出可见提示；确认已经由调用方做过。 */
export async function toggleInstance(instanceId: string, enable: boolean, hooks: InstanceToggleHooks = {}) {
  hooks.onStart?.();
  const pending = Message.loading({ content: enable ? "正在启用实例…" : "正在停用实例…", duration: 0 });
  let ok = false;
  try {
    await setInstanceEnabled(instanceId, enable);
    ok = true;
    Message.success(enable ? "实例已启用" : "实例已停用");
  } catch (error) {
    Message.error(`${error instanceof Error && error.message ? error.message : "控制请求失败"}（正在重新读取实例状态）`);
    hooks.onFailed?.(error);
  } finally {
    pending?.close?.();
    hooks.onSettled?.(ok);
  }
}
