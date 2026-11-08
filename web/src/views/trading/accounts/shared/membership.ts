import type { LogicalAccount, TradingAccount } from "@/api/trade/types";
import { accountMapOf, joinedAccountIds } from "../account-list";
import {
  accountEnvironment,
  logicalEnvironment,
  logicalInEnvironment,
  type Environment
} from "@/components/trade-confirm/environment";

/**
 * 能加入这个组合的执行账户：环境、模拟或实盘、结算资产都和组合一致（后端的校验），
 * 已启用，并且不在任何组合里（一个执行账户最多属于一个组合）。市场不限，组合的可交易市场取成员的并集。
 * 环境按 logicalEnvironment 判断：还没有可判断成员的实盘组合按进入的页签（entered）算，从测试网页签进来只列测试网账户，
 * 从生产环境页签进来（或不知道从哪进来）只列生产环境账户，和详情页显示的环境一致。
 */
export function eligibleAccounts(
  accounts: readonly TradingAccount[],
  logicals: readonly LogicalAccount[],
  logical: LogicalAccount,
  entered?: Environment
): TradingAccount[] {
  const joined = joinedAccountIds(logicals);
  const accountMap = accountMapOf(accounts);
  const env = logicalEnvironment(logical, accountMap, entered);
  return accounts.filter(
    item =>
      item.status === "ENABLED" &&
      !joined.has(item.trading_account_id) &&
      item.execution_mode === logical.execution_mode &&
      item.settlement_asset === logical.settlement_asset &&
      accountEnvironment(item) === env
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
