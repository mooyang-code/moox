import { listInstances, listStrategies } from "@/api/strategy";

export interface StrategyLink {
  instanceId: string;
  strategyId: string;
  /** 策略定义的名称，查不到时退回策略编号 */
  name: string;
  enabled: boolean;
  health: string;
}

/**
 * 组合账户关联的策略：组合上记着策略实例编号，实例指向策略定义，一次取全部实例和策略建好映射，
 * 不为每个组合各查一次。键是组合账户编号。查询失败时返回空映射，页面照常显示。
 */
export async function loadStrategyLinks(): Promise<Map<string, StrategyLink>> {
  const result = new Map<string, StrategyLink>();
  try {
    const [instances, strategies] = await Promise.all([
      listInstances({ page: 1, page_size: 500 }),
      listStrategies({ page: 1, page_size: 500 })
    ]);
    const names = new Map(strategies.items.map(item => [item.strategy_id, item.name]));
    for (const instance of instances.items) {
      if (!instance.logical_account_id) continue;
      result.set(instance.logical_account_id, {
        instanceId: instance.instance_id,
        strategyId: instance.strategy_id,
        name: names.get(instance.strategy_id) || instance.strategy_id,
        enabled: instance.enabled,
        health: instance.health
      });
    }
  } catch {
    // 关联策略只是展示信息，取不到时留空。
  }
  return result;
}
