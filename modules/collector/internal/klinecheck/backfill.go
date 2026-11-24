package klinecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	// DefaultMaxInFlight 是每个空间同时执行的补采分块数上限，与补采函数的预留并发一致（SCF 执行端由平台硬性限制）。
	DefaultMaxInFlight = 5
	// LocalMaxInFlight 是全部数据集都用本地执行端（collector-stream）补采的空间的并发上限：不受 SCF 预留并发限制，
	// 只受行情源 REST 限频约束（补采分块很小，权重低）。
	LocalMaxInFlight = 12
	// MaxChunkBars 是一个分块最多拉取的 K 线数。
	MaxChunkBars = 1000
	// MaxChunksPerJob 防止一次作业展开出无法完成的分块数。
	MaxChunksPerJob = 20000
	// autoScanInterval 是自动补采的扫描间隔；autoScanOverlap 是每次扫描向前重叠的时间。
	// 高频小窗口：每次只检查最近几分钟，请求量小、链路占用短，缺口也能很快发现。
	autoScanInterval = 5 * time.Minute
	autoScanOverlap  = 5 * time.Minute
	// autoScanOffset 把扫描错开到每个 5 分钟槽开始后 2 分 30 秒之后，避开整点、整 5 分钟与每分钟开头的采集高峰。
	autoScanOffset = 150 * time.Second
	// autoScanWindow 是一次扫描单个数据集的最大跨度（落后时分多次追赶）：窗口过大（例如 24 小时的 1 分钟线约
	// 70 万行）会在公网链路上持续数分钟占满带宽，拖慢同一时段的策略与事件投递，读超时又使水位无法推进、
	// 下一轮重复同样的大扫描。
	autoScanWindow    = time.Hour
	autoMaxChunks     = 2000
	maxChunkAttempts  = 5
	chunkInvokeLimit  = 90 * time.Second
	chunkSafetyMargin = 30 * time.Second
)

// JobRequest 是补采作业的范围。
type JobRequest struct {
	SpaceID   string
	DatasetID string
	SubjectID string
	From      time.Time
	To        time.Time
	// IncludeOpenBar 见 Request.IncludeOpenBar：恢复追赶的范围从暂停那一刻开始，要包含当时正在走的那根 K 线。
	IncludeOpenBar bool
	// Force 为 true 时不检查缺口，范围内应有的 K 线全部重拉。
	Force bool
	// Watermark 非空表示这是自动补采作业：作业成功后才把该数据集的扫描水位推进到 To。
	Watermark *WatermarkTarget
}

// WatermarkTarget 标识自动补采作业成功后要推进水位的数据集与频率。
type WatermarkTarget struct {
	DatasetID string
	Frequency string
}

// BackfillStore 是补采需要的持久化接口，由 *store.BackfillRepository 实现。
type BackfillStore interface {
	CreateJob(context.Context, store.BackfillJob, []store.BackfillChunk) error
	GetJob(context.Context, string) (*store.BackfillJob, []store.BackfillChunk, error)
	ActiveJobs(context.Context, string) ([]store.BackfillJob, error)
	ListJobs(context.Context, string, store.BackfillJobFilter, int, int) ([]store.BackfillJob, int64, error)
	SetJobStatus(context.Context, string, string, string) error
	CancelJob(context.Context, string) error
	RetryJob(context.Context, string) (int, error)
	ClaimChunks(context.Context, string, int, time.Time) ([]store.BackfillChunk, error)
	InFlightChunks(context.Context, string) (int, error)
	FinishChunk(context.Context, int64, int, string, int, string) error
	RequeueChunk(context.Context, int64, int, string, string) error
	RenewChunk(context.Context, int64, int, time.Time) error
	ReleaseChunk(context.Context, int64, int) error
	ChunkStatusCounts(context.Context, string) (map[string]int, error)
	ChunkStatusCountsOf(context.Context, []string) (map[string]map[string]int, error)
	Watermark(context.Context, string, string, string) (time.Time, error)
	SetWatermark(context.Context, string, string, string, time.Time) error
	AddAbsences(context.Context, []store.SourceAbsence) error
	AbsencesIn(context.Context, string, string, string, []string, time.Time, time.Time) ([]store.SourceAbsence, error)
	ClearAbsences(context.Context, string, string, string) (int64, error)
	HistoryScans(context.Context, string) (map[string]store.HistoryScan, error)
	SaveHistoryScan(context.Context, store.HistoryScan) error
}

// RouteSource 由当前的采集任务给出补采数据集（某个频率）用的来源、拉取参数与输出字段，由 *fetchplan.BackfillRoutes 实现。
// 返回的计划只用 Market、Fetch 与 Datasets 里这个数据集的输出字段；没有写这个数据集的任务（已删除）时 found 为假。
type RouteSource interface {
	BackfillRoute(ctx context.Context, spaceID, datasetID, frequency string) (plan *marketfetch.Plan, found bool, err error)
}

// FunctionProvider 保证补采函数存在并返回函数名。
type FunctionProvider interface {
	EnsureBackfillFunction(ctx context.Context, marketType string) (string, error)
}

// Invoker 同步调用补采执行端（云函数或本地执行端）并等它返回。
type Invoker interface {
	Invoke(ctx context.Context, spaceID, functionName string, event map[string]any) (marketfetch.InvocationResult, error)
}

// Backfiller 管理补采作业：展开分块、通过补采函数执行、复查缺口。
type Backfiller struct {
	// Refill 为 true 时，分块执行完立刻补位认领下一块（生产开启）；为 false 时只在调度节拍上认领。
	Refill bool

	Checker *Checker
	Store   BackfillStore
	// Routes 给出补采用的来源与参数（取当前任务）；交易所符号按 Checker.Timeline 取分块起点那一段的。
	Routes    RouteSource
	Invoker   Invoker
	Functions func(spaceID string) FunctionProvider
	// Executor 返回数据集的补采执行端（scf 或 local），为空时按 scf；Local 是本地执行端（collector-stream 里的 MarketFetchExecutor）。
	Executor func(ctx context.Context, spaceID, datasetID string) (string, error)
	Local    Invoker
	// FlushNotices 在补采作业结束后调用：把分块成功时记下的补写通知封存成 event.collector.kline.backfilled
	// （因子据此立即推进连续水位并补算，合成据此重算）。失败只记日志，通知留在库里，由维护轮（合成开启时还有合成的定时器）再封存。
	FlushNotices func(ctx context.Context) error
	// Paused 报告空间是否处于采集暂停；暂停期间不再认领分块（含补位），已在执行的分块自然结束。为空表示不暂停。
	Paused func(ctx context.Context, spaceID string) (bool, error)
	// BaseCtx 是异步执行分块的上下文（进程生命周期）。
	BaseCtx     context.Context
	MaxInFlight int
	// MaxInFlightFor 非空时按空间决定并发上限（返回值不大于 0 表示用 MaxInFlight / 默认值）。
	MaxInFlightFor func(ctx context.Context, spaceID string) int
	Now            func() time.Time
	// CloseRefetch 非空时启用收市后整天重取（见 RefetchAfterClose）：空间的重取时刻落在 (after, until] 之内时
	// 返回 due 为真和当天整天的范围 [from, to)（K 线开始时刻）。不需要重取的市场返回 due 为假。
	CloseRefetch func(spaceID string, after, until time.Time) (from, to time.Time, due bool, err error)
	// RefetchFrequency 非空时，收市后整天重取只对它返回真的周期提交（市场的数据源声明支持的周期）。
	RefetchFrequency func(spaceID, frequency string) bool

	mu       sync.Mutex
	lastScan map[string]time.Time
	// refetchedDay 是每个空间最近一次已提交收市后整天重取的那天（范围起点），只在内存里，防止同一轮里重复提交。
	refetchedDay map[string]time.Time
	// lastHistoryRun 是每个空间最近一次历史缺口扫描的时间（见 ScanHistory）；每个数据集的扫描记录保存在库里。
	lastHistoryRun map[string]time.Time
	workers        sync.WaitGroup
	// tickMu 串行化认领，避免补位与调度节拍同时超出并发上限。
	tickMu sync.Mutex
}

func (b *Backfiller) now() time.Time {
	if b.Now != nil {
		return b.Now().UTC()
	}
	return time.Now().UTC()
}

func (b *Backfiller) maxInFlight(ctx context.Context, spaceID string) int {
	if b.MaxInFlightFor != nil {
		if n := b.MaxInFlightFor(ctx, spaceID); n > 0 {
			return n
		}
	}
	if b.MaxInFlight > 0 {
		return b.MaxInFlight
	}
	return DefaultMaxInFlight
}

// Wait 等待所有异步分块结束（进程退出时调用）。
func (b *Backfiller) Wait() { b.workers.Wait() }

// splitGap 把一段缺口拆成每块至多 MaxChunkBars 根的分块。
func splitGap(gap Gap, frequency marketdata.Frequency) [][2]time.Time {
	var parts [][2]time.Time
	for from := gap.From; from.Before(gap.To); {
		to := advanceBars(frequency, from, MaxChunkBars)
		if to.After(gap.To) {
			to = gap.To
		}
		parts = append(parts, [2]time.Time{from, to})
		from = to
	}
	return parts
}

// chunkID 是分块的稳定标识。
func chunkID(jobID, datasetID, frequency, subject string, from time.Time) string {
	return fmt.Sprintf("bf|%s|%s|%s|%s|%d", jobID, datasetID, frequency, subject, from.Unix())
}

// Check 检查范围内的缺口。
func (b *Backfiller) Check(ctx context.Context, request Request) (*Result, error) {
	return b.Checker.Check(ctx, request)
}

// Submit 创建补采作业：先找出缺口（Force 时为全部应有 K 线），拆成分块并保存。返回作业 ID 与分块数。
func (b *Backfiller) Submit(ctx context.Context, request JobRequest, source string) (string, int, error) {
	return b.submit(ctx, request, source, MaxChunksPerJob)
}

// errTooManyChunks 表示范围展开的分块数超过上限，调用方可缩小范围重试。
var errTooManyChunks = fmt.Errorf("the range expands to too many chunks")

func (b *Backfiller) submit(ctx context.Context, request JobRequest, source string, maxChunks int) (string, int, error) {
	if b == nil || b.Checker == nil || b.Store == nil {
		return "", 0, fmt.Errorf("backfiller is not initialized")
	}
	result, err := b.Checker.Check(ctx, Request{SpaceID: request.SpaceID, DatasetID: request.DatasetID, SubjectID: request.SubjectID, From: request.From, To: request.To, AssumeMissing: request.Force, BeforeFirstPlan: request.Force, IncludeOpenBar: request.IncludeOpenBar})
	if err != nil {
		return "", 0, err
	}
	if result.Truncated {
		return "", 0, fmt.Errorf("%w: more than %d gaps", errTooManyChunks, MaxGaps)
	}
	gaps, err := b.gapsWithTask(ctx, request, result.Gaps)
	if err != nil || len(gaps) == 0 {
		return "", 0, err
	}
	job := store.BackfillJob{SpaceID: request.SpaceID, Source: source, Kind: store.BackfillKindGap, DatasetID: request.DatasetID, SubjectID: request.SubjectID, From: request.From, To: request.To}
	if request.Force {
		job.Kind = store.BackfillKindForce
	}
	if request.Watermark != nil {
		job.DatasetID, job.Frequency, job.AdvanceWatermark = request.Watermark.DatasetID, request.Watermark.Frequency, true
	}
	return b.createJob(ctx, job, gaps, maxChunks)
}

// gapsWithTask 只留下仍有采集任务的数据集的缺口：补采的来源、参数取当前任务，任务删除后不能补采
// （时间线里停止采集的历史段仍在，不过滤的话会把它们列成缺口，建出的分块逐个失败）。
// 指定了数据集而它没有任务时明确拒绝；不指定数据集时跳过这些数据集。
func (b *Backfiller) gapsWithTask(ctx context.Context, request JobRequest, gaps []Gap) ([]Gap, error) {
	if b.Routes == nil {
		return nil, fmt.Errorf("backfill routes are not configured")
	}
	found := map[string]bool{}
	var kept []Gap
	for _, gap := range gaps {
		key := DatasetKey(gap.DatasetID, gap.Frequency)
		ok, seen := found[key]
		if !seen {
			_, exists, err := b.Routes.BackfillRoute(ctx, request.SpaceID, gap.DatasetID, gap.Frequency)
			if err != nil {
				return nil, err
			}
			ok, found[key] = exists, exists
			if !exists && request.DatasetID != "" {
				return nil, fmt.Errorf("数据集 %s（%s）没有对应的采集任务（任务已删除），不能补采", gap.DatasetID, gap.Frequency)
			}
		}
		if ok {
			kept = append(kept, gap)
		}
	}
	return kept, nil
}

// createJob 把缺口拆成分块并保存作业，返回作业 ID 与分块数。作业没写频率时，若全部缺口属于同一频率就补上。
func (b *Backfiller) createJob(ctx context.Context, job store.BackfillJob, gaps []Gap, maxChunks int) (string, int, error) {
	jobID := uuid.NewString()
	job.JobID = jobID
	frequencies := map[string]bool{}
	var chunks []store.BackfillChunk
	for _, gap := range gaps {
		frequencies[gap.Frequency] = true
		frequency, err := marketdata.ParseFrequency(gap.Frequency)
		if err != nil {
			return "", 0, err
		}
		for _, part := range splitGap(gap, frequency) {
			chunks = append(chunks, store.BackfillChunk{DatasetID: gap.DatasetID, Frequency: gap.Frequency, SubjectID: gap.SubjectID, From: part[0], To: part[1], ChunkID: chunkID(jobID, gap.DatasetID, gap.Frequency, gap.SubjectID, part[0])})
		}
	}
	if len(chunks) > maxChunks {
		return "", 0, fmt.Errorf("%w: %d chunks (limit %d)", errTooManyChunks, len(chunks), maxChunks)
	}
	if job.Frequency == "" && len(frequencies) == 1 {
		for frequency := range frequencies {
			job.Frequency = frequency
		}
	}
	if err := b.Store.CreateJob(ctx, job, chunks); err != nil {
		return "", 0, err
	}
	return jobID, len(chunks), nil
}

// JobStatus 是作业的进度；Failed 只在查询单个作业时填写，至多 maxFailedChunks 个。
type JobStatus struct {
	Job    store.BackfillJob
	Counts map[string]int
	Failed []store.BackfillChunk
}

const maxFailedChunks = 50

// Status 返回作业进度与失败的分块。
func (b *Backfiller) Status(ctx context.Context, jobID string) (*JobStatus, error) {
	job, chunks, err := b.Store.GetJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if job == nil {
		return nil, fmt.Errorf("backfill job %s not found", jobID)
	}
	status := &JobStatus{Job: *job, Counts: map[string]int{}}
	for _, chunk := range chunks {
		status.Counts[chunk.Status]++
		if chunk.Status == store.BackfillFailed && len(status.Failed) < maxFailedChunks {
			status.Failed = append(status.Failed, chunk)
		}
	}
	return status, nil
}

// List 分页返回空间符合筛选的补采作业及各状态的分块数。
func (b *Backfiller) List(ctx context.Context, spaceID string, filter store.BackfillJobFilter, page, size int) ([]JobStatus, int64, error) {
	jobs, total, err := b.Store.ListJobs(ctx, spaceID, filter, page, size)
	if err != nil {
		return nil, 0, err
	}
	jobIDs := make([]string, 0, len(jobs))
	for _, job := range jobs {
		jobIDs = append(jobIDs, job.JobID)
	}
	counts, err := b.Store.ChunkStatusCountsOf(ctx, jobIDs)
	if err != nil {
		return nil, 0, err
	}
	items := make([]JobStatus, 0, len(jobs))
	for _, job := range jobs {
		jobCounts := counts[job.JobID]
		if jobCounts == nil {
			jobCounts = map[string]int{}
		}
		items = append(items, JobStatus{Job: job, Counts: jobCounts})
	}
	return items, total, nil
}

// Cancel 取消作业：尚未开始的分块不再执行，运行中的分块等其结束。
func (b *Backfiller) Cancel(ctx context.Context, jobID string) error {
	if err := b.Store.CancelJob(ctx, jobID); err != nil {
		return err
	}
	// 取消前已成功的分块记了补写通知，一并发出。
	b.flushNotices(ctx, jobID)
	return nil
}

// Retry 把已失败作业的失败分块退回待执行，由下一次调度继续执行；返回退回的分块数。
func (b *Backfiller) Retry(ctx context.Context, jobID string) (int, error) {
	return b.Store.RetryJob(ctx, jobID)
}

// ClearAbsences 清掉一个数据集已确认的行情源缺失，并让历史扫描重新进行；返回清掉的区间数。
func (b *Backfiller) ClearAbsences(ctx context.Context, spaceID, datasetID, frequency string) (int64, error) {
	return b.Store.ClearAbsences(ctx, spaceID, datasetID, frequency)
}

// Tick 推进该空间的补采作业：在名额内认领分块并异步执行，作业全部结束后标记结果。
func (b *Backfiller) Tick(ctx context.Context, spaceID string) error {
	b.tickMu.Lock()
	defer b.tickMu.Unlock()
	if b.Paused != nil {
		paused, err := b.Paused(ctx, spaceID)
		if err != nil {
			return err
		}
		if paused {
			return nil
		}
	}
	jobs, err := b.Store.ActiveJobs(ctx, spaceID)
	if err != nil {
		return err
	}
	// 强制重拉的作业排在最后分名额：它重写的是已有的行，不急；行情源不可用时它的分块每块都失败、退回重试，
	// 排在前面会每轮占满名额，补缺口的作业只能捡空档（2026-11-23 港股收市后整天重取拖慢了自动补采）。
	sort.SliceStable(jobs, func(i, j int) bool {
		return jobs[i].Kind != store.BackfillKindForce && jobs[j].Kind == store.BackfillKindForce
	})
	for _, job := range jobs {
		inFlight, err := b.Store.InFlightChunks(ctx, spaceID)
		if err != nil {
			return err
		}
		if free := b.maxInFlight(ctx, spaceID) - inFlight; free > 0 {
			latestEnd := b.now().Add(chunkInvokeLimit + chunkSafetyMargin)
			claimed, err := b.Store.ClaimChunks(ctx, job.JobID, free, latestEnd)
			if err != nil {
				return err
			}
			if len(claimed) > 0 {
				if err := b.Store.SetJobStatus(ctx, job.JobID, store.BackfillRunning, ""); err != nil {
					return err
				}
			}
			for _, chunk := range claimed {
				b.workers.Add(1)
				go func() {
					defer b.workers.Done()
					// 分块执行完（成功或失败）后立刻补位认领下一块，不等下一次定时调度：
					// 否则吞吐被限制为每次调度一批（每分钟约 5 块），大窗口要排很久。
					// 退回名额、结果不明的分块不触发补位，避免函数未就绪时空转。
					if b.runChunk(b.baseContext(), spaceID, chunk, job.Kind == store.BackfillKindForce) && b.Refill {
						if err := b.Tick(b.baseContext(), spaceID); err != nil {
							log.WarnContextf(b.baseContext(), "collector backfill refill failed space=%s: %v", spaceID, err)
						}
					}
				}()
			}
		}
		counts, err := b.Store.ChunkStatusCounts(ctx, job.JobID)
		if err != nil {
			return err
		}
		if counts[store.BackfillPending] == 0 && counts[store.BackfillRunning] == 0 {
			status, message := store.BackfillSucceeded, ""
			if counts[store.BackfillFailed] > 0 {
				status, message = store.BackfillFailed, fmt.Sprintf("%d chunks failed", counts[store.BackfillFailed])
			}
			if err := b.Store.SetJobStatus(ctx, job.JobID, status, message); err != nil {
				return err
			}
			if status == store.BackfillSucceeded {
				b.advanceWatermark(ctx, job)
			}
			// 失败的作业里已成功的分块也记了补写通知，一并发出。
			b.flushNotices(ctx, job.JobID)
		}
	}
	return nil
}

func (b *Backfiller) baseContext() context.Context {
	if b.BaseCtx != nil {
		return b.BaseCtx
	}
	return context.Background()
}

// chunkPlan 返回分块用的计划：来源、拉取参数、输出字段取当前任务（Routes），交易所符号取标的时间线上覆盖分块起点的那一段
// （改名或换合约的标的，旧区间要用旧符号，否则会取到别的交易对并写入）。
func (b *Backfiller) chunkPlan(ctx context.Context, spaceID string, chunk store.BackfillChunk) (*marketfetch.Plan, error) {
	if b.Routes == nil {
		return nil, fmt.Errorf("backfill routes are not configured")
	}
	plan, found, err := b.Routes.BackfillRoute(ctx, spaceID, chunk.DatasetID, chunk.Frequency)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("数据集 %s（%s）没有对应的采集任务（任务已删除），不能补采", chunk.DatasetID, chunk.Frequency)
	}
	timeline, err := loadTimeline(ctx, b.Checker.Timeline, spaceID)
	if err != nil {
		return nil, err
	}
	symbol, ok := timeline.SymbolAt(DatasetKey(chunk.DatasetID, chunk.Frequency), chunk.SubjectID, chunk.From)
	if !ok {
		return nil, fmt.Errorf("标的时间线里没有 %s 在 %s 的交易所符号", chunk.SubjectID, chunk.From.UTC().Format(time.RFC3339))
	}
	plan.SymbolExceptions = map[string]string{chunk.SubjectID: symbol}
	return plan, nil
}

// chunkRequest 构造补采分块的调用请求：一个标的、一段连续时间，只写行，不涉及周期。force 是强制重拉作业的分块：
// 只用开高低收完整的来源（见 marketfetch.Request.CompleteOnly）。
func chunkRequest(spaceID, functionName string, plan *marketfetch.Plan, chunk store.BackfillChunk, force bool) (marketfetch.Request, error) {
	frequency, err := marketdata.ParseFrequency(chunk.Frequency)
	if err != nil {
		return marketfetch.Request{}, err
	}
	bars := barCount(frequency, chunk.From, chunk.To)
	if bars < 1 || bars > MaxChunkBars {
		return marketfetch.Request{}, fmt.Errorf("chunk spans %d bars", bars)
	}
	var outputFields string
	for _, dataset := range plan.Datasets {
		if marketfetch.PlanDatasetKeyOf(dataset) == DatasetKey(chunk.DatasetID, chunk.Frequency) && len(dataset.OutputFields) > 0 {
			raw, _ := json.Marshal(dataset.OutputFields)
			outputFields = string(raw)
		}
	}
	instanceID := "backfill|" + chunk.Frequency + "|" + chunk.SubjectID
	// 补采与实时采集同一套来源分配：A 股按标的分配、失败回落，加密货币固定标签来源。
	provider, sourceID := plan.Market.RequestSource()
	item := domain.CollectionItem{
		InstanceID: instanceID, SubjectID: chunk.SubjectID, Symbol: plan.ExchangeSymbol(chunk.SubjectID),
		StartTime: chunk.From.UTC().Format(time.RFC3339), EndTime: chunk.To.UTC().Format(time.RFC3339), BarLimit: bars,
		Provider: provider, SourceID: sourceID, MarketID: plan.Market.MarketID, InstrumentType: plan.Market.InstrumentType,
		MarketType: plan.Market.MarketType, DataType: "kline", DatasetID: chunk.DatasetID, Frequency: chunk.Frequency,
	}
	target := domain.WriteTarget{
		ID: chunk.DatasetID + "|" + chunk.Frequency + "|" + chunk.SubjectID, SpaceID: spaceID, InstanceID: instanceID, TaskID: chunk.DatasetID,
		DatasetID: chunk.DatasetID, OutputFields: outputFields, Frequency: chunk.Frequency,
	}
	return marketfetch.Request{
		BatchID: chunk.ChunkID, BatchKind: domain.BatchKindBackfill, SpaceID: spaceID, MarketID: plan.Market.MarketID, InstrumentType: plan.Market.InstrumentType,
		DatasetID: chunk.DatasetID, Frequency: chunk.Frequency, Provider: provider, SourceID: sourceID, MarketType: plan.Market.MarketType,
		NodeID: functionName, FunctionName: functionName, RequestID: chunk.ChunkID, PlanRequest: true, Concurrency: 1, FetchLimits: plan.Fetch.Limits(),
		Items: []domain.CollectionItem{item}, Targets: []domain.WriteTarget{target}, CompleteOnly: force,
	}, nil
}

// runChunk 调用补采函数执行一个分块，随后复查该块的缺口。force 表示分块属于强制重拉作业：函数报失败时退回重试
// （见下文），而不是直接记失败。
func (b *Backfiller) runChunk(ctx context.Context, spaceID string, chunk store.BackfillChunk, force bool) (finished bool) {
	fail := func(err error) {
		finished = true
		log.WarnContextf(ctx, "collector backfill chunk failed space=%s chunk=%s: %v", spaceID, chunk.ChunkID, err)
		if finishErr := b.Store.FinishChunk(ctx, chunk.ID, chunk.Attempts, store.BackfillFailed, 0, err.Error()); finishErr != nil {
			log.WarnContextf(ctx, "collector backfill chunk state write failed chunk=%s: %v", chunk.ChunkID, finishErr)
		}
	}
	if chunk.Attempts > maxChunkAttempts {
		reason := fmt.Sprintf("gave up after %d attempts", chunk.Attempts-1)
		if chunk.LastError != "" {
			reason += ": " + chunk.LastError
		}
		fail(errors.New(reason))
		return
	}
	plan, err := b.chunkPlan(ctx, spaceID, chunk)
	if err != nil {
		fail(err)
		return
	}
	release := func(reason error) {
		// 执行端暂时不可用（补采函数还在创建或升级、本地执行端离线）：不算执行失败，分块退回待认领并退回这次尝试。
		log.WarnContextf(ctx, "collector backfill executor is not ready space=%s chunk=%s: %v", spaceID, chunk.ChunkID, reason)
		if releaseErr := b.Store.ReleaseChunk(ctx, chunk.ID, chunk.Attempts); releaseErr != nil {
			log.WarnContextf(ctx, "collector backfill chunk release failed chunk=%s: %v", chunk.ChunkID, releaseErr)
		}
	}
	executor := marketfetch.PlanExecutorSCF
	if b.Executor != nil {
		if executor, err = b.Executor(ctx, spaceID, chunk.DatasetID); err != nil {
			release(err)
			return
		}
	}
	invoker := b.Invoker
	var functionName string
	switch executor {
	case marketfetch.PlanExecutorLocal:
		if b.Local == nil {
			fail(fmt.Errorf("no local executor is configured for space %s", spaceID))
			return
		}
		invoker, functionName = b.Local, "local-"+strings.ToLower(spaceID)
	default:
		provider := b.Functions(spaceID)
		if provider == nil {
			fail(fmt.Errorf("no backfill function provider for space %s", spaceID))
			return
		}
		if functionName, err = provider.EnsureBackfillFunction(ctx, plan.Market.MarketType); err != nil {
			release(err)
			return
		}
	}
	request, err := chunkRequest(spaceID, functionName, plan, chunk, force)
	if err != nil {
		fail(err)
		return
	}
	event, err := marketfetch.MarketFetchEvent(request)
	if err != nil {
		fail(err)
		return
	}
	event["source"] = model.EventSourceCollectorBackfill
	// 名额有效期从实际调用时起算：认领之后等待函数就绪的时间不能吃掉执行预算。
	if err := b.Store.RenewChunk(ctx, chunk.ID, chunk.Attempts, b.now().Add(chunkInvokeLimit+chunkSafetyMargin)); err != nil {
		log.WarnContextf(ctx, "collector backfill chunk is no longer owned space=%s chunk=%s: %v", spaceID, chunk.ChunkID, err)
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, chunkInvokeLimit)
	result, err := invoker.Invoke(callCtx, spaceID, functionName, event)
	cancel()
	if err != nil {
		// 同步调用超时或断连时结果不明：不标记失败，名额保留到最迟结束时间后，由重新认领再执行。
		log.WarnContextf(ctx, "collector backfill invoke outcome unknown space=%s chunk=%s: %v", spaceID, chunk.ChunkID, err)
		return
	}
	if success, _ := result.Result["success"].(bool); !success && !sourceReportedEmpty(result) {
		// 函数只因“行情源没有已收盘 K 线”失败时不算执行失败，继续走下面的复查与缺失判定。
		failure := fmt.Errorf("function reported failure: code=%d message=%q result=%v", result.Code, result.Message, result.Result)
		if !force {
			fail(failure)
			return
		}
		// 强制重拉的行已经在库里，失败后没有缺口可让自动补采重新发现：退回待执行，由后面的认领重试，
		// 尝试次数照常累计，超过 maxChunkAttempts 后下一次认领时记失败。这里不触发补位（返回 false），
		// 重试等到同一空间别的分块执行完、或下一轮对账（每分钟一次）才被认领，不在原地连续重试。
		log.WarnContextf(ctx, "collector forced backfill chunk failed and is requeued space=%s chunk=%s attempt=%d: %v", spaceID, chunk.ChunkID, chunk.Attempts, failure)
		if err := b.Store.RequeueChunk(ctx, chunk.ID, chunk.Attempts, chunk.Missing, failure.Error()); err != nil {
			log.WarnContextf(ctx, "collector backfill chunk requeue failed chunk=%s: %v", chunk.ChunkID, err)
		}
		return false
	}
	// 复查带上 BeforeFirstPlan：强制重拉早于计划生效点的分块要真的读回来确认；范围在计划内的分块不受影响。
	check, err := b.Checker.Check(ctx, Request{SpaceID: spaceID, DatasetID: chunk.DatasetID, SubjectID: chunk.SubjectID, From: chunk.From, To: chunk.To, BeforeFirstPlan: true})
	if err != nil {
		fail(fmt.Errorf("verify chunk: %w", err))
		return
	}
	finished = true
	note := ""
	rows := bars(chunk) - check.Absent
	if check.Missing > 0 {
		missing := missingKey(check.Gaps)
		if missing != chunk.Missing {
			// 函数已成功执行，但复查仍缺：可能是行情源没有，也可能是上游晚出数、暂时缺失。记下这次缺的 K 线退回重跑，
			// 连续两次缺的完全一样才认定是行情源没有（单根分块也一样：缺失记录是永久的，只有港美股收市后整天重取会补回）；
			// 超过最大尝试次数仍不一致时由下一次认领标记失败。
			note = fmt.Sprintf("%d bars are still missing after the backfill", check.Missing)
			if err := b.Store.RequeueChunk(ctx, chunk.ID, chunk.Attempts, missing, note); err != nil {
				log.WarnContextf(ctx, "collector backfill chunk requeue failed chunk=%s: %v", chunk.ChunkID, err)
			}
			return finished
		}
		// 连续两次缺的一样：这些 K 线是行情源本身没有的（没有成交的分钟、停牌等）。
		// 写进已确认缺失，检查与补采以后都扣除它们；分块按成功结束，缺口数写进说明。
		if err := b.Store.AddAbsences(ctx, absencesOf(spaceID, check.Gaps)); err != nil {
			fail(fmt.Errorf("record source absences: %w", err))
			return
		}
		rows -= check.Missing
		note = fmt.Sprintf("%d bars are absent at the source", check.Missing)
		log.InfoContextf(ctx, "collector backfill chunk finished with source gaps space=%s chunk=%s missing=%d", spaceID, chunk.ChunkID, check.Missing)
	}
	if err := b.Store.FinishChunk(ctx, chunk.ID, chunk.Attempts, store.BackfillSucceeded, max(rows, 0), note); err != nil {
		log.WarnContextf(ctx, "collector backfill chunk state write failed chunk=%s: %v", chunk.ChunkID, err)
	}
	return finished
}

// sourceReportedEmpty 判断函数的失败是否仅因行情源返回了空结果（没有已收盘的 K 线）。
func sourceReportedEmpty(result marketfetch.InvocationResult) bool {
	text := strings.ToLower(fmt.Sprintf("%s %v", result.Message, result.Result))
	return strings.Contains(text, strings.ToLower(marketdata.ErrNoClosedBar.Error()))
}

// missingKey 把复查仍缺的 K 线规范化成区间串，用于比较两次执行缺的是否相同。
func missingKey(gaps []Gap) string {
	parts := make([]string, 0, len(gaps))
	for _, gap := range gaps {
		parts = append(parts, fmt.Sprintf("%d-%d", gap.From.Unix(), gap.To.Unix()))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func absencesOf(spaceID string, gaps []Gap) []store.SourceAbsence {
	out := make([]store.SourceAbsence, 0, len(gaps))
	for _, gap := range gaps {
		out = append(out, store.SourceAbsence{SpaceID: spaceID, DatasetID: gap.DatasetID, Frequency: gap.Frequency, SubjectID: gap.SubjectID, From: gap.From, To: gap.To, Bars: gap.Missing})
	}
	return out
}

func bars(chunk store.BackfillChunk) int {
	frequency, err := marketdata.ParseFrequency(chunk.Frequency)
	if err != nil {
		return 0
	}
	return barCount(frequency, chunk.From, chunk.To)
}

// flushNotices 在作业结束后把补写通知封存成事件。
func (b *Backfiller) flushNotices(ctx context.Context, jobID string) {
	if b.FlushNotices == nil {
		return
	}
	if err := b.FlushNotices(ctx); err != nil {
		log.WarnContextf(ctx, "collector backfill notice flush failed job=%s: %v", jobID, err)
	}
}

// advanceWatermark 自动补采作业全部成功后才推进扫描水位；失败的作业不推进，下次扫描会重新覆盖同一窗口。
func (b *Backfiller) advanceWatermark(ctx context.Context, job store.BackfillJob) {
	if !job.AdvanceWatermark || job.DatasetID == "" || job.Frequency == "" {
		return
	}
	if err := b.Store.SetWatermark(ctx, job.SpaceID, job.DatasetID, job.Frequency, job.To); err != nil {
		log.WarnContextf(ctx, "collector auto backfill watermark update failed job=%s: %v", job.JobID, err)
	}
}

// AutoScan 每 autoScanInterval 一次：从扫描水位（向前重叠 autoScanOverlap）起，检查已过截止时间的周期，发现缺口就建立自动补采作业。
// 每次每个数据集最多扫描 autoScanWindow、展开不超过 autoMaxChunks 个分块；Collector 停机恢复后从水位追赶。
func (b *Backfiller) AutoScan(ctx context.Context, spaceID string) error {
	b.mu.Lock()
	if b.lastScan == nil {
		b.lastScan = map[string]time.Time{}
	}
	// 每个 autoScanInterval 槽只扫一次，且只在槽内偏移 autoScanOffset 之后：lastScan 记录已扫的槽起点。
	now := b.now()
	started := now.Truncate(autoScanInterval)
	if now.Sub(started) < autoScanOffset || b.lastScan[spaceID].Equal(started) {
		b.mu.Unlock()
		return nil
	}
	b.lastScan[spaceID] = started
	b.mu.Unlock()
	// 扫描被超时或取消打断时不占用整个扫描槽：下一轮对账就重试。
	defer func() {
		if ctx.Err() != nil {
			b.mu.Lock()
			if b.lastScan[spaceID].Equal(started) {
				delete(b.lastScan, spaceID)
			}
			b.mu.Unlock()
		}
	}()
	active, err := b.Store.ActiveJobs(ctx, spaceID)
	if err != nil {
		return err
	}
	for _, job := range active {
		if job.Source == "auto" {
			return nil // 上一次自动作业还没结束，不叠加
		}
	}
	timeline, err := loadTimeline(ctx, b.Checker.Timeline, spaceID)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(timeline))
	for key := range timeline {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	now = b.now()
	for _, key := range keys {
		datasetID, frequencyName, _ := strings.Cut(key, "|")
		segments := timeline[key]
		if !timeline.Active(key) {
			continue
		}
		frequency, err := marketdata.ParseFrequency(frequencyName)
		if err != nil {
			continue
		}
		lateness := time.Duration(segments[len(segments)-1].LatenessS) * time.Second
		to := alignDown(frequency, now.Add(-lateness-frequency.NominalDuration()))
		watermark, err := b.Store.Watermark(ctx, spaceID, datasetID, frequencyName)
		if err != nil {
			return err
		}
		from := time.Unix(segments[0].From, 0).UTC()
		if !watermark.IsZero() {
			from = watermark.Add(-autoScanOverlap)
		}
		if from.Before(to.Add(-autoScanWindow - autoScanOverlap)) {
			// 追赶：每次只向前推进一个窗口。判断时扣掉向前重叠的部分，否则 1h 等低频数据集每到新的整点都会被误判为落后，
			// 先扫到“整点 - 重叠”、下一轮再补到整点。
			to = from.Add(autoScanWindow)
		}
		if !from.Before(to) {
			continue
		}
		// 窗口展开的分块太多时对半缩小再试；缩到 10 分钟仍太多就跳过本轮，等下一轮。
		windowTo := to
		var jobID string
		var chunks int
		var submitErr error
		for {
			request := JobRequest{SpaceID: spaceID, DatasetID: datasetID, From: from, To: windowTo, Watermark: &WatermarkTarget{DatasetID: datasetID, Frequency: frequencyName}}
			jobID, chunks, submitErr = b.submit(ctx, request, "auto", autoMaxChunks)
			if submitErr == nil || !errors.Is(submitErr, errTooManyChunks) || windowTo.Sub(from) <= 10*time.Minute {
				break
			}
			windowTo = alignDown(frequency, from.Add(windowTo.Sub(from)/2))
			if !windowTo.After(from) {
				break
			}
		}
		if submitErr != nil {
			log.WarnContextf(ctx, "collector auto backfill scan failed space=%s dataset=%s: %v", spaceID, key, submitErr)
			continue
		}
		if jobID == "" {
			// 窗口内没有缺口：直接推进水位。
			if err := b.Store.SetWatermark(ctx, spaceID, datasetID, frequencyName, windowTo); err != nil {
				return err
			}
			continue
		}
		log.InfoContextf(ctx, "collector auto backfill job created space=%s dataset=%s job=%s chunks=%d", spaceID, key, jobID, chunks)
	}
	return nil
}
