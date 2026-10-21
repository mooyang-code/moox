// Package seriesreplay 编排逐期回放（设计第 9 节）：按时间顺序逐期推进历史数据，每期取因子帧（因子引擎）、
// 求策略决策（策略）、在纸面账本里撮合（Trade），得到接近实盘时序的模拟结果。
// 回放服务自己的 SQLite 按 (回放, 周期) 保存周期记录与各阶段的完成标记，中断后按标记补齐、继续推进。
package seriesreplay

import "encoding/json"

// Spec 是一次回放的规格。
type Spec struct {
	ReplayID string `json:"replay_id"`
	// SetID 是计算因子帧的因子集合，InstanceID 是提供策略定义与数据源绑定的策略实例。
	SetID      string `json:"set_id"`
	InstanceID string `json:"instance_id"`
	// From、To 是回放区间（RFC3339，左闭右开），决策周期是 data_time 落在其中的每一根 K 线。
	From string `json:"from"`
	To   string `json:"to"`
	// Quote 是成交价取法：next_open（默认，本期收盘决策、下一根开盘成交）或 close。
	Quote string `json:"quote,omitempty"`
	// WriteFactors 为真时每期把因子结果写回结果数据集（与区间重算相同的写法，不发周期完成事件）。
	WriteFactors bool `json:"write_factors,omitempty"`
	// AllowShortWarmup 允许多数标的的本地数据不够预热区间（不加时拒绝，因子前段会是空值）。
	AllowShortWarmup bool `json:"allow_short_warmup,omitempty"`
	// InitialCash、FeeRate、SlippageBPS 是纸面账本的初始资金与费用配置（空取账本默认值）。
	InitialCash string `json:"initial_cash,omitempty"`
	FeeRate     string `json:"fee_rate,omitempty"`
	SlippageBPS string `json:"slippage_bps,omitempty"`
}

// 任务状态。
const (
	StatusRunning   = "running"
	StatusPaused    = "paused"
	StatusCancelled = "cancelled"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
)

// ---------------------------------------------------------------- 因子引擎

// FactorOpenRequest 是 POST /v1/replay/open 的请求体。
type FactorOpenRequest struct {
	ReplayID     string `json:"replay_id"`
	SetID        string `json:"set_id"`
	From         string `json:"from"`
	To           string `json:"to"`
	ExtraPeriods int    `json:"extra_periods"`
	Quote        string `json:"quote,omitempty"`
	WriteFactors bool   `json:"write_factors,omitempty"`
	// AllowShortWarmup 允许多数标的的本地数据不够预热区间。
	AllowShortWarmup bool `json:"allow_short_warmup,omitempty"`
}

// FactorInfo 是回放固定下来的一个因子。
type FactorInfo struct {
	FactorID       string   `json:"factor_id"`
	DefinitionHash string   `json:"definition_hash"`
	Outputs        []string `json:"outputs"`
}

// FactorExcluded 是被排除出回放的标的及原因。
type FactorExcluded struct {
	Subject string `json:"subject"`
	Reason  string `json:"reason"`
}

// FactorOpenResponse 是建立会话的结果。
type FactorOpenResponse struct {
	ReplayID string           `json:"replay_id"`
	Subjects []string         `json:"subjects"`
	Excluded []FactorExcluded `json:"excluded,omitempty"`
	Periods  int              `json:"periods"`
	Start    string           `json:"start"`
	StepSecs int64            `json:"step_seconds"`
	Bytes    int64            `json:"bytes"`
	Factors  []FactorInfo     `json:"factors"`
	Fields   []string         `json:"fields"`
	// ShortWarmup 是本地数据起点晚于预热起点的标的数（因子前段为空值）。
	ShortWarmup  int      `json:"short_warmup,omitempty"`
	ShortSamples []string `json:"short_samples,omitempty"`
}

// FactorStepRequest 是 POST /v1/replay/step 的请求体。
type FactorStepRequest struct {
	ReplayID      string   `json:"replay_id"`
	Period        string   `json:"period"`
	SourceColumns []string `json:"source_columns,omitempty"`
	WriteFactors  bool     `json:"write_factors,omitempty"`
}

// FactorSubject 是一个标的本期的因子帧。
type FactorSubject struct {
	Source      map[string]float64            `json:"source"`
	SourcePrev  map[string]float64            `json:"source_prev,omitempty"`
	Factors     map[string]map[string]float64 `json:"factors,omitempty"`
	FactorsPrev map[string]map[string]float64 `json:"factors_prev,omitempty"`
	Close       float64                       `json:"close"`
	Exec        float64                       `json:"exec"`
	AgeBars     int                           `json:"age_bars"`
}

// FactorState 是一个因子本期的状态。
type FactorState struct {
	Status         string   `json:"status"`
	DefinitionHash string   `json:"definition_hash"`
	FailedSubjects []string `json:"failed_subjects,omitempty"`
}

// FactorStepResponse 是一期的因子帧。
type FactorStepResponse struct {
	ReplayID      string                   `json:"replay_id"`
	Period        string                   `json:"period"`
	DecisionAt    string                   `json:"decision_at"`
	Subjects      map[string]FactorSubject `json:"subjects"`
	States        map[string]FactorState   `json:"factor_states"`
	Rows          int                      `json:"rows,omitempty"`
	ComputeMillis int64                    `json:"compute_ms"`
}

// ---------------------------------------------------------------- 策略

// StrategyBinding 是策略固定下来的版本与因子绑定。
type StrategyBinding struct {
	InstanceID    string            `json:"instance_id"`
	StrategyID    string            `json:"strategy_id"`
	SpaceID       string            `json:"space_id"`
	ViewID        string            `json:"view_id"`
	DSLHash       string            `json:"dsl_hash"`
	Bar           string            `json:"bar"`
	Calendar      string            `json:"calendar"`
	Spot          bool              `json:"spot"`
	MinAgeBars    int               `json:"min_age_bars,omitempty"`
	Factors       map[string]string `json:"factors"`
	SourceColumns []string          `json:"source_columns"`
	PreviousDepth int               `json:"previous_depth"`
}

// StrategyEvaluateRequest 是 POST /v1/replay/evaluate 的请求体。
type StrategyEvaluateRequest struct {
	ReplayID     string                 `json:"replay_id"`
	InstanceID   string                 `json:"instance_id"`
	DSLHash      string                 `json:"dsl_hash"`
	Period       string                 `json:"period"`
	Rows         map[string]StrategyRow `json:"rows"`
	FactorStates map[string]FactorState `json:"factor_states"`
	State        json.RawMessage        `json:"state,omitempty"`
}

// StrategyRow 是一个标的本期的输入。
type StrategyRow struct {
	Source      map[string]float64            `json:"source"`
	SourcePrev  map[string]float64            `json:"source_prev,omitempty"`
	Factors     map[string]map[string]float64 `json:"factors,omitempty"`
	FactorsPrev map[string]map[string]float64 `json:"factors_prev,omitempty"`
	AgeBars     int                           `json:"age_bars"`
}

// StrategyTarget 是决策里的一个目标。
type StrategyTarget struct {
	InstrumentID string `json:"instrument_id"`
	TargetWeight string `json:"target_weight"`
	Hold         bool   `json:"hold,omitempty"`
}

// StrategyDecision 是本期决策。
type StrategyDecision struct {
	Status     string           `json:"status"`
	SkipReason string           `json:"skip_reason,omitempty"`
	Detail     string           `json:"detail,omitempty"`
	Targets    []StrategyTarget `json:"targets"`
	State      json.RawMessage  `json:"state"`
	Summary    json.RawMessage  `json:"summary,omitempty"`
	BarEnd     string           `json:"bar_end"`
	DSLHash    string           `json:"dsl_hash"`
}

// ---------------------------------------------------------------- Trade

// TradeCreateRequest 是建立纸面账本会话的参数。
type TradeCreateRequest struct {
	ReplayID    string `json:"replay_id"`
	InitialCash string `json:"initial_cash,omitempty"`
	FeeRate     string `json:"fee_rate,omitempty"`
	SlippageBPS string `json:"slippage_bps,omitempty"`
}

// TradeSession 是账本会话的状态。
type TradeSession struct {
	ReplayID    string            `json:"replay_id"`
	Status      string            `json:"status"`
	InitialCash string            `json:"initial_cash"`
	FeeRate     string            `json:"fee_rate"`
	SlippageBPS string            `json:"slippage_bps"`
	Cash        string            `json:"cash"`
	Positions   map[string]string `json:"positions"`
	LastPeriod  int64             `json:"last_period"`
}

// TradeQuote 是一个标的本期的回放报价。
type TradeQuote struct {
	Mark string `json:"mark,omitempty"`
	Exec string `json:"exec,omitempty"`
}

// TradeDecision 是发给账本的决策。
type TradeDecision struct {
	Kind    string           `json:"kind"`
	Targets []StrategyTarget `json:"targets,omitempty"`
}

// TradeAdvanceRequest 是 ReplayAdvance。
type TradeAdvanceRequest struct {
	ReplayID string                `json:"replay_id"`
	Period   int64                 `json:"period"`
	Decision TradeDecision         `json:"decision"`
	Quotes   map[string]TradeQuote `json:"quotes"`
}

// TradeOrder 是本期的一笔模拟订单。
type TradeOrder struct {
	OrderID           string `json:"order_id"`
	InstrumentID      string `json:"instrument_id"`
	Side              string `json:"side"`
	RequestedQuantity string `json:"requested_quantity"`
	FilledQuantity    string `json:"filled_quantity"`
	Price             string `json:"price"`
	Fee               string `json:"fee"`
	Status            string `json:"status"`
	Reason            string `json:"reason,omitempty"`
}

// TradeHolding 是期末的一个持仓。
type TradeHolding struct {
	InstrumentID string `json:"instrument_id"`
	Quantity     string `json:"quantity"`
	Mark         string `json:"mark"`
	Value        string `json:"value"`
}

// TradeResult 是一期推进的确认。
type TradeResult struct {
	ReplayID     string         `json:"replay_id"`
	Period       int64          `json:"period"`
	Kind         string         `json:"kind"`
	EquityBefore string         `json:"equity_before"`
	Equity       string         `json:"equity"`
	Cash         string         `json:"cash"`
	Fees         string         `json:"fees"`
	Turnover     string         `json:"turnover"`
	Holdings     []TradeHolding `json:"holdings"`
	Orders       []TradeOrder   `json:"orders"`
	Replayed     bool           `json:"replayed,omitempty"`
}
