import type { LogicalAccount, TradingAccount } from "@/api/trade/types";
import { accountMapOf, joinedAccountIds } from "../account-list";
import { accountEnvironment, logicalInEnvironment, type Environment } from "./environment";

/**
 * 能加入这个组合的执行账户：环境、模拟或实盘、结算资产都和组合一致（后端的校验），
 * 已启用，并且不在任何组合里（一个执行账户最多属于一个组合）。市场不限，组合的可交易市场取成员的并集。
 * 环境按 logicalInEnvironment 判断：还没有可判断成员的实盘组合，测试网和生产环境的账户都能加入。
 */
export function eligibleAccounts(
  accounts: readonly TradingAccount[],
  logicals: readonly LogicalAccount[],
  logical: LogicalAccount
): TradingAccount[] {
  const joined = joinedAccountIds(logicals);
  const accountMap = accountMapOf(accounts);
  return accounts.filter(
    item =>
      item.status === "ENABLED" &&
      !joined.has(item.trading_account_id) &&
      item.execution_mode === logical.execution_mode &&
      item.settlement_asset === logical.settlement_asset &&
      logicalInEnvironment(logical, accountMap, accountEnvironment(item))
  );
}

/**
 * 执行账户能加入的已有组合：环境一致、结算资产一致，并且已暂停自动执行
 * （后端要求增删成员前先暂停，运行中的组合要先去暂停）。
 */
export function joinableLogicals(
  logicals: readonly LogicalAccount[],
  accounts: ReadonlyMap<string, TradingAccount>,
  env: Environment,
  settlementAsset: string
): LogicalAccount[] {
  const asset = settlementAsset.trim().toUpperCase();
  return logicals.filter(
    item => logicalInEnvironment(item, accounts, env) && item.settlement_asset === asset && item.automation_state === "PAUSED"
  );
}

/** 加入已有组合时的优先级：排在现有成员之后。 */
export function nextPriority(logical: LogicalAccount): number {
  return Math.max(0, ...(logical.members || []).map(member => member.priority)) + 1;
}
