import { ControlMode, type LogicalAccount, type TradingAccount } from "@/api/trade/types";
import { joinedAccountIds } from "../account-list";
import {
  accountEnvironment,
  logicalEnvironment,
  logicalInEnvironment,
  type Environment
} from "@/components/trade-confirm/environment";

/**
 * 能加入这个组合的执行账户：环境、模拟或实盘、结算资产都和组合一致（后端的校验），
 * 已启用，并且不在任何组合里（一个执行账户最多属于一个组合）。市场不限，组合的可交易市场取成员的并集。
 */
export function eligibleAccounts(
  accounts: readonly TradingAccount[],
  logicals: readonly LogicalAccount[],
  logical: LogicalAccount
): TradingAccount[] {
  const joined = joinedAccountIds(logicals);
  const env = logicalEnvironment(logical);
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
 * 执行账户能加入的已有组合：环境一致、结算资产一致，并且允许编辑成员
 * （自主下单组合没有暂停态，随时可加；策略驱动组合需先暂停自动执行）。
 */
export function joinableLogicals(
  logicals: readonly LogicalAccount[],
  env: Environment,
  settlementAsset: string
): LogicalAccount[] {
  const asset = settlementAsset.trim().toUpperCase();
  return logicals.filter(
    item =>
      logicalInEnvironment(item, env) &&
      item.settlement_asset === asset &&
      (item.control_mode === ControlMode.MANUAL || item.automation_state === "PAUSED")
  );
}

/** 加入已有组合时的优先级：排在现有成员之后。 */
export function nextPriority(logical: LogicalAccount): number {
  return Math.max(0, ...(logical.members || []).map(member => member.priority)) + 1;
}
