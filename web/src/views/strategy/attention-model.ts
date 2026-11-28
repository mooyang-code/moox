import type { Replay, Strategy, StrategyInstance, StrategyTargetSnapshot } from "@/api/strategy-types";
import { deriveTargetState, type TargetState } from "@/views/strategy/model";
import { resultsLink, type StrategyLink } from "@/views/strategy/links";
import { shortId, sortInstancesForResults } from "@/views/strategy/results/results-model";

// 首页与策略运行页共用的策略状态：实例的目标状态、要人处理的实例、进行中的回放。

export interface AttentionInput {
  instances: StrategyInstance[];
  /** 已读取的最新目标，按实例 ID；没有读取（停用或超出读取上限）的实例不在其中。 */
  targets: Record<string, StrategyTargetSnapshot>;
  catalog: Strategy[];
  nowMs: number;
  /** 已暂停自动执行的组合账户 ID：绑定它们的实例目标不会执行。 */
  pausedAccounts?: Set<string>;
}

export interface PendingItem {
  kind: "degraded" | "paused" | "expired";
  instanceId: string;
  title: string;
  text: string;
  target: StrategyLink;
}

function definitionName(instance: StrategyInstance, catalog: Strategy[]): string {
  return catalog.find(item => item.strategy_id === instance.strategy_id)?.name || instance.strategy_id;
}

/** 实例的目标状态：停用实例为 inactive；启用但没有读取到目标的为 unknown。 */
export function stateOf(instance: StrategyInstance, targets: Record<string, StrategyTargetSnapshot>, nowMs: number): TargetState {
  if (!instance.enabled) return "inactive";
  const snapshot = targets[instance.instance_id];
  return snapshot ? deriveTargetState(instance, snapshot, nowMs) : "unknown";
}

/** 排队或运行中的回放。 */
export function activeReplays(replays: Replay[]): Replay[] {
  return replays.filter(item => item.status === "queued" || item.status === "running");
}

/** 待处理事项：每个实例最多一条，按严重程度取最先命中的一条。 */
export function pendingItems(input: AttentionInput): PendingItem[] {
  const items: PendingItem[] = [];
  for (const instance of sortInstancesForResults(input.instances)) {
    const title = `${definitionName(instance, input.catalog)} · ${shortId(instance.instance_id)}`;
    const target = resultsLink(instance.instance_id);
    const base = { instanceId: instance.instance_id, title, target };
    if (instance.health === "degraded") {
      items.push({ ...base, kind: "degraded", text: "最近一期被跳过，查看历史结果中的跳过原因" });
    } else if (instance.enabled && instance.logical_account_id && input.pausedAccounts?.has(instance.logical_account_id)) {
      items.push({
        ...base,
        kind: "paused",
        text: "组合账户已暂停，目标不会执行，到组合账户恢复自动执行",
        target: { name: "trading-account-detail", params: { logical_account_id: instance.logical_account_id } }
      });
    } else if (instance.enabled && stateOf(instance, input.targets, input.nowMs) === "expired") {
      items.push({ ...base, kind: "expired", text: "目标已过期，最近一期之后没有产出新结果" });
    }
  }
  return items;
}
