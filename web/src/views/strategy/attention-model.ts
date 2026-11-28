import type { Replay, StrategyInstance, StrategyTargetSnapshot } from "@/api/strategy-types";
import { tradingAccountDetailLink } from "@/router/links";
import { toInstantMs } from "@/utils/time-zone";
import { deriveTargetState, type TargetState } from "@/views/strategy/model";
import { detailLink, type StrategyLink } from "@/views/strategy/links";

// 首页与策略运行页共用的策略状态：实例的目标状态、要人处理的实例、进行中的回放。

export interface AttentionInput {
  instances: StrategyInstance[];
  /** 已读取的最新目标，按实例 ID；没有读取（停用或超出读取上限）的实例不在其中。 */
  targets: Record<string, StrategyTargetSnapshot>;
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

/** 首页与待处理列表的实例顺序：已启用在前，其次是需要处理的（降级），再按更新时间从新到旧。 */
export function sortInstancesByPriority(instances: StrategyInstance[]): StrategyInstance[] {
  const attention = (item: StrategyInstance) => (item.health === "degraded" ? 0 : 1);
  return [...instances].sort(
    (a, b) =>
      Number(b.enabled) - Number(a.enabled) ||
      attention(a) - attention(b) ||
      (toInstantMs(b.updated_at) ?? 0) - (toInstantMs(a.updated_at) ?? 0) ||
      a.instance_id.localeCompare(b.instance_id)
  );
}

export function shortId(id: string): string {
  return id.length > 8 ? id.slice(0, 8) : id;
}

function definitionName(instance: StrategyInstance): string {
  return instance.strategy_name || instance.strategy_id;
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
  for (const instance of sortInstancesByPriority(input.instances)) {
    const title = `${definitionName(instance)} · ${shortId(instance.instance_id)}`;
    const target = detailLink(instance.instance_id);
    const base = { instanceId: instance.instance_id, title, target };
    if (instance.health === "degraded") {
      items.push({ ...base, kind: "degraded", text: "最近一期被跳过，查看历史结果中的跳过原因" });
    } else if (instance.enabled && instance.logical_account_id && input.pausedAccounts?.has(instance.logical_account_id)) {
      items.push({
        ...base,
        kind: "paused",
        text: "组合账户已暂停，目标不会执行，到组合账户恢复自动执行",
        target: tradingAccountDetailLink(instance.logical_account_id)
      });
    } else if (instance.enabled && stateOf(instance, input.targets, input.nowMs) === "expired") {
      items.push({ ...base, kind: "expired", text: "目标已过期，最近一期之后没有产出新结果" });
    }
  }
  return items;
}
