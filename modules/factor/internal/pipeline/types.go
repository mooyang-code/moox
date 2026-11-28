package pipeline

import (
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/serieswindow"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/packages/factorexpr/exec"
)

type Mode int

const (
	ModeLive Mode = iota + 1
	ModeRecalc
)

type Plan struct {
	Mode Mode
	Set  domain.FactorTask
	// Members 是本次计算的成员（成员键加定义）。
	Members []domain.MemberDef
	// Params 是本次计算各成员用的参数（成员键 → 快照）：实时是这一期取到的提交值，补算是一个参数段（段内各期快照相同）。
	// 没有的成员按成员自己的参数（固定参数，或可变成员的默认参数）计算。目标期的快照覆盖整个回看区间。
	Params map[string]domain.ParamSnapshot
	// BaseParams 是已有结果行的 __params（行 → 成员键 → 参数名 → 取值），补算与只填充时按它合并写回：
	// 只替换本次计算的成员，其余成员的记录保留。nil 表示没有已有行（实时写入整行重新生成）。
	BaseParams     map[RowKey]map[string]map[string]float64
	PeriodTime     time.Time // 实时模式的目标周期 T；补算为零值
	TargetStart    time.Time
	TargetEnd      time.Time
	Expected       []string
	Available      []string
	UpstreamFailed []string
	// FrozenUniverse 只用于补算：为真表示 Expected 是从周期 marker 读到的冻结全集（已按本批标的过滤）；
	// 为假表示 Expected 只是调用方选的标的，Run 先向数据层收窄（SubjectExpecter），排除区间结束时还没上市的标的。
	FrozenUniverse bool
	CarryColumns   []string
	WriteCarry     bool
	Budget         time.Duration
	// FillOnly 只填充：窗口检查通过才计算并写入，有洞、失败都不写任何字段（尤其不写失效 NULL），也不写 carry 列。
	// 用于空洞补齐后的恢复。
	FillOnly bool
	// HistorySubjects 只在有成员写了 cross_section_history: observed 时才有：本地存储里在读取区间内有数据、但不在 Available
	// （也不是上游失败）的标的，例如已下架或移出全集的标的。它们只读本地历史、不同步、不输出结果，只放进面板参与 observed
	// 成员的历史截面；由 Run 在内存预检之前向数据层取得（HistorySource）。
	HistorySubjects []string
}

// Observed 报告计划里是否有成员写了 cross_section_history: observed。
func (p Plan) Observed() bool {
	for _, member := range p.Members {
		if member.Factor.ObservedHistory() {
			return true
		}
	}
	return false
}

// RowKey 标识结果数据集里的一行：标的、时间与序列标签。
type RowKey struct {
	Subject string
	At      time.Time
	Tag     string
}

type Outcome struct {
	Status         string
	FailedSubjects []string
	Factors        []storageio.FactorState
	RowsWritten    int
	Warming        int
	StageDurations map[string]time.Duration
	// Items 是补算与填充的逐 (因子, 标的, 周期) 结果；实时周期不填。
	Items map[ItemKey]ItemStatus
	// DefaultFilled 是补算里按默认参数新补的期次（成员键 → 期次）：已有行不存在、或 __params 里没有这个成员。
	// 由补算执行器填写，管线本身不知道。
	DefaultFilled map[string][]time.Time
	// Warnings 是成员在目标区间内整截面没有算出的次数（成员键 → 原因 → 截面数），例如截面回归奇异（cs_resid_singular）、
	// 组数超限（gs_too_many_groups）。这些截面的值为 NaN、照常写出，不改变成员状态；引擎按原因计入失败指标。
	Warnings map[string]map[string]int
}

type Config struct {
	WriteBatchRows    int
	WriteRetries      int
	WriteRetryBackoff time.Duration
	// ComputeWorkers 是一次程序执行的并行度（设计 7.2）。
	ComputeWorkers int
	// MaxMemory 是一次计算估算峰值内存的上限（字节，设计 7.5）；超限时本期全部因子按计算失败处理。0 表示不检查。
	// 补算分块在读取之前已按同一估算收窄（recalcexec.WithMemoryBudget）。
	MaxMemory int64
	// OnCompileError 在引擎侧编译定义失败时调用（指标 factor_compile_errors_total）；nil 不记录。
	OnCompileError func(taskID, factorID string)
	// MemoryAdmission 非空时，每次计算先向它预留估算的内存（设计 4.4），预留不到就排队，超时按计算失败处理。
	MemoryAdmission Admission
	// AdmissionTimeout 是等待内存预留的最长时间；0 表示不超时（受 ctx 限制）。
	AdmissionTimeout time.Duration
	// UDF 是自定义算子的执行宿主；定义里没有 udf. 调用时不需要。
	UDF exec.UDFHost
	// GapSink 接收每次本期没有算的记录（账本）：上游失败、窗口还没载入完成、本期数据取不到；nil 不记录。
	GapSink GapSink
}

type Runner struct {
	store storageio.Store
	data  Data
	clock periodclock.Clock
	cfg   Config
	// compiled 缓存引擎侧编译好的定义，键为 (factor_id, spec)。
	compiled sync.Map
}

func NewRunner(store storageio.Store, data Data, clock periodclock.Clock, cfg Config) *Runner {
	if cfg.WriteBatchRows <= 0 {
		cfg.WriteBatchRows = 1000
	}
	if cfg.WriteRetries <= 0 {
		cfg.WriteRetries = 3
	}
	if cfg.ComputeWorkers <= 0 {
		cfg.ComputeWorkers = 1
	}
	return &Runner{store: store, data: data, clock: clock, cfg: cfg}
}

type LoadResult struct {
	// Frames 是目标区间的行（series_tag 与携带列）：组装写出携带列用，不含回看窗口。
	Frames map[string]*storageio.Frame
	// View 是计算的列式输入：回看窗口加目标区间。
	View *serieswindow.View
	// Available、FailedSubjects 是期望标的里本期可用与不可用的标的（可用：本期有真实的源行；
	// 不可用：上游失败、本期数据还没到、同步失败、预热中）。
	Available      []string
	FailedSubjects []string
	// WarmingSubjects are left out of the period until their lookback window
	// is loaded; they are also listed in FailedSubjects.
	WarmingSubjects []string
}

type Computation struct {
	// CrossPeriods 记录截面因子每个目标周期的结果：补算一次计算多个周期，块级的失败名单推不出各周期的结果。
	CrossPeriods map[string]map[int64]periodOutcome
	Results      map[string]map[string]ItemResult
	FactorStates map[string]storageio.FactorState
	// Failures 记录成员整体计算失败的原因（补算模式下截面因子的逐行结果不带错误，原因放在这里供试算等调用方报告）。
	Failures  map[string]error
	BudgetHit bool
	// Warnings 是成员在目标区间内整截面没有算出的次数（成员键 → 原因 → 截面数），来自 exec.Result.Warnings。
	Warnings map[string]map[string]int
}

// ItemResult 是一个成员在一个标的（截面因子为空串）上的逐行结果：列为 data_time、series_tag、
// （截面因子）subject_id 与各输出列（结果数据集里的列名），值为 null 或 float64；Err 非空表示计算失败。
type ItemResult struct {
	Member  string
	Columns []string
	Rows    [][]any
	Err     error
}

// periodOutcome 是截面因子一个目标周期的结果。
type periodOutcome string

const (
	periodOK     periodOutcome = "ok"
	periodGap    periodOutcome = "gap"
	periodFailed periodOutcome = "failed"
)

// ItemKey 标识补算或填充里的一个 (成员, 标的, 周期)。
type ItemKey struct {
	Member  string
	Subject string
	Period  time.Time
}

// ItemStatus 是一个 (因子, 标的, 周期) 的结果：written 已取得有效值并写入，failed 计算错误、无效值或没有源行。
type ItemStatus string

const (
	ItemWritten ItemStatus = "written"
	ItemFailed  ItemStatus = "failed"
)
