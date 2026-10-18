import type { Replay, Strategy, StrategyInstance, StrategyTargetSnapshot } from "@/api/strategy-types";
import {
  deriveTargetState,
  formatStrategyTime,
  percent,
  rankTargets,
  targetStateTag,
  type TargetState
} from "@/views/strategy/model";
import { replayLink, resultsLink, type StrategyLink } from "@/views/strategy/links";
import { shortId, sortInstancesForResults } from "@/views/strategy/results/results-model";

export interface OverviewInput {
  instances: StrategyInstance[];
  /** 已读取的最新目标，按实例 ID；没有读取（停用或超出读取上限）的实例不在其中。 */
  targets: Record<string, StrategyTargetSnapshot>;
  replays: Replay[];
  catalog: Strategy[];
  nowMs: number;
}

export type Tone = "ok" | "danger" | "muted" | "normal";

export interface StatItem {
  key: string;
  label: string;
  value: string;
  hint: string;
  tone: Tone;
}

export interface TargetChip {
  instrumentId: string;
  text: string;
  short: boolean;
}

export interface InstanceCard {
  instanceId: string;
  title: string;
  subtitle: string;
  enabled: boolean;
  health: string;
  trading: boolean;
  state: TargetState;
  stateTag: { label: string; color: string };
  chips: TargetChip[];
  more: number;
  period: string;
  links: { results: StrategyLink; replay: StrategyLink };
}

export interface PendingItem {
  kind: "degraded" | "session_unverified" | "expired" | "releasing";
  instanceId: string;
  title: string;
  text: string;
  target: StrategyLink;
}

/** 一张实例卡片最多展示的目标标的数，其余折叠为“+N”。 */
export const CARD_CHIP_LIMIT = 6;
/** 总览最多展示的实例卡片数，其余在「运行结果」查看。 */
export const CARD_LIMIT = 12;

function definitionName(instance: StrategyInstance, catalog: Strategy[]): string {
  return catalog.find(item => item.strategy_id === instance.strategy_id)?.name || instance.strategy_id;
}

/** 实例的目标状态：停用实例为 inactive；启用但没有读取到目标的为 unknown。 */
export function stateOf(instance: StrategyInstance, targets: Record<string, StrategyTargetSnapshot>, nowMs: number): TargetState {
  if (!instance.enabled) return "inactive";
  const snapshot = targets[instance.instance_id];
  return snapshot ? deriveTargetState(instance, snapshot, nowMs) : "unknown";
}

export function activeReplays(replays: Replay[]): Replay[] {
  return replays.filter(item => item.status === "pending" || item.status === "running");
}

export function overviewStats(input: OverviewInput): StatItem[] {
  const enabled = input.instances.filter(item => item.enabled);
  const valid = enabled.filter(item => {
    const state = stateOf(item, input.targets, input.nowMs);
    return state === "valid" || state === "zero";
  }).length;
  const attention = pendingItems(input).length;
  const running = activeReplays(input.replays).length;
  return [
    {
      key: "instances",
      label: "运行实例",
      value: `${enabled.length} / ${input.instances.length}`,
      hint: "已启用 / 全部",
      tone: enabled.length ? "ok" : "muted"
    },
    {
      key: "valid",
      label: "目标有效",
      value: `${valid} / ${enabled.length}`,
      hint: "已启用实例中目标仍在有效期内",
      tone: enabled.length && valid === enabled.length ? "ok" : enabled.length ? "normal" : "muted"
    },
    {
      key: "attention",
      label: "需要处理",
      value: String(attention),
      hint: "降级、会话待核实、目标过期、会话释放中",
      tone: attention ? "danger" : "ok"
    },
    {
      key: "replays",
      label: "进行中的回放",
      value: String(running),
      hint: "排队或运行中",
      tone: running ? "normal" : "muted"
    }
  ];
}

export function instanceCards(input: OverviewInput): InstanceCard[] {
  return sortInstancesForResults(input.instances)
    .slice(0, CARD_LIMIT)
    .map(instance => {
      const state = stateOf(instance, input.targets, input.nowMs);
      const snapshot = input.targets[instance.instance_id];
      const ranked = snapshot ? rankTargets(snapshot.targets) : [];
      return {
        instanceId: instance.instance_id,
        title: definitionName(instance, input.catalog),
        subtitle: instance.instance_id,
        enabled: instance.enabled,
        health: instance.health,
        trading: Boolean(instance.logical_account_id),
        state,
        stateTag: targetStateTag(state),
        chips: ranked.slice(0, CARD_CHIP_LIMIT).map(row => ({
          instrumentId: row.target.instrument_id,
          text: `${row.target.instrument_id} ${percent(row.target.target_weight)}`,
          short: row.weight < 0
        })),
        more: Math.max(0, ranked.length - CARD_CHIP_LIMIT),
        period: snapshot?.bar_end_time ? `最近一期 ${formatStrategyTime(snapshot.bar_end_time)}` : "",
        links: {
          results: resultsLink(instance.instance_id),
          replay: replayLink({ instanceId: instance.instance_id, viewId: instance.view_id })
        }
      };
    });
}

/** 待处理事项：每个实例最多一条，按严重程度取最先命中的一条。 */
export function pendingItems(input: OverviewInput): PendingItem[] {
  const items: PendingItem[] = [];
  for (const instance of sortInstancesForResults(input.instances)) {
    const title = `${definitionName(instance, input.catalog)} · ${shortId(instance.instance_id)}`;
    const target = resultsLink(instance.instance_id);
    const base = { instanceId: instance.instance_id, title, target };
    if (instance.health === "degraded") {
      items.push({ ...base, kind: "degraded", text: "最近一期被跳过，查看历史结果中的跳过原因" });
    } else if (instance.health === "session_unverified") {
      items.push({ ...base, kind: "session_unverified", text: "无法核实 Trade 账户会话，目标是否被接受未知" });
    } else if (instance.enabled && stateOf(instance, input.targets, input.nowMs) === "expired") {
      items.push({ ...base, kind: "expired", text: "目标已过期，最近一期之后没有产出新结果" });
    } else if (!instance.enabled && instance.session_id) {
      items.push({ ...base, kind: "releasing", text: "已停用但会话仍在释放，旧目标不代表当前仓位" });
    }
  }
  return items;
}
