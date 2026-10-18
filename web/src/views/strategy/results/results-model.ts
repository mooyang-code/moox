import type { Strategy, StrategyInstance } from "@/api/strategy-types";

export interface InstanceTab {
  instanceId: string;
  title: string;
  enabled: boolean;
  health: string;
}

/** 结果页的实例顺序：已启用在前，其次是需要处理的（降级、会话待核实），再按更新时间从新到旧。 */
export function sortInstancesForResults(instances: StrategyInstance[]): StrategyInstance[] {
  const attention = (item: StrategyInstance) => (item.health === "degraded" || item.health === "session_unverified" ? 0 : 1);
  return [...instances].sort(
    (a, b) =>
      Number(b.enabled) - Number(a.enabled) ||
      attention(a) - attention(b) ||
      (Date.parse(b.updated_at) || 0) - (Date.parse(a.updated_at) || 0) ||
      a.instance_id.localeCompare(b.instance_id)
  );
}

export function shortId(id: string): string {
  return id.length > 8 ? id.slice(0, 8) : id;
}

/** 实例 Tab 的标题：定义名称 · 实例 ID 前 8 位；停用的实例加“（停用）”。 */
export function instanceTabTitle(instance: StrategyInstance, catalog: Strategy[]): string {
  const name = catalog.find(item => item.strategy_id === instance.strategy_id)?.name || instance.strategy_id;
  return `${name} · ${shortId(instance.instance_id)}${instance.enabled ? "" : "（停用）"}`;
}

export function instanceTabs(instances: StrategyInstance[], catalog: Strategy[]): InstanceTab[] {
  return sortInstancesForResults(instances).map(instance => ({
    instanceId: instance.instance_id,
    title: instanceTabTitle(instance, catalog),
    enabled: instance.enabled,
    health: instance.health
  }));
}

/**
 * 当前选中的实例：URL 指定的优先（即使它不在列表里，例如已删除但仍可查看历史的实例）；
 * 没有指定时取排序后的第一个；列表为空时为空串。
 */
export function resolveSelectedInstance(requested: string, tabs: InstanceTab[]): string {
  if (requested) return requested;
  return tabs[0]?.instanceId ?? "";
}
