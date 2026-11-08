import type { LogicalAccount, MarketType, TradingAccount } from "@/api/trade/types";
import { environmentLabel, logicalEnvironment } from "@/components/trade-confirm/environment";

// 策略市场在交易模块里的编号：A 股（equity）对应股票市场，可以挂 A 股模拟账户。
const marketTypes: Record<string, number> = { spot: 1, swap: 2, equity: 3 };

export interface LogicalAccountOption {
  account: LogicalAccount;
  label: string;
  disabledReason: string;
}

// 策略要输出的市场：types 是这些市场在交易模块里的编号。校验前为空，不过滤账户。
export interface ResolvedMarkets {
  types?: number[];
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
  return { types: markets.map(market => marketTypes[market]).filter(Boolean) };
}

/**
 * 选项前面的环境：模拟盘、测试网、生产。组合账户本身只区分模拟和实盘，测试网和生产要看成员执行账户；
 * 执行账户没读到（tradingAccounts 为 null）时实盘只写“实盘”，不猜是测试网还是生产。
 */
export function environmentPrefix(account: LogicalAccount, tradingAccounts: ReadonlyMap<string, TradingAccount> | null): string {
  if (account.execution_mode === 1) return environmentLabel("paper");
  if (!tradingAccounts) return "实盘";
  return environmentLabel(logicalEnvironment(account, tradingAccounts));
}

// 账户的可交易市场必须包含策略要输出的全部市场（交易模块用启用成员已开通市场的并集算出）。
export function logicalAccountOptions(
  accounts: LogicalAccount[],
  markets: ResolvedMarkets = {},
  tradingAccounts: ReadonlyMap<string, TradingAccount> | null = null
): LogicalAccountOption[] {
  return accounts
    .filter(
      account =>
        account.control_mode === 1 && (markets.types ?? []).every(type => (account.markets ?? []).includes(type as MarketType))
    )
    .map(account => {
      const members = (account.members || []).filter(member => member.enabled).length;
      const disabledReason = account.owner_instance_id ? `已绑定实例 ${account.owner_instance_id}` : "";
      const state = account.ready ? "就绪" : "未就绪";
      const suffix = disabledReason ? `，${disabledReason}` : "";
      return {
        account,
        disabledReason,
        label: `${environmentPrefix(account, tradingAccounts)} · ${account.name || account.logical_account_id}（${members} 个成员，${state}${suffix}）`
      };
    });
}
