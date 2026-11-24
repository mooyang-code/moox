// 测试用：按 DSL 推出建实例要绑定的任务，模拟后端 GetStrategy / ValidateStrategy 返回的 requirement。
// 页面代码不再解析 DSL，组件测试用它代替 useTaskRequirement，按测试里登记的定义给出要求。
import { computed, ref, type Ref } from "vue";
import { parseDocument } from "yaml";
import type { StrategyTaskRequirement } from "@/api/strategy-types";
import type { RequirementSource } from "@/views/strategy/use-task-requirement";

const MARKET_ORDER = ["spot", "swap", "equity"];

interface MarketRequirement {
  declared: boolean;
  markets: string[];
}

/**
 * 从 DSL 文本读出市场模式需要的计算任务：select 为 both 或 prefer 与 select 不同要现货、永续各一个，其余只要一个。
 * 解析失败或没写 market 时 declared 为 false（后端校验保存时的写法是否合法）。
 */
function marketRequirement(dslYaml: string): MarketRequirement {
  try {
    const market = parseDocument(dslYaml).toJS()?.market;
    if (!market || typeof market !== "object") return { declared: false, markets: [] };
    const select = String(market.select ?? "");
    const prefer = String(market.prefer ?? select);
    if (select === "both" || (select && prefer && select !== prefer)) return { declared: true, markets: ["spot", "swap"] };
    if (select === "spot" || select === "swap") return { declared: true, markets: [select] };
  } catch {
    // 语法错误由后端校验报告
  }
  return { declared: false, markets: [] };
}

/** 附加来源与参考市场：sources 是 DSL 声明的附加来源（名字与周期，声明顺序），references 是 market.reference 写出的参考市场。 */
interface SourceRequirement {
  sources: { name: string; bar: string }[];
  references: string[];
}

/** 从 DSL 文本读出附加来源与参考市场；解析失败或都没写时为空（后端校验写法）。 */
function sourceRequirement(dslYaml: string): SourceRequirement {
  try {
    const document = parseDocument(dslYaml).toJS();
    const sources = document?.sources && typeof document.sources === "object" ? document.sources : {};
    const reference = Array.isArray(document?.market?.reference) ? document.market.reference : [];
    return {
      sources: Object.entries(sources).map(([name, value]: [string, any]) => ({ name, bar: String(value?.bar ?? "") })),
      references: reference.map((item: unknown) => String(item)).filter((item: string) => item === "spot" || item === "swap")
    };
  } catch {
    return { sources: [], references: [] };
  }
}

/** 策略池的成员引用；DSL 不是策略池（没有 strategy_pool）或无法解析时返回 null。 */
function poolMembers(dslYaml: string): { id: string; strategyId: string }[] | null {
  try {
    const pool = parseDocument(dslYaml).toJS()?.strategy_pool;
    if (pool === undefined || pool === null) return null;
    const members = Array.isArray(pool?.members) ? pool.members : [];
    return members.map((member: any) => ({ id: String(member?.id ?? ""), strategyId: String(member?.strategy ?? "") }));
  } catch {
    return null;
  }
}

interface PoolRequirement {
  /** 池要绑定计算任务的市场：各成员所需市场的并集（现货在前）；有问题时为空。 */
  markets: string[];
  /** 阻止创建的问题（中文）：成员定义读不到、没写 market、是策略池等。 */
  problems: string[];
}

/**
 * 策略池要绑定的计算任务：成员引用的策略定义各自按 marketRequirement 解析市场，取并集。
 * lookup 返回成员策略的 DSL 文本；null 表示确认不存在，undefined 表示还在读取。不是策略池时返回 null。
 */
function poolRequirement(dslYaml: string, lookup: (strategyId: string) => string | null | undefined): PoolRequirement | null {
  const members = poolMembers(dslYaml);
  if (!members) return null;
  if (!members.length) return { markets: [], problems: ["策略池没有写成员（strategy_pool.members）"] };
  const problems: string[] = [];
  const markets = new Set<string>();
  // 股票策略不写 market：没写 market 的成员按股票成员处理，池绑定股票任务；股票成员不能与现货、永续成员混用。
  const stockMembers: string[] = [];
  for (const member of members) {
    const label = `成员 ${member.id || "?"}`;
    if (!member.strategyId) {
      problems.push(`${label} 没有写 strategy`);
      continue;
    }
    const memberDsl = lookup(member.strategyId);
    if (memberDsl === undefined) {
      problems.push(`正在读取${label}引用的策略 ${member.strategyId}…`);
    } else if (memberDsl === null) {
      problems.push(`${label} 引用的策略 ${member.strategyId} 不存在（定义已删除，或读取失败）`);
    } else if (poolMembers(memberDsl)) {
      problems.push(`${label} 引用的策略 ${member.strategyId} 本身是策略池，策略池不能嵌套`);
    } else {
      const requirement = marketRequirement(memberDsl);
      if (!requirement.declared) stockMembers.push(label);
      else requirement.markets.forEach(market => markets.add(market));
    }
  }
  if (stockMembers.length && markets.size)
    problems.push(
      `${stockMembers.join("、")} 引用的策略没有写 market，按股票成员处理；股票成员不能与现货、永续成员放在同一个池里（现货、永续成员要显式写 market）`
    );
  else if (stockMembers.length) markets.add("equity");
  return { markets: problems.length ? [] : MARKET_ORDER.filter(market => markets.has(market)), problems };
}

/** 测试里登记的策略定义（ID → DSL）：按 ID 读要求时从这里取。 */
export const fixtureDefinitions = new Map<string, string>();

/** 按 DSL 推出要求；lookup 返回策略池成员的 DSL。 */
export function fixtureRequirement(
  dsl: string,
  lookup: (id: string) => string | null | undefined = id => fixtureDefinitions.get(id) ?? null
): StrategyTaskRequirement {
  const pool = poolRequirement(dsl, lookup);
  if (pool)
    return { pool: true, marketDeclared: pool.markets.length > 0, taskKeys: pool.markets, sources: [], problems: pool.problems };
  const market = marketRequirement(dsl);
  const extra = sourceRequirement(dsl);
  if (!market.declared)
    return { pool: false, marketDeclared: false, taskKeys: [], sources: extra.sources, problems: [], referenceMarkets: [] };
  const references = extra.references.filter(item => !market.markets.includes(item));
  const sourceKeys = extra.sources.flatMap(source => market.markets.map(item => `${source.name}:${item}`));
  return {
    pool: false,
    marketDeclared: true,
    taskKeys: [...market.markets, ...references, ...sourceKeys],
    sources: extra.sources,
    problems: [],
    referenceMarkets: references
  };
}

/** 代替 useTaskRequirement：同步按登记的定义或 DSL 文本给出要求。 */
export function useFixtureTaskRequirement(source: Ref<RequirementSource>) {
  const requirement = computed(() => {
    const next = source.value;
    const dsl = next.strategyId ? fixtureDefinitions.get(next.strategyId) : next.dsl;
    return dsl ? fixtureRequirement(dsl) : null;
  });
  return { requirement, loading: ref(false), error: ref(""), reload: () => undefined };
}

/** 把定义列表登记成按 ID 读要求时的来源（先清空）。 */
export function registerFixtureDefinitions(list: readonly { strategy_id: string; dsl_yaml?: string }[]) {
  fixtureDefinitions.clear();
  for (const item of list) if (item.dsl_yaml) fixtureDefinitions.set(item.strategy_id, item.dsl_yaml);
}
