/**
 * 策略模块内的跳转目标统一在这里构造：页面之间只通过这些函数互相链接，路由名与 query 键改动时只改一处。
 * 「策略运行」是一个带标题 Tab 的工作台（运行实例 / 运行结果 / 策略回放），Tab 由 query.tab 决定，运行实例是默认 Tab。
 */
export const STRATEGY_TASKS_ROUTE = "strategy-tasks";

export type StrategyTaskTab = "instances" | "results" | "replay";

export interface StrategyLink {
  name: string;
  params?: Record<string, string>;
  query?: Record<string, string>;
}

function compact(query: Record<string, string | undefined>): Record<string, string> {
  return Object.fromEntries(Object.entries(query).filter((entry): entry is [string, string] => Boolean(entry[1])));
}

export function normalizeTaskTab(value: unknown): StrategyTaskTab {
  const raw = Array.isArray(value) ? value[0] : value;
  return raw === "results" || raw === "replay" ? raw : "instances";
}

/** 运行实例 Tab：可带定义筛选，create 为真时打开创建抽屉并以该定义为初始值。 */
export function instancesLink(options: { strategyId?: string; create?: boolean } = {}): StrategyLink {
  return {
    name: STRATEGY_TASKS_ROUTE,
    query: compact({ strategy_id: options.strategyId, create: options.create ? "1" : undefined })
  };
}

/** 运行结果 Tab：instanceId 为空时由页面选中默认实例。 */
export function resultsLink(instanceId = ""): StrategyLink {
  return { name: STRATEGY_TASKS_ROUTE, query: compact({ tab: "results", instance: instanceId }) };
}

/** 策略回放 Tab：replayId 打开已有回放；其余参数是发起回放的来源预填，并打开创建抽屉。 */
export function replayLink(
  options: { replayId?: string; strategyId?: string; instanceId?: string; viewId?: string } = {}
): StrategyLink {
  const create = Boolean(options.strategyId || options.instanceId || options.viewId);
  return {
    name: STRATEGY_TASKS_ROUTE,
    query: compact({
      tab: "replay",
      replay_id: options.replayId,
      create: create ? "1" : undefined,
      instance_id: options.instanceId,
      strategy_id: options.instanceId ? undefined : options.strategyId,
      view_id: options.viewId
    })
  };
}

export function detailLink(instanceId: string): StrategyLink {
  return { name: "strategy-detail", params: { instanceId } };
}

export function definitionsLink(definitionId = ""): StrategyLink {
  return { name: "strategy-definitions", query: compact({ definition_id: definitionId }) };
}
