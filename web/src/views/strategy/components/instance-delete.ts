import { h } from "vue";
import { Message, Modal } from "@arco-design/web-vue";
import { listFactorTasks } from "@/api/factor";
import type { FactorTask } from "@/api/factor/types";
import { deleteInstance } from "@/api/strategy";
import type { StrategyInstance } from "@/api/strategy-types";
import { instanceTasks, marketLabel } from "@/views/strategy/market-mode";

export const DELETE_CANCELED_TIP = "已取消，实例没有删除";
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

/** 确认框正文：逐个列出将要删除的因子任务与结果数据集，最后写明结果数据不可恢复。 */
export function instanceDeleteLines(tasks: DeleteTaskLine[]): string[] {
  const lines = ["将一起删除下面的因子任务和它们的结果数据集："];
  for (const task of tasks) {
    const market = marketLabel(task.market);
    if (!task.factorTask) {
      lines.push(`${market}：${task.taskId}（已删除）`);
      continue;
    }
    const name = task.factorTask.name ? `「${task.factorTask.name}」` : "";
    lines.push(`${market}：因子任务 ${task.taskId}${name}，结果数据集 ${task.factorTask.result_dataset_id || "-"}`);
  }
  lines.push("结果数据不可恢复。交易模块里的历史目标和订单保留。");
  return lines;
}

/** 超时或中途失败：删除可能已经做了一部分，再删一次即可接着做完。 */
export function isDeleteIncomplete(error: unknown): boolean {
  const message = error instanceof Error ? error.message : String(error ?? "");
  return /timeout|超时|可以重新执行删除/i.test(message);
}

/** 列出因子服务里的全部任务（按页取完），用来在确认框里写出任务名称和结果数据集。 */
async function loadFactorTasks(): Promise<Map<string, FactorTask>> {
  const tasks = new Map<string, FactorTask>();
  for (let page = 1; page <= 50; page++) {
    const rsp = await listFactorTasks({ page: { page, size: 200 } });
    for (const info of rsp.factor_tasks ?? []) tasks.set(info.factor_task.task_id, info.factor_task);
    if (!rsp.page_result?.has_more || !(rsp.factor_tasks ?? []).length) break;
  }
  return tasks;
}

export interface InstanceDeleteHooks {
  /** 用户确认、请求发出前。 */
  onStart?: () => void;
  /** 请求结束后：ok 为真表示已删除。 */
  onSettled?: (ok: boolean) => void;
}

/** 删除实例的统一入口：读出将要删除的任务与数据集，确认后调用删除。 */
export async function confirmInstanceDelete(instance: StrategyInstance, hooks: InstanceDeleteHooks = {}) {
  let existing: Map<string, FactorTask>;
  try {
    existing = await loadFactorTasks();
  } catch (error) {
    Message.error(`读取因子任务失败，暂不能删除：${error instanceof Error ? error.message : String(error)}`);
    return;
  }
  const tasks = instanceTasks(instance.factor_tasks).map(item => ({
    market: item.market,
    taskId: item.taskId,
    factorTask: existing.get(item.taskId)
  }));
  Modal.confirm({
    title: `删除实例「${instance.instance_id}」`,
    content: () =>
      h(
        "div",
        { class: "instance-delete-confirm" },
        instanceDeleteLines(tasks).map(line => h("p", line))
      ),
    okText: "确认删除",
    cancelText: "取消",
    okButtonProps: { status: "danger" },
    onOk: () => void runDelete(instance.instance_id, hooks),
    onCancel: () => Message.info(DELETE_CANCELED_TIP)
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
