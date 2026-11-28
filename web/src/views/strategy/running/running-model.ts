import type { HistorySeed } from "@/api/collector/runtime-status";
import type { StrategyInstance } from "@/api/strategy-types";
import { toInstantMs } from "@/utils/time-zone";
import { buildQuery, queryPage, queryString } from "@/views/strategy/query";

export type EnabledFilter = "" | "true" | "false";

/** 运行实例页的 URL 状态：筛选、分页与创建抽屉。 */
export interface RunningState {
  strategyId: string;
  enabled: EnabledFilter;
  page: number;
  create: boolean;
}

type QueryLike = Record<string, unknown>;

/** 从 URL 解析并规范化：非法页码回到 1。 */
export function parseRunningQuery(query: QueryLike): RunningState {
  const enabledRaw = queryString(query.enabled as string);
  const create = queryString(query.create as string) === "1";
  return {
    strategyId: queryString(query.strategy_id as string),
    enabled: enabledRaw === "true" || enabledRaw === "false" ? enabledRaw : "",
    page: queryPage(query.page as string),
    create
  };
}

/** 状态写回 URL：默认值与空值不写入。 */
export function buildRunningQuery(state: RunningState): Record<string, string> {
  return buildQuery(
    {
      strategy_id: state.strategyId,
      enabled: state.enabled,
      page: state.page,
      create: state.create ? "1" : ""
    },
    { page: 1 }
  );
}

/** 决定是否需要重新读取列表的键：只含影响列表内容的条件，不含抽屉状态。 */
export function listKey(state: RunningState): string {
  return `${state.strategyId}|${state.enabled}|${state.page}`;
}

export type EmptyKind = "none" | "no-instances" | "no-match" | "error";

/** 列表空态的类别：读取失败、当前空间没有实例、筛选无匹配分别提示。 */
export function emptyKind(options: { loading: boolean; error: string; count: number; filtered: boolean }): EmptyKind {
  if (options.loading || options.count > 0) return "none";
  if (options.error) return "error";
  return options.filtered ? "no-match" : "no-instances";
}

/**
 * 实例是否在试运行：启用、挂了组合账户、还没确认实盘。试运行的目标只记为影子目标、不下单；
 * 没挂账户的实例启用时就确认了，不会出现试运行。
 */
export function isTrialInstance(
  instance: Pick<StrategyInstance, "enabled" | "logical_account_id" | "confirmed_at"> | null | undefined
): boolean {
  return Boolean(instance?.enabled && instance.logical_account_id && toInstantMs(instance.confirmed_at) === null);
}

/** 待生效的文案：实例已启用，但上游数据集正在历史补齐，策略暂不求值。 */
export const PENDING_SEED_LABEL = "待生效（上游历史补齐中）";

/**
 * 实例是否待生效：已启用，且 resolved_json 里任一任务的 dataset_id 或 source_dataset_id 命中补齐中的记录
 * （status 为 running，数据集相同，频率与 bar 忽略大小写相等）。resolved_json 解析不了或没有补齐数据时按没有处理。
 */
export function isPendingSeedInstance(
  instance: Pick<StrategyInstance, "enabled" | "resolved_json"> | null | undefined,
  seeds: HistorySeed[] | undefined
): boolean {
  if (!instance?.enabled) return false;
  const running = (seeds || []).filter(item => item.status === "running");
  if (!running.length) return false;
  let resolved: { tasks?: unknown; bar?: unknown };
  try {
    resolved = JSON.parse(instance.resolved_json || "{}") || {};
  } catch {
    return false;
  }
  const bar = String(resolved.bar || "").toLowerCase();
  if (!bar || !resolved.tasks || typeof resolved.tasks !== "object") return false;
  const tasks = Object.values(resolved.tasks as Record<string, { dataset_id?: string; source_dataset_id?: string }>);
  return tasks.some(task => {
    const ids = [task?.dataset_id, task?.source_dataset_id].filter(Boolean);
    return running.some(seed => ids.includes(seed.dataset_id) && (seed.frequency || "").toLowerCase() === bar);
  });
}
