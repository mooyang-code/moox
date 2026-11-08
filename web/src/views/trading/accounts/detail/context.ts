import type { ComputedRef, InjectionKey, Ref } from "vue";
import type { LogicalAccount, LogicalAccountTarget, Order, TradingAccount } from "@/api/trade";
import type { Environment } from "../shared/environment";
import type { PositionSnapshot } from "../shared/position-snapshot";
import type { StrategyLink } from "../shared/strategy-link";
import type { ActionTracker } from "./action-tracker";
import type { CoinRow } from "./positions-model";

/** 下单对话框的预填：方向（1 买入、2 卖出）和数量。 */
export interface OrderPrefill {
  side?: 1 | 2;
  quantity?: string;
}

/** 详情页壳提供给各页签的数据和操作。 */
export interface DetailContext {
  logical: ComputedRef<LogicalAccount>;
  /** 本空间全部执行账户（含未加入组合的），添加成员时从里面选 */
  allAccounts: Ref<TradingAccount[]>;
  accounts: ComputedRef<Map<string, TradingAccount>>;
  env: ComputedRef<Environment>;
  target: Ref<LogicalAccountTarget | null>;
  targetExpired: ComputedRef<boolean>;
  snapshot: Ref<PositionSnapshot>;
  coinRows: ComputedRef<CoinRow[]>;
  strategy: Ref<StrategyLink | null>;
  tracker: ActionTracker;
  /** 订单、成交、操作记录的刷新计数：撤单、下单成功后加一，页签据此重新读取 */
  dataVersion: Ref<number>;
  /** 暂停自动执行后才能增删、改成员 */
  membersEditable: ComputedRef<boolean>;
  /** 清仓进行中：期间不能再下单或清仓 */
  flattenRunning: ComputedRef<boolean>;
  reload: () => Promise<void>;
  openOrder: (instrumentId?: string, accountId?: string, prefill?: OrderPrefill) => void;
  openFlatten: (account?: TradingAccount) => void;
  openCancel: (order: Order) => void;
  openClosePaper: (account: TradingAccount) => void;
  syncAccounts: (accounts: TradingAccount[]) => Promise<void>;
}

export const detailContextKey: InjectionKey<DetailContext> = Symbol("trading-account-detail");
