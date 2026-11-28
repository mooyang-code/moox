import { listInstances } from "@/api/strategy";
import type { StrategyInstance } from "@/api/strategy-types";
import { toInstantMs } from "@/utils/time-zone";

export interface StrategyLink {
  instanceId: string;
  strategyId: string;
  /** 策略定义的名称，查不到时退回策略编号 */
  name: string;
  enabled: boolean;
  health: string;
}

/** 只需要组合账户的编号：实例与组合账户的归属只记在 Strategy，Trade 不存。 */
export interface LogicalRef {
  logical_account_id: string;
}

/**
 * 一个组合账户可能被多个实例引用（唯一索引只约束启用中的实例，停用的旧实例还留着），按这个顺序取：
 * 启用中的实例（同一账户最多一个），其次最新创建的（实例列表按创建时间倒序，同时间时取靠前的）。
 */
export function chooseInstance(candidates: readonly StrategyInstance[]): StrategyInstance | undefined {
  const enabled = candidates.find(item => item.enabled);
  if (enabled) return enabled;
  return [...candidates].sort((a, b) => (toInstantMs(b.created_at) ?? 0) - (toInstantMs(a.created_at) ?? 0))[0];
}

/** 把实例合成每个组合账户的关联策略，键是组合账户编号；策略名称直接取实例带出的 strategy_name。 */
export function buildStrategyLinks(
  logicals: readonly LogicalRef[],
  instances: readonly StrategyInstance[]
): Map<string, StrategyLink> {
  const result = new Map<string, StrategyLink>();
  for (const logical of logicals) {
    const candidates = instances.filter(item => item.logical_account_id === logical.logical_account_id);
    const instance = chooseInstance(candidates);
    if (!instance) continue;
    result.set(logical.logical_account_id, {
      instanceId: instance.instance_id,
      strategyId: instance.strategy_id,
      name: instance.strategy_name || instance.strategy_id,
      enabled: instance.enabled,
      health: instance.health
    });
  }
  return result;
}

/**
 * 一次取全部实例建好映射，不为每个组合各查一次。
 * 查询失败时返回 null：页面要写“策略读取失败”，不能把每个组合都说成“尚未绑定策略”。
 */
export async function loadStrategyLinks(logicals: readonly LogicalRef[]): Promise<Map<string, StrategyLink> | null> {
  try {
    const instances = await listInstances({ page: 1, size: 500 });
    return buildStrategyLinks(logicals, instances.items);
  } catch {
    return null;
  }
}
