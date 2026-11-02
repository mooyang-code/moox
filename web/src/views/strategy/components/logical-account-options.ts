import type { LogicalAccount } from "@/api/trade/types";

const marketTypes: Record<string, number> = { spot: 1, swap: 2 };

export interface LogicalAccountOption {
  account: LogicalAccount;
  label: string;
  disabledReason: string;
}

// resolved_json 来自校验结果，校验前不知道市场。
export function resolvedMarket(resolvedJSON?: string): number | undefined {
  if (!resolvedJSON) return undefined;
  try {
    const resolved = JSON.parse(resolvedJSON) as { market_type?: string };
    return marketTypes[resolved.market_type || ""];
  } catch {
    return undefined;
  }
}

export function logicalAccountOptions(accounts: LogicalAccount[], market?: number): LogicalAccountOption[] {
  return accounts
    .filter(account => account.control_mode === 1 && (market === undefined || account.market_type === market))
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
