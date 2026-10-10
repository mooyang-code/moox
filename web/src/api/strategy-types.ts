export interface PageRequest {
  page?: number;
  page_size?: number;
}

export interface PageResponse {
  total: number;
  page?: number;
  page_size?: number;
}

export interface InstrumentTarget {
  instrument_id: string;
  target_weight: string;
}

/** 策略定义：一份 DSL 文本及其内容哈希。 */
export interface Strategy {
  strategy_id: string;
  name: string;
  dsl_yaml: string;
  dsl_hash: string;
  created_at: string;
  updated_at: string;
}

/** 实例：定义 + 一个 View + 可选的组合账户；启用时固化 resolved_json。 */
export interface StrategyInstance {
  instance_id: string;
  strategy_id: string;
  space_id: string;
  view_id: string;
  logical_account_id: string;
  enabled: boolean;
  session_id: string;
  resolved_json: string;
  health: "ok" | "degraded" | string;
  created_at: string;
  updated_at: string;
}

/** 一个处理过的周期：ok 带目标，skipped 带原因。 */
export interface StrategyResult {
  result_id: string;
  instance_id: string;
  session_id: string;
  bar_end_time: string;
  valid_until: string;
  status: "ok" | "skipped" | string;
  skip_reason: string;
  dsl_hash: string;
  targets: InstrumentTarget[];
  summary_json: string;
  input_json: string;
  rule_states_json: string;
  publish_status: "none" | "pending" | "sent" | "cancelled" | string;
  created_at: string;
}

/** 一条规则下一个标的的解释。 */
export interface StrategyResultItem {
  rule_id: string;
  instrument_id: string;
  stage: string;
  score: string;
  rank: number;
  weight: string;
  reason: string;
}

export interface StrategyResultDetail {
  result: StrategyResult;
  items: StrategyResultItem[];
  dsl_yaml: string;
  resolved_json: string;
}

export interface StrategyTargetSnapshot {
  targets: InstrumentTarget[];
  session_id: string;
  bar_end_time: string;
  valid_until: string;
  result_id: string;
}

export interface ValidateStrategyResult {
  diagnostics: string[];
  resolved_json: string;
  trial: StrategyResult | null;
  trial_items: StrategyResultItem[];
}

/** 基于 View 的研究回放任务。 */
export interface Replay {
  replay_id: string;
  strategy_id: string;
  dsl_yaml: string;
  space_id: string;
  view_id: string;
  start_time: string;
  end_time: string;
  fee_bps: number;
  status: "pending" | "running" | "done" | "failed" | "cancelled" | string;
  progress_time: string;
  metrics_json: string;
  error: string;
  created_at: string;
  updated_at: string;
  /** 被回放的 DSL 版本（内容哈希）。列表不带 dsl_yaml 全文，详情才有。 */
  dsl_hash: string;
  /** 从实例发起时的来源实例与固化 DSL 的会话。 */
  instance_id: string;
  session_id: string;
  /** 回放所用的日历（crypto_24x7 | cn_stock）：cn_stock 的区间按上海日期解释。 */
  calendar: string;
}

/** 回放中一个周期的记录；brief 列表不含目标、持仓与摘要 JSON。 */
export interface ReplayBar {
  bar_end_time: string;
  status: string;
  targets: InstrumentTarget[];
  positions_json: string;
  summary_json: string;
  bar_return: number;
  equity: number;
  turnover: number;
  fee: number;
  holdings: number;
  frozen: number;
  skip_reason: string;
  unfilled: number;
  liquidated: number;
}

/** strategy_id、dsl_yaml、instance_id 三选一。 */
export interface StartReplayRequest {
  strategy_id?: string;
  dsl_yaml?: string;
  instance_id?: string;
  view_id: string;
  start_time: string;
  end_time: string;
  fee_bps: number;
}

/** 发起回放的结果：任务与实际回放的根数、首末根 bar_end。 */
export interface StartedReplay {
  replay: Replay;
  bar_count: number;
  first_bar_end: string;
  last_bar_end: string;
}
