package pipeline

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/serieswindow"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
)

// Data 是管线的数据层：本地时序存储、内存窗口和与 Storage 的同步（engine 里的 localdata 实现）。
// 管线只通过它取得列式输入，不再从 Storage 读整窗口。
type Data interface {
	// Load 为一个计划准备列式输入与目标区间的行。
	//   - 实时：先把 plan.Available 标的同步到本期（只拉增量），再抓取截至本期的读租约；
	//   - 补算：从本地存储读 [回看起点, TargetEnd)。
	Load(ctx context.Context, plan Plan) (DataResult, error)
	// WindowStatuses 返回各集合内存窗口的载入状态。
	WindowStatuses() []WarmupStatus
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
}

// Admission 是进程级内存准入（设计 4.4）：计算在开始前预留估算的内存，用完释放。
type Admission interface {
	// Reserve 预留 bytes 字节，预留不到时等待；ctx 结束返回错误。返回的函数释放预留。
	Reserve(ctx context.Context, bytes int64) (release func(), err error)
}

// Warm-up states of a live factor set. 窗口载入完成之前，该集合的标的本期都不算（记入账本，载入后由跳过恢复补算）。
const (
	WarmupWarming = "warming"
	WarmupReady   = "ready"
)

// WarmupStatus reports how much of a live set's universe has its lookback
// window loaded. Results of warming subjects are not formal: they are reported
// as failed subjects, so strategies holding them do not evaluate the period.
type WarmupStatus struct {
	SetID            string
	State            string
	WarmSubjects     int
	ExpectedSubjects int
}

// SkipReasonWarming 是账本里「窗口还没载入完成」被跳过的原因：本期没有算。
const SkipReasonWarming = "warming"

// SkipReasonUpstream 是实时周期里标的本期数据取不到（上游截止时还没到、或同步失败）而没有算的标的：
// 补采补齐后由只填充补算补上。
const SkipReasonUpstream = "upstream_failed"

// GapEntry 是写入账本的一条跳过记录：一个标的、一个周期、一个因子。
type GapEntry struct {
	SetID          string
	Subject        string
	Period         time.Time
	FactorID       string
	DefinitionHash string
	// TriggerEventID 是触发本期的上游事件，截面因子的恢复据此读取冻结全集；人工补算为空。
	TriggerEventID string
	Reason         string
}

// GapSink 接收每次跳过的记录（引擎的永久账本）。写入失败不影响周期推进，调用方记日志与指标。
type GapSink interface {
	RecordGaps(ctx context.Context, entries []GapEntry) error
}
