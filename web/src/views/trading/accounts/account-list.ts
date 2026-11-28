import { ExecutionMode, type LogicalAccount, type TradingAccount } from "@/api/trade/types";
import { exchangeLabel } from "@/api/trade";
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
  return account.execution_mode === ExecutionMode.PAPER && account.status !== "ENABLED";
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
  equityChangePct24h: number | null;
  positionCount: number;
  settlementAsset: string;
  /** 组合里能找到的执行账户（含停用的成员） */
  members: TradingAccount[];
}

function optionalNumber(value: string | undefined): number | null {
  if (value === undefined || value === "") return null;
  const n = Number(value);
  return Number.isFinite(n) ? n : null;
}

export function comboRows(
  logicals: readonly LogicalAccount[],
  accounts: ReadonlyMap<string, TradingAccount>,
  env: Environment
): ComboRow[] {
  return logicals
    .filter(logical => logicalInEnvironment(logical, env))
    .map(logical => {
      const members: TradingAccount[] = [];
      for (const member of logical.members || []) {
        const account = accounts.get(member.trading_account_id);
        if (account) members.push(account);
      }
      const exchanges = [...new Set(members.map(item => exchangeLabel(item.exchange)))];
      return {
        key: logical.logical_account_id,
        logical,
        env,
        exchangesText: exchanges.length ? exchanges.join("、") : "-",
        equity: optionalNumber(logical.total_equity),
        equityChangePct24h: optionalNumber(logical.equity_change_pct_24h),
        positionCount: logical.position_count ?? 0,
        settlementAsset: logical.settlement_asset,
        members
      };
    });
}
