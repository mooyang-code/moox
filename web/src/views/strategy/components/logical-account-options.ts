import type { LogicalAccount, MarketType } from "@/api/trade/types";

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

// 账户的可交易市场必须包含策略要输出的全部市场（交易模块用启用成员已开通市场的并集算出）。
export function logicalAccountOptions(accounts: LogicalAccount[], markets: ResolvedMarkets = {}): LogicalAccountOption[] {
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
        label: `${account.name || account.logical_account_id}（${members} 个成员，${state}${suffix}）`
      };
    });
}
