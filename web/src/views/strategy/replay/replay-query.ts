import { buildQuery, queryPage, queryString } from "@/views/strategy/query";

/** 创建回放时的来源预填：入口一次性写入，不随表单输入更新。 */
export interface ReplayPrefill {
  strategyId: string;
  instanceId: string;
  viewId: string;
}

/** 回放页的 URL 状态：分析对象、两处分页、创建抽屉及其预填。 */
export interface ReplayQueryState {
  replayId: string;
  page: number;
  barPage: number;
  create: boolean;
  prefill: ReplayPrefill;
}

type QueryLike = Record<string, unknown>;

export const emptyPrefill: ReplayPrefill = { strategyId: "", instanceId: "", viewId: "" };

/** 从 URL 解析并规范化：非法页码回到 1；实例与定义同时出现时沿用“实例优先”，清掉定义。 */
export function parseReplayQuery(query: QueryLike): ReplayQueryState {
  const instanceId = queryString(query.instance_id as string);
  return {
    replayId: queryString(query.replay_id as string),
    page: queryPage(query.page as string),
    barPage: queryPage(query.bar_page as string),
    create: queryString(query.create as string) === "1",
    prefill: {
      strategyId: instanceId ? "" : queryString(query.strategy_id as string),
      instanceId,
      viewId: queryString(query.view_id as string)
    }
  };
}

/**
 * 状态写回 URL：默认值与空值不写；预填只在创建抽屉打开时保留，关闭后即清除（它们不表示分析对象）。
 * 没有选中回放时不写 bar_page。
 */
export function buildReplayQuery(state: ReplayQueryState): Record<string, string> {
  const prefill = state.create ? state.prefill : emptyPrefill;
  return buildQuery(
    {
      replay_id: state.replayId,
      page: state.page,
      bar_page: state.replayId ? state.barPage : 1,
      create: state.create ? "1" : "",
      strategy_id: prefill.instanceId ? "" : prefill.strategyId,
      instance_id: prefill.instanceId,
      view_id: prefill.viewId
    },
    { page: 1, bar_page: 1 }
  );
}
