import { h } from "vue";
import { Message, Modal } from "@arco-design/web-vue";
import { getFactorTask } from "@/api/factor";
import type { FactorTask } from "@/api/factor/types";
import { ControlRequestError } from "@/api/admin/http";
import { deleteInstance } from "@/api/strategy";
import type { StrategyInstance } from "@/api/strategy-types";
import { instanceTasks, marketLabel } from "@/views/strategy/market-mode";

export const DELETE_INCOMPLETE_TIP = "删除未完成，请稍后重试";

/** 只有已停用、没有运行会话的实例能删除。 */
export function canDeleteInstance(instance: Pick<StrategyInstance, "enabled" | "session_id">): boolean {
  return !instance.enabled && !instance.session_id;
}

/** 删除确认框里的一个因子任务：factorTask 为空表示因子服务里已经没有这个任务（上次删除时已删掉）。 */
export interface DeleteTaskLine {
  market: string;
  taskId: string;
  factorTask?: FactorTask;
}

/** 任务是否随实例删除：绑定到此实例的，以及因子服务里已经没有的（上次删除时已删掉）；其余（没绑定、绑定到别的实例）保留。 */
export function deletedWithInstance(instanceId: string, task: DeleteTaskLine): boolean {
  return !task.factorTask || task.factorTask.bound_instance_id === instanceId;
}

/** 确认框正文：分两组列出将删除的因子任务与数据集、保留的因子任务（写明原因），最后写明结果数据不可恢复。 */
export function instanceDeleteLines(instanceId: string, tasks: DeleteTaskLine[]): string[] {
  const removed = tasks.filter(task => deletedWithInstance(instanceId, task));
  const kept = tasks.filter(task => !deletedWithInstance(instanceId, task));
  const lines = ["将删除的因子任务和数据集："];
  if (!removed.length) lines.push("无");
  for (const task of removed) {
    const market = marketLabel(task.market);
    if (!task.factorTask) {
      lines.push(`${market}：${task.taskId}（已删除）`);
      continue;
    }
    const name = task.factorTask.name ? `「${task.factorTask.name}」` : "";
    lines.push(`${market}：因子任务 ${task.taskId}${name}，结果数据集 ${task.factorTask.result_dataset_id || "-"}`);
  }
  if (kept.length) {
    lines.push("保留的因子任务（只去掉实例对它的引用）：");
    for (const task of kept) lines.push(`${marketLabel(task.market)}：因子任务 ${task.taskId}，未绑定到此实例`);
  }
  lines.push("结果数据不可恢复。交易模块里的历史目标和订单保留。");
  return lines;
}

/**
 * 删除失败时是否要提示“删除未完成，请稍后重试”：服务端答复了的，按答复里的 incomplete（删除已经开始、没做完）判断，
 * 什么都还没删的失败（例如读不到归档任务、被拒绝）原样显示；没拿到答复的（超时、连接中断、框架错误）删除可能已经做了一部分。
 */
export function isDeleteIncomplete(error: unknown): boolean {
  if (error instanceof ControlRequestError && error.response?.ret_info) {
    return Boolean((error.response as { incomplete?: boolean }).incomplete);
  }
  return true;
}

function isNotFound(error: unknown): boolean {
  const code = error instanceof ControlRequestError ? error.response?.ret_info?.code : undefined;
  return code === 5 || code === "5" || code === "NOT_FOUND";
}

/** 逐个读实例登记的因子任务；因子服务里已经没有的返回空。 */
async function loadInstanceTasks(instance: StrategyInstance): Promise<DeleteTaskLine[]> {
  return Promise.all(
    instanceTasks(instance.factor_tasks).map(async item => {
      try {
        return { market: item.market, taskId: item.taskId, factorTask: await getFactorTask(item.taskId, { silent: true }) };
      } catch (error) {
        if (isNotFound(error)) return { market: item.market, taskId: item.taskId };
        throw error;
      }
    })
  );
}

export interface InstanceDeleteHooks {
  /** 用户确认、请求发出前。 */
  onStart?: () => void;
  /** 请求结束后：ok 为真表示已删除。 */
  onSettled?: (ok: boolean) => void;
}

/** 删除实例的统一入口：读出将要删除的任务与数据集，确认后调用删除。 */
export async function confirmInstanceDelete(instance: StrategyInstance, hooks: InstanceDeleteHooks = {}) {
  let tasks: DeleteTaskLine[];
  try {
    tasks = await loadInstanceTasks(instance);
  } catch (error) {
    Message.error(`读取因子任务失败，暂不能删除：${error instanceof Error ? error.message : String(error)}`);
    return;
  }
  Modal.confirm({
    title: `删除实例「${instance.instance_id}」`,
    content: () =>
      h(
        "div",
        { class: "instance-delete-confirm" },
        instanceDeleteLines(instance.instance_id, tasks).map(line => h("p", line))
      ),
    okText: "确认删除",
    cancelText: "取消",
    okButtonProps: { status: "danger" },
    onOk: () => void runDelete(instance.instance_id, hooks)
  });
}

async function runDelete(instanceId: string, hooks: InstanceDeleteHooks) {
  hooks.onStart?.();
  const pending = Message.loading({ content: "正在删除实例…", duration: 0 });
  let ok = false;
  try {
    await deleteInstance(instanceId);
    ok = true;
    Message.success("实例已删除");
  } catch (error) {
    const message = error instanceof Error && error.message ? error.message : "删除请求失败";
    Message.error(isDeleteIncomplete(error) ? `${DELETE_INCOMPLETE_TIP}（${message}）` : message);
  } finally {
    pending?.close?.();
    hooks.onSettled?.(ok);
  }
}
