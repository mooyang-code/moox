import { callTrade } from "./http";
import type { LogicalAccount, RetInfo } from "./types";

/** 组合账户的下单设置，数值用字符串；字段含义见 Trade 的 domain/ordersettings。 */
export interface OrderSettings {
  /** TARGET_RATIO | POSITION_RATIO | EQUITY_RATIO | ALWAYS */
  rebalance_threshold_mode: string;
  rebalance_threshold_ratio: string;
  min_notional_spot: string;
  min_notional_swap: string;
  /** 键是内部标的 ID 或 *，值是每个自然小时累计下单金额上限 */
  hourly_cap_spot?: Record<string, string>;
  hourly_cap_swap?: Record<string, string>;
  max_child_notional: string;
  randomize_split: boolean;
  /** 0 表示用默认批间隔 */
  batch_interval_seconds: number;
  /** MARKET | PSEUDO_MARKET */
  order_mode: string;
  slippage_step: string;
  slippage_max: string;
  max_attempts: number;
  resting_ratio: string;
  /** 0 表示挂单保留到下一份目标到达为止 */
  resting_ttl_seconds: number;
}

/** 组合账户的资金设置，数值用字符串；字段含义见 Trade 的 domain/fundsettings。 */
export interface FundSettings {
  reserve_ratio: string;
  bnb_discount: boolean;
  bnb_reserve_amount: string;
  dust_convert_enabled: boolean;
  /** 0 表示默认 15 天 */
  dust_convert_interval_days: number;
  /** 稳定币及目标比例：必须含 USDT，比例之和为 1 */
  stablecoin_margin?: Record<string, string>;
  /** 保证金币及配置的固定 U 值 */
  margin_coins?: Record<string, string>;
  arbitrage_base_assets?: string[];
}

/** 带当前生效设置的组合账户（Trade 在 LogicalAccount 里只读返回两组设置，父组件取到后传给设置表单）。 */
export type LogicalAccountWithSettings = LogicalAccount & {
  order_settings?: OrderSettings;
  fund_settings?: FundSettings;
  /** 存的设置读不出来时的说明（此时对应的设置为空）：界面提示并禁止保存，不能按默认值显示 */
  order_settings_error?: string;
  fund_settings_error?: string;
};

interface SettingsResponse {
  ret_info: RetInfo;
  logical_account: LogicalAccountWithSettings;
}

export function updateLogicalAccountOrderSettings(logical_account_id: string, settings: OrderSettings) {
  return callTrade<{ logical_account_id: string; settings: OrderSettings }, SettingsResponse>(
    "console",
    "UpdateLogicalAccountOrderSettings",
    { logical_account_id, settings },
    { silent: true }
  );
}

export function updateLogicalAccountFundSettings(logical_account_id: string, settings: FundSettings) {
  return callTrade<{ logical_account_id: string; settings: FundSettings }, SettingsResponse>(
    "console",
    "UpdateLogicalAccountFundSettings",
    { logical_account_id, settings },
    { silent: true }
  );
}
