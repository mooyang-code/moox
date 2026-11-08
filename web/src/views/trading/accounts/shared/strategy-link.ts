import { listInstances, listStrategies } from "@/api/strategy";
import type { StrategyInstance } from "@/api/strategy-types";

export interface StrategyLink {
  instanceId: string;
  strategyId: string;
  /** 策略定义的名称，查不到时退回策略编号 */
  name: string;
  enabled: boolean;
  health: string;
}

/** 只需要组合账户的编号和它记着的策略实例编号。 */
export interface LogicalRef {
  logical_account_id: string;
  owner_instance_id?: string;
}

/**
 * 一个组合账户可能被多个实例引用（唯一索引只约束启用中的实例，停用的旧实例还留着），按这个顺序取：
 * 组合自己记着的实例，其次启用中的，再其次最新创建的（实例列表按创建时间倒序，同时间时取靠前的）。
 */
export function chooseInstance(logical: LogicalRef, candidates: readonly StrategyInstance[]): StrategyInstance | undefined {
  const owned = candidates.find(item => item.instance_id === logical.owner_instance_id);
  if (owned) return owned;
  const enabled = candidates.find(item => item.enabled);
  if (enabled) return enabled;
  return [...candidates].sort((a, b) => Date.parse(b.created_at || "") - Date.parse(a.created_at || "") || 0)[0];
}

/** 把实例和策略定义合成每个组合账户的关联策略，键是组合账户编号。 */
export function buildStrategyLinks(
  logicals: readonly LogicalRef[],
  instances: readonly StrategyInstance[],
  strategyNames: ReadonlyMap<string, string>
): Map<string, StrategyLink> {
  const result = new Map<string, StrategyLink>();
  for (const logical of logicals) {
    const candidates = instances.filter(item => item.logical_account_id === logical.logical_account_id);
    const instance = chooseInstance(logical, candidates);
    if (!instance) continue;
    result.set(logical.logical_account_id, {
      instanceId: instance.instance_id,
      strategyId: instance.strategy_id,
      name: strategyNames.get(instance.strategy_id) || instance.strategy_id,
      enabled: instance.enabled,
      health: instance.health
    });
  }
  return result;
}

/**
 * 一次取全部实例和策略建好映射，不为每个组合各查一次。查询失败时返回空映射，页面照常显示。
 */
export async function loadStrategyLinks(logicals: readonly LogicalRef[]): Promise<Map<string, StrategyLink>> {
  try {
    const [instances, strategies] = await Promise.all([
      listInstances({ page: 1, page_size: 500 }),
      listStrategies({ page: 1, page_size: 500 })
    ]);
    return buildStrategyLinks(logicals, instances.items, new Map(strategies.items.map(item => [item.strategy_id, item.name])));
  } catch {
    // 关联策略只是展示信息，取不到时留空。
    return new Map();
  }
}
