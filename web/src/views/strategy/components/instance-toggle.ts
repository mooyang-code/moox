import { Message, Modal } from "@arco-design/web-vue";
import { setInstanceEnabled } from "@/api/strategy";

export const TOGGLE_CANCELED_TIP = "已取消，实例状态未改变";

/** 启停确认框的文案：停用强调不清仓，启用说明会固化快照、交易模式会认领账户会话。 */
export function instanceToggleCopy(enable: boolean) {
  return enable
    ? {
        title: "启用实例",
        content: "启用会解析 View、列与因子指纹并固化会话快照，交易模式下还会认领账户会话。确认启用？",
        okText: "确认启用"
      }
    : {
        title: "停用实例",
        content: "停用后不会再计算新目标，也不会自动清仓。确认停用？",
        okText: "确认停用"
      };
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

async function toggleInstance(instanceId: string, enable: boolean, hooks: InstanceToggleHooks) {
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
