import type { LogicalAccount } from "@/api/trade/types";

const marketTypes: Record<string, number> = { spot: 1, swap: 2 };

// 交易模块还没有账户的市场：实例只能“仅计算”，账户列表为空并给出提示。
const marketsWithoutAccounts: Record<string, string> = { equity: "A 股暂无交易账户" };

export interface LogicalAccountOption {
  account: LogicalAccount;
  label: string;
  disabledReason: string;
}

// 校验结果里的市场：type 是交易模块的市场编号；noAccount 非空表示这个市场没有交易账户。校验前两者都为空，不过滤账户。
export interface ResolvedMarket {
  type?: number;
  noAccount?: string;
}

// resolved_json 来自校验结果，校验前不知道市场。
export function resolvedMarket(resolvedJSON?: string): ResolvedMarket {
  if (!resolvedJSON) return {};
  try {
    const resolved = JSON.parse(resolvedJSON) as { market_type?: string };
    const market = resolved.market_type || "";
    return { type: marketTypes[market], noAccount: marketsWithoutAccounts[market] };
  } catch {
    return {};
  }
}

export function logicalAccountOptions(accounts: LogicalAccount[], market: ResolvedMarket = {}): LogicalAccountOption[] {
  if (market.noAccount) return [];
  return accounts
    .filter(account => account.control_mode === 1 && (market.type === undefined || account.market_type === market.type))
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
