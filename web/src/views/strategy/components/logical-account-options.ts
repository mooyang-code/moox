import type { LogicalAccount } from "@/api/trade/types";

const marketTypes: Record<string, number> = { spot: 1, swap: 2 };

// 交易模块还没有账户的市场：实例只能“仅计算”，账户列表为空并给出提示。
const marketsWithoutAccounts: Record<string, string> = { equity: "A 股暂无交易账户" };

export interface LogicalAccountOption {
  account: LogicalAccount;
  label: string;
  disabledReason: string;
}

// 策略要输出的市场：types 是这些市场在交易模块里的编号；noAccount 非空表示其中有没有交易账户的市场。校验前两者都为空，不过滤账户。
export interface ResolvedMarkets {
  types?: number[];
  noAccount?: string;
}

// resolved_json 来自校验结果，校验前不知道市场。市场模式的 select 为 both 或 prefer 不同时要输出现货和永续两个市场。
export function resolvedMarkets(resolvedJSON?: string): ResolvedMarkets {
  if (!resolvedJSON) return {};
  try {
    const resolved = JSON.parse(resolvedJSON) as { market?: { select?: string; prefer?: string } };
    const select = resolved.market?.select || "";
    const prefer = resolved.market?.prefer || select;
    if (!select) return {};
    const markets = select === "both" || select !== prefer ? ["spot", "swap"] : [select];
    const noAccount = markets.map(market => marketsWithoutAccounts[market]).find(Boolean);
    return { types: markets.map(market => marketTypes[market]).filter(Boolean), noAccount };
  } catch {
    return {};
  }
}

// 账户的可交易市场必须包含策略要输出的全部市场（交易模块用启用成员已开通市场的并集算出）。
export function logicalAccountOptions(accounts: LogicalAccount[], markets: ResolvedMarkets = {}): LogicalAccountOption[] {
  if (markets.noAccount) return [];
  return accounts
    .filter(
      account =>
        account.control_mode === 1 && (markets.types ?? []).every(type => (account.markets ?? []).includes(type as 1 | 2))
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
