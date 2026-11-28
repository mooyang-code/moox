import type { StrategyInstance } from "@/api/strategy-types";
import type { LogicalAccount, MarketType, TradingAccount } from "@/api/trade/types";
import { environmentLabel, logicalEnvironment } from "@/components/trade-confirm/environment";

// 策略市场与交易模块的市场写法相同（spot、swap、equity）：股票（equity）可以挂 A 股、港股模拟账户。
const tradeMarkets: readonly string[] = ["spot", "swap", "equity"];

export interface LogicalAccountOption {
  account: LogicalAccount;
  label: string;
  disabledReason: string;
}

// 策略要输出的市场（交易模块的市场写法）。校验前为空，不过滤账户。
export interface ResolvedMarkets {
  types?: MarketType[];
}

// resolved_json 来自校验结果，校验前不知道市场。市场模式的 select 为 both 或 prefer 不同时要输出现货和永续两个市场。
export function resolvedMarkets(resolvedJSON?: string): ResolvedMarkets {
  if (!resolvedJSON) return {};
  try {
    const resolved = JSON.parse(resolvedJSON) as { market?: { select?: string; prefer?: string } };
    const select = resolved.market?.select || "";
    const prefer = resolved.market?.prefer || select;
    if (!select) return {};
    return marketsToResolved(select === "both" || select !== prefer ? ["spot", "swap"] : [select]);
  } catch {
    return {};
  }
}

// 市场列表对应的账户要求：账户的可交易市场要包含列表里的全部市场（策略池按成员市场的并集）。
export function marketsToResolved(markets: string[]): ResolvedMarkets {
  return { types: markets.filter((market): market is MarketType => tradeMarkets.includes(market)) };
}

/**
 * 选项前面的环境：模拟盘、测试网、生产。组合账户本身只区分模拟和实盘，测试网和生产要看成员执行账户；
 * 执行账户没读到（tradingAccounts 为 null）时实盘只写“实盘”，不猜是测试网还是生产。
 */
export function environmentPrefix(account: LogicalAccount, tradingAccounts: ReadonlyMap<string, TradingAccount> | null): string {
  if (account.environment === 3) return environmentLabel("paper");
  if (account.environment === 1) return environmentLabel("testnet");
  if (account.environment === 2) return environmentLabel("prod");
  if (account.execution_mode === 1) return environmentLabel("paper");
  if (!tradingAccounts) return "实盘";
  return environmentLabel(logicalEnvironment(account, tradingAccounts));
}

/** 组合账户编号 → 绑着它的启用实例编号：取自 Strategy 的实例列表（归属只记在 Strategy，Trade 不存）。 */
export function enabledInstanceAccounts(instances: readonly StrategyInstance[]): Map<string, string> {
  const result = new Map<string, string>();
  for (const instance of instances) {
    if (instance.enabled && instance.logical_account_id && !result.has(instance.logical_account_id)) {
      result.set(instance.logical_account_id, instance.instance_id);
    }
  }
  return result;
}

// 账户的可交易市场必须包含策略要输出的全部市场（交易模块用启用成员已开通市场的并集算出）。
// 已被启用实例绑着的账户显示但不可选；boundInstances 读不到（null）时不置灰，启用时由 Strategy 拒绝。
export function logicalAccountOptions(
  accounts: LogicalAccount[],
  markets: ResolvedMarkets = {},
  tradingAccounts: ReadonlyMap<string, TradingAccount> | null = null,
  boundInstances: ReadonlyMap<string, string> | null = null
): LogicalAccountOption[] {
  return accounts
    .filter(account => account.control_mode === 1 && (markets.types ?? []).every(type => (account.markets ?? []).includes(type)))
    .map(account => {
      const members = (account.members || []).filter(member => member.enabled).length;
      const bound = boundInstances?.get(account.logical_account_id);
      const disabledReason = bound ? `已绑定实例 ${bound}` : "";
      const state = account.ready ? "就绪" : "未就绪";
      const suffix = disabledReason ? `，${disabledReason}` : "";
      return {
        account,
        disabledReason,
        label: `${environmentPrefix(account, tradingAccounts)} · ${account.name || account.logical_account_id}（${members} 个成员，${state}${suffix}）`
      };
    });
}
