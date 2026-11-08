import type { EquityPoint, LogicalAccount, TradingAccount } from "@/api/trade/types";
import { exchangeLabels } from "@/api/trade";
import { logicalInEnvironment, accountEnvironment, type Environment } from "@/components/trade-confirm/environment";

export function accountMapOf(accounts: readonly TradingAccount[]): Map<string, TradingAccount> {
  return new Map(accounts.map(account => [account.trading_account_id, account]));
}

/** 已加入组合的执行账户：只要出现在任一组合的成员里（启用或停用都算）就算已加入。 */
export function joinedAccountIds(logicals: readonly LogicalAccount[]): Set<string> {
  return new Set(logicals.flatMap(item => (item.members || []).map(member => member.trading_account_id)));
}

/** 已关闭的模拟账户：关闭会停用账户并移出组合，之后不能再启用，也不再出现在执行账户列表里。 */
export function isClosedPaper(account: Pick<TradingAccount, "execution_mode" | "status">): boolean {
  return account.execution_mode === 1 && account.status !== "ENABLED";
}

/** 执行账户子页签的一行：账户本身，以及它所在的组合（没加入组合时为空）。 */
export interface ExecutionRow {
  key: string;
  account: TradingAccount;
  logical: LogicalAccount | null;
  priority: number | null;
  enabled: boolean | null;
}

/** 当前环境下的执行账户（已关闭的模拟账户不列），带上所属组合；一个账户最多在一个组合里。 */
export function executionRows(
  accounts: readonly TradingAccount[],
  logicals: readonly LogicalAccount[],
  env: Environment
): ExecutionRow[] {
  const owner = new Map<string, { logical: LogicalAccount; priority: number; enabled: boolean }>();
  for (const logical of logicals) {
    for (const member of logical.members || []) {
      owner.set(member.trading_account_id, { logical, priority: member.priority, enabled: member.enabled });
    }
  }
  return accounts
    .filter(item => !isClosedPaper(item) && accountEnvironment(item) === env)
    .map(account => {
      const joined = owner.get(account.trading_account_id);
      return {
        key: account.trading_account_id,
        account,
        logical: joined?.logical ?? null,
        priority: joined?.priority ?? null,
        enabled: joined?.enabled ?? null
      };
    });
}

export interface ComboRow {
  key: string;
  logical: LogicalAccount;
  env: Environment;
  exchangesText: string;
  equity: number | null;
  settlementAsset: string;
  /** 组合里能找到的执行账户（含停用的成员） */
  members: TradingAccount[];
}

function snapshotNumber(value: string | undefined): number | null {
  if (value === undefined || value === "") return null;
  const n = Number(value);
  return Number.isFinite(n) ? n : null;
}

/** 成员权益之和；没有任何成员给出权益时返回 null。 */
export function sumEquity(accounts: readonly TradingAccount[]): number | null {
  let total: number | null = null;
  for (const account of accounts) {
    const value = snapshotNumber(account.snapshot?.equity);
    if (value !== null) total = (total ?? 0) + value;
  }
  return total;
}

export function comboRows(
  logicals: readonly LogicalAccount[],
  accounts: ReadonlyMap<string, TradingAccount>,
  env: Environment
): ComboRow[] {
  return logicals
    .filter(logical => logicalInEnvironment(logical, accounts, env))
    .map(logical => {
      const members: TradingAccount[] = [];
      const enabled: TradingAccount[] = [];
      for (const member of logical.members || []) {
        const account = accounts.get(member.trading_account_id);
        if (!account) continue;
        members.push(account);
        if (member.enabled) enabled.push(account);
      }
      const exchanges = [...new Set(members.map(item => exchangeLabels[item.exchange] || "-"))];
      return {
        key: logical.logical_account_id,
        logical,
        env,
        exchangesText: exchanges.length ? exchanges.join("、") : "-",
        equity: sumEquity(enabled),
        settlementAsset: logical.settlement_asset,
        members
      };
    });
}

/**
 * 近 24 小时权益变化比例：取窗口内第一个点和最后一个点。窗口内不足两个点、起点权益为 0 时返回 null。
 * points 是 QueryEquityCurve 在 [now - 24h, now] 内返回的点。
 */
export function equityChange(points: readonly EquityPoint[]): number | null {
  const sorted = [...points].sort((a, b) => Number(a.bucket_time) - Number(b.bucket_time));
  if (sorted.length < 2) return null;
  const first = Number(sorted[0].equity);
  const last = Number(sorted[sorted.length - 1].equity);
  if (!Number.isFinite(first) || !Number.isFinite(last) || first === 0) return null;
  return last / first - 1;
}
