package pipeline

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/serieswindow"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/packages/seriesset"
)

// Data 是管线的数据层：本地时序存储、内存窗口和与 Storage 的同步（engine 里的 localdata 实现）。
// 管线只通过它取得列式输入，不再从 Storage 读整窗口。
type Data interface {
	// Load 为一个计划准备列式输入与目标区间的行。
	//   - 实时：先把 plan.Available 标的同步到本期（只拉增量），再抓取截至本期的读租约；
	//   - 补算：从本地存储读 [回看起点, TargetEnd)。
	Load(ctx context.Context, plan Plan) (DataResult, error)
	// WindowStatuses 返回各任务内存窗口的载入状态。
	WindowStatuses() []WarmupStatus
}

// SeriesExpecter 是数据层的可选能力：为拿不到冻结全集的补算类计划（人工补算、失效重算，以及读不到周期 marker 的空洞恢复）
// 构造统一数据集（源数据集属性 market_type=multi）的期望序列。能读到周期 marker 的恢复路径直接用冻结的 universe_series，不走这里。
// 这里改用本地存储里“应存在”的（标的、series_tag）：序列在计划区间结束之前已有数据、并且没有下架就是应存在——
// 下架看它最后一行相对数据源最新一根沉默了多久（超过 max(6 小时, 2×周期) 才算下架），与回看根数、give_up_after 无关；
// 区间内断档、最近一期晚到的序列仍算应存在。这是推测（Plan.ExpectedSeriesDerived）：非只填充计划里因推测缺数被跳过的 tag 不写 NULL。
// 单序列数据集返回 nil，计划保持按标的处理。
type SeriesExpecter interface {
	ExpectedSeries(ctx context.Context, plan Plan) ([]seriesset.Series, error)
}

// DataResult 是 Data.Load 的结果。
type DataResult struct {
	// View 是计算的列式输入。
	View *serieswindow.View
	// Frames 是目标区间的行（data_time、series_tag、携带列），供组装使用。
	Frames map[string]*storageio.Frame
	// Failed 是本期数据取不到的标的：同步失败，或同步之后本地仍没有目标周期的行（数据还没到）。
	Failed []string
	// Warming 是窗口还没载入完成、本期没有算的标的。
	Warming []string
	// FailedSeries 是统一数据集（plan.ExpectedSeries 非空）上本期取不到的序列：标的本身没有失败，但这条序列没有真实的源行
	// （例如永续比现货晚到）。Failed 里的标的整体失败（它的全部序列都不可用），不再重复列在这里。
	FailedSeries []seriesset.Series
}

// Admission 是进程级内存准入（设计 4.4）：计算在开始前预留估算的内存，用完释放。
type Admission interface {
	// Reserve 预留 bytes 字节，预留不到时等待；ctx 结束返回错误。返回的函数释放预留。
	Reserve(ctx context.Context, bytes int64) (release func(), err error)
}

// Warm-up states of a live factor task. 窗口载入完成之前，该任务的标的本期都不算（记入账本，载入后由跳过恢复补算）。
const (
	WarmupWarming = "warming"
	WarmupReady   = "ready"
)

// WarmupStatus reports how much of a live set's universe has its lookback
// window loaded. Results of warming subjects are not formal: they are reported
// as failed subjects, so strategies holding them do not evaluate the period.
type WarmupStatus struct {
	TaskID           string
	State            string
	WarmSubjects     int
	ExpectedSubjects int
}

// SkipReasonWarming 是账本里「窗口还没载入完成」被跳过的原因：本期没有算。
const SkipReasonWarming = "warming"

// SkipReasonUpstream 是实时周期里标的本期数据取不到（上游截止时还没到、或同步失败）而没有算的标的：
// 补采补齐后由只填充补算补上。
const SkipReasonUpstream = "upstream_failed"

// GapEntry 是写入账本的一条跳过记录：一个标的、一个周期、一个成员。
type GapEntry struct {
	TaskID         string
	Subject        string
	Period         time.Time
	Member         string
	DefinitionHash string
	// TriggerEventID 是触发本期的上游事件，截面因子的恢复据此读取冻结全集；人工补算为空。
	TriggerEventID string
	Reason         string
}

// GapSink 接收每次跳过的记录（引擎的永久账本）。写入失败不影响周期推进，调用方记日志与指标。
type GapSink interface {
	RecordGaps(ctx context.Context, entries []GapEntry) error
}
