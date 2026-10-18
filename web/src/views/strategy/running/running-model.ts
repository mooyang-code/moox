import { buildQuery, queryPage, queryString } from "@/views/strategy/query";

export type EnabledFilter = "" | "true" | "false";

/** 运行实例页的 URL 状态：筛选、分页、快速详情对象与创建抽屉。 */
export interface RunningState {
  strategyId: string;
  enabled: EnabledFilter;
  page: number;
  instanceId: string;
  create: boolean;
}

type QueryLike = Record<string, unknown>;

/** 从 URL 解析并规范化：非法页码回到 1；创建抽屉与快速详情互斥，创建优先。 */
export function parseRunningQuery(query: QueryLike): RunningState {
  const enabledRaw = queryString(query.enabled as string);
  const create = queryString(query.create as string) === "1";
  return {
    strategyId: queryString(query.strategy_id as string),
    enabled: enabledRaw === "true" || enabledRaw === "false" ? enabledRaw : "",
    page: queryPage(query.page as string),
    instanceId: create ? "" : queryString(query.instance_id as string),
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
      instance_id: state.create ? "" : state.instanceId,
      create: state.create ? "1" : ""
    },
    { page: 1 }
  );
}

/** 决定是否需要重新读取列表的键：只含影响列表内容的条件，不含抽屉状态。 */
export function listKey(state: RunningState): string {
  return `${state.strategyId}|${state.enabled}|${state.page}`;
}

export interface ActionState {
  disabled: boolean;
  reason: string;
}

/** 删除实例的条件与原因：启用中、会话仍在释放中都不能删。 */
export function instanceDeleteState(instance: { enabled: boolean; session_id: string }): ActionState {
  if (instance.enabled) return { disabled: true, reason: "实例已启用，请先停用" };
  if (instance.session_id) return { disabled: true, reason: "实例已停用，但会话仍在释放中，稍后可删除" };
  return { disabled: false, reason: "" };
}

export type EmptyKind = "none" | "no-instances" | "no-match" | "error";

/** 列表空态的类别：读取失败、当前空间没有实例、筛选无匹配分别提示。 */
export function emptyKind(options: { loading: boolean; error: string; count: number; filtered: boolean }): EmptyKind {
  if (options.loading || options.count > 0) return "none";
  if (options.error) return "error";
  return options.filtered ? "no-match" : "no-instances";
}

/** 删除最后一项后应显示的页码：末页被删空时回到上一页。 */
export function pageAfterRemoval(page: number, remainingOnPage: number): number {
  return remainingOnPage === 0 && page > 1 ? page - 1 : page;
}
