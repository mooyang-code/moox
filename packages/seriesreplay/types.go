// Package seriesreplay 编排逐期回放（设计第 9 节）：按时间顺序逐期推进历史数据，每期取因子帧（因子引擎）、
// 求策略决策（策略）、在纸面账本里撮合（Trade），得到接近实盘时序的模拟结果。
// 回放服务自己的 SQLite 按 (回放, 周期) 保存周期记录与各阶段的完成标记，中断后按标记补齐、继续推进。
package seriesreplay

import "encoding/json"

// Spec 是一次回放的规格。
type Spec struct {
	ReplayID string `json:"replay_id"`
	// SetID 是计算因子帧的因子集合，InstanceID 是提供策略定义与数据源绑定的策略实例。实例声明了 factors 时因子集合就是
	// 实例的私有因子集：SetID 可以省略（取实例的），写了必须与实例的一致。
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

// FactorInfo 是回放固定下来的一个因子成员。Member 是成员别名（同一定义可以用不同别名出现多次，参数各自调整）。
type FactorInfo struct {
	Member         string   `json:"member,omitempty"`
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
	// Params 是闭环回放这一期各成员的参数快照（成员别名 → 参数名 → 取值），由上一期的策略求值产出；没有的成员由因子引擎
	// 按冻结记录或默认值计算。
	Params MemberParams `json:"params,omitempty"`
}

// ParamValue 是一个参数一期的取值：统一值加按标的覆盖。
type ParamValue struct {
	Default   float64            `json:"default"`
	Overrides map[string]float64 `json:"overrides,omitempty"`
}

// MemberParams 是各成员的完整参数快照：成员别名 → 参数名 → 取值。
type MemberParams map[string]map[string]ParamValue

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
	// AppliedParams 是引擎本期实际生效的请求参数（不合法的成员已沿用上一期实际用的）；旧版引擎不返回，此时按请求的参数计。
	AppliedParams MemberParams `json:"applied_params,omitempty"`
	// RejectedParams 是本期被引擎拒绝的成员参数与原因。
	RejectedParams []ParamRejection `json:"rejected_params,omitempty"`
}

// ParamRejection 是被拒绝的成员参数：与实时提交一样，被拒绝的成员沿用上一期实际用的参数。
type ParamRejection struct {
	Member string `json:"member"`
	Reason string `json:"reason"`
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
	// FactorSetID 是声明了 factors 的实例的私有因子集；Tuned 为真表示策略逐期输出下一期的因子参数（闭环回放）。
	FactorSetID string `json:"factor_set_id,omitempty"`
	Tuned       bool   `json:"tuned,omitempty"`
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
	// NextParams 是策略这一期求出的下一期因子参数（只含求出参数的成员）；回放服务把它并进生效参数，下一期交给因子引擎。
	NextParams MemberParams `json:"next_params,omitempty"`
	// EffectiveParams 是到本期为止生效的参数：回放服务把 NextParams 按成员并入上一期的生效参数后保存，
	// 没有求出参数的成员沿用上一期（缺值沿用），续跑时据此恢复。
	EffectiveParams MemberParams `json:"effective_params,omitempty"`
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
