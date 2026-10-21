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
	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	// DefaultMaxInFlight 是每个空间同时执行的补采分块数上限，与补采函数的预留并发一致。
	DefaultMaxInFlight = 5
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

// FunctionProvider 保证补采函数存在并返回函数名。
type FunctionProvider interface {
	EnsureBackfillFunction(ctx context.Context, marketType string) (string, error)
}

// Invoker 同步调用函数。
type Invoker interface {
	Invoke(ctx context.Context, spaceID, nodeID string, event map[string]any, invokeType cloudnodepb.ScfInvokeType) (scfinvoker.InvocationResult, error)
}

// Backfiller 管理补采作业：展开分块、通过补采函数执行、复查缺口。
type Backfiller struct {
	// Refill 为 true 时，分块执行完立刻补位认领下一块（生产开启）；为 false 时只在调度节拍上认领。
	Refill bool

	Checker   *Checker
	Store     BackfillStore
	Plans     PlanHistory
	Invoker   Invoker
	Functions func(spaceID string) FunctionProvider
	// Executor 返回数据集的补采执行端（scf 或 local），为空时按 scf；Local 是本地执行端（collector-stream 里的 MarketFetchExecutor）。
	Executor func(ctx context.Context, spaceID, datasetID string) (string, error)
	Local    Invoker
	// Notify 在补采作业成功后调用（补齐通知：因子据此立即推进连续水位并补算），失败只记日志；absent 是确认行情源没有的标的。
	Notify func(ctx context.Context, filled []FilledRange) error
	// Paused 报告空间是否处于采集暂停；暂停期间不再认领分块（含补位），已在执行的分块自然结束。为空表示不暂停。
	Paused func(ctx context.Context, spaceID string) (bool, error)
	// BaseCtx 是异步执行分块的上下文（进程生命周期）。
	BaseCtx     context.Context
	MaxInFlight int
	Now         func() time.Time

	mu       sync.Mutex
	lastScan map[string]time.Time
	// lastHistoryRun 是每个空间最近一次历史缺口扫描的时间（见 ScanHistory）；每个数据集的扫描记录保存在库里。
	lastHistoryRun map[string]time.Time
	workers        sync.WaitGroup
	// tickMu 串行化认领，避免补位与调度节拍同时超出并发上限。
	tickMu sync.Mutex
}

// FilledRange 是补采补齐的一段（数据集, 频率, [From, To)）。
type FilledRange struct {
	SpaceID   string
	DatasetID string
	Frequency string
	From, To  time.Time
	Absent    []string
}

func (b *Backfiller) now() time.Time {
	if b.Now != nil {
		return b.Now().UTC()
	}
	return time.Now().UTC()
}

func (b *Backfiller) maxInFlight() int {
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
	result, err := b.Checker.Check(ctx, Request{SpaceID: request.SpaceID, DatasetID: request.DatasetID, SubjectID: request.SubjectID, From: request.From, To: request.To, AssumeMissing: request.Force, BeforeFirstPlan: request.Force})
	if err != nil {
		return "", 0, err
	}
	if len(result.Gaps) == 0 {
		return "", 0, nil
	}
	if result.Truncated {
		return "", 0, fmt.Errorf("%w: more than %d gaps", errTooManyChunks, MaxGaps)
	}
	job := store.BackfillJob{SpaceID: request.SpaceID, Source: source, Kind: store.BackfillKindGap, DatasetID: request.DatasetID, SubjectID: request.SubjectID, From: request.From, To: request.To}
	if request.Force {
		job.Kind = store.BackfillKindForce
	}
	if request.Watermark != nil {
		job.DatasetID, job.Frequency, job.AdvanceWatermark = request.Watermark.DatasetID, request.Watermark.Frequency, true
	}
	return b.createJob(ctx, job, result.Gaps, maxChunks)
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
	return b.Store.CancelJob(ctx, jobID)
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
	for _, job := range jobs {
		inFlight, err := b.Store.InFlightChunks(ctx, spaceID)
		if err != nil {
			return err
		}
		if free := b.maxInFlight() - inFlight; free > 0 {
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
					if b.runChunk(b.baseContext(), spaceID, chunk) && b.Refill {
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
				b.notifyFilled(ctx, job)
			}
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

// datasetMarket 从采集计划里找到负责该标的的那份函数计划（市场与交易所符号映射）。
// 例外符号只保存在负责该标的的函数计划里，所以必须按数据集、频率、标的与时间定位，不能借用同一数据集的其他分片。
// 优先选择在 at 时已生效的计划份，找不到时退回最近一份包含该标的的计划。
func (b *Backfiller) datasetMarket(ctx context.Context, spaceID, datasetID, frequency, subjectID string, at time.Time) (*marketfetch.Plan, error) {
	records, err := b.Plans.ListAll(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	key := DatasetKey(datasetID, frequency)
	// resolve 返回选中的计划；标的的符号映射按原始新旧计划与时间重新判定，避免过渡计划合并后的映射用错。
	resolve := func(content *marketfetch.FetchPlanContent, function *marketfetch.PlanFunctionChange, plan *marketfetch.Plan) *marketfetch.Plan {
		symbol, ok := content.SymbolAt(function, key, subjectID, at)
		if !ok || plan.ExchangeSymbol(subjectID) == symbol {
			return plan
		}
		patched := *plan
		patched.SymbolExceptions = map[string]string{}
		for id, value := range plan.SymbolExceptions {
			patched.SymbolExceptions[id] = value
		}
		patched.SymbolExceptions[subjectID] = symbol
		return &patched
	}
	contents := make([]*marketfetch.FetchPlanContent, len(records))
	for i := range records {
		content, err := marketfetch.DecodeFetchPlanContent(records[i].Plan)
		if err != nil {
			return nil, err
		}
		contents[i] = content
		// 纯符号映射变更没有数据集生效边界：不知道旧映射适用到何时，变更之前的历史数据拒绝补采，避免取错交易对。
		for _, name := range content.SortedFunctionNames() {
			if !content.SymbolChangedWithoutBoundary(content.Functions[name], key, subjectID) {
				continue
			}
			// 未完成的计划还在更新（进度、错误、停用收尾都会改 UpdatedAt），无法证明映射何时切换：一律拒绝。
			// 已生效的计划不再更新，UpdatedAt 是确认完成的保守上界：此前的历史数据拒绝，此后的照常。
			if records[i].Status != store.FetchPlanApplied {
				return nil, fmt.Errorf("symbol mapping of %s is changing in an unfinished fetch plan without an effective boundary; backfill is refused until it is applied", subjectID)
			}
			if at.Before(records[i].UpdatedAt) {
				return nil, fmt.Errorf("symbol mapping of %s changed at %s without an effective boundary; backfill of earlier data is refused", subjectID, records[i].UpdatedAt.UTC().Format(time.RFC3339))
			}
		}
	}
	var fallback *marketfetch.Plan
	for i := len(records) - 1; i >= 0; i-- {
		content := contents[i]
		for _, name := range content.SortedFunctionNames() {
			function := content.Functions[name]
			for _, plan := range []*marketfetch.Plan{content.FinalPlan(function), content.TransitionalPlan(function)} {
				if plan == nil {
					continue
				}
				for _, dataset := range plan.Datasets {
					if marketfetch.PlanDatasetKeyOf(dataset) != key {
						continue
					}
					for _, copyView := range dataset.Copies {
						for _, subject := range copyView.Subjects {
							if !strings.EqualFold(subject.ID, subjectID) {
								continue
							}
							if copyView.From <= at.Unix() {
								return resolve(content, function, plan), nil
							}
							if fallback == nil {
								fallback = resolve(content, function, plan)
							}
						}
					}
				}
			}
		}
	}
	if fallback != nil {
		return fallback, nil
	}
	return nil, fmt.Errorf("subject %s of dataset %s %s is not part of any fetch plan", subjectID, datasetID, frequency)
}

// chunkRequest 构造补采分块的调用请求：一个标的、一段连续时间，只写行，不涉及周期。
func chunkRequest(spaceID, functionName string, plan *marketfetch.Plan, chunk store.BackfillChunk) (marketfetch.Request, error) {
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
	item := domain.CollectionItem{
		InstanceID: instanceID, SubjectID: chunk.SubjectID, Symbol: plan.ExchangeSymbol(chunk.SubjectID),
		StartTime: chunk.From.UTC().Format(time.RFC3339), EndTime: chunk.To.UTC().Format(time.RFC3339), BarLimit: bars,
		Provider: plan.Market.Provider, SourceID: plan.Market.SourceID, MarketID: plan.Market.MarketID, InstrumentType: plan.Market.InstrumentType,
		MarketType: plan.Market.MarketType, DataType: "kline", DatasetID: chunk.DatasetID, Frequency: chunk.Frequency,
	}
	target := domain.WriteTarget{
		ID: chunk.DatasetID + "|" + chunk.Frequency + "|" + chunk.SubjectID, SpaceID: spaceID, InstanceID: instanceID, TaskID: chunk.DatasetID,
		DatasetID: chunk.DatasetID, OutputFields: outputFields, Frequency: chunk.Frequency,
	}
	return marketfetch.Request{
		BatchID: chunk.ChunkID, BatchKind: domain.BatchKindBackfill, SpaceID: spaceID, MarketID: plan.Market.MarketID, InstrumentType: plan.Market.InstrumentType,
		DatasetID: chunk.DatasetID, Frequency: chunk.Frequency, Provider: plan.Market.Provider, SourceID: plan.Market.SourceID, MarketType: plan.Market.MarketType,
		NodeID: functionName, FunctionName: functionName, RequestID: chunk.ChunkID, WriteIDByContent: true, Concurrency: 1, FetchLimits: plan.Fetch.Limits(),
		Items: []domain.CollectionItem{item}, Targets: []domain.WriteTarget{target},
	}, nil
}

// runChunk 调用补采函数执行一个分块，随后复查该块的缺口。
func (b *Backfiller) runChunk(ctx context.Context, spaceID string, chunk store.BackfillChunk) (finished bool) {
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
	plan, err := b.datasetMarket(ctx, spaceID, chunk.DatasetID, chunk.Frequency, chunk.SubjectID, chunk.From)
	if err != nil {
		fail(err)
		return
	}
	release := func(reason error) {
		// 执行端暂时不可用（函数创建中、发布租约被占用、本地执行端离线）：不算执行失败，分块退回待认领并退回这次尝试。
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
	request, err := chunkRequest(spaceID, functionName, plan, chunk)
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
	result, err := invoker.Invoke(callCtx, spaceID, functionName, event, cloudnodepb.ScfInvokeType_SCF_INVOKE_TYPE_REQUEST_RESPONSE)
	cancel()
	if err != nil {
		// 同步调用超时或断连时结果不明：不标记失败，名额保留到最迟结束时间后，由重新认领再执行。
		log.WarnContextf(ctx, "collector backfill invoke outcome unknown space=%s chunk=%s: %v", spaceID, chunk.ChunkID, err)
		return
	}
	if success, _ := result.Result["success"].(bool); !success && !sourceReportedEmpty(result) {
		// 函数只因“行情源没有已收盘 K 线”失败时不算执行失败，继续走下面的复查与缺失判定。
		fail(fmt.Errorf("function reported failure: code=%d message=%q result=%v", result.Code, result.Message, result.Result))
		return
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
		if bars(chunk) > 1 && missing != chunk.Missing {
			// 函数已成功执行，但复查仍缺一部分：可能是行情源没有，也可能是暂时缺失。记下这次缺的 K 线退回重跑，
			// 连续两次缺的完全一样才认定是行情源没有；超过最大尝试次数仍不一致时由下一次认领标记失败。
			note = fmt.Sprintf("%d bars are still missing after the backfill", check.Missing)
			if err := b.Store.RequeueChunk(ctx, chunk.ID, chunk.Attempts, missing, note); err != nil {
				log.WarnContextf(ctx, "collector backfill chunk requeue failed chunk=%s: %v", chunk.ChunkID, err)
			}
			return finished
		}
		// 单根分块、或连续两次缺的一样：这些 K 线是行情源本身没有的（没有成交的分钟、停牌等）。
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
func sourceReportedEmpty(result scfinvoker.InvocationResult) bool {
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

// notifyFilled 在作业成功后按（数据集, 频率）发补齐通知。通知只是加快下游，失败只记日志（因子另有定时读回保底）。
// 确认没有的标的按库里的已确认缺失逐条带出精确的数据集、频率、标的与区间（进程重启不丢）。
func (b *Backfiller) notifyFilled(ctx context.Context, job store.BackfillJob) {
	if b.Notify == nil {
		return
	}
	_, chunks, err := b.Store.GetJob(ctx, job.JobID)
	if err != nil {
		log.WarnContextf(ctx, "collector backfill filled notification skipped job=%s: %v", job.JobID, err)
		return
	}
	ranges := map[string]*FilledRange{}
	var order []string
	for _, chunk := range chunks {
		key := chunk.DatasetID + "|" + chunk.Frequency
		item := ranges[key]
		if item == nil {
			item = &FilledRange{SpaceID: job.SpaceID, DatasetID: chunk.DatasetID, Frequency: chunk.Frequency, From: chunk.From, To: chunk.To}
			ranges[key] = item
			order = append(order, key)
		}
		if chunk.From.Before(item.From) {
			item.From = chunk.From
		}
		if chunk.To.After(item.To) {
			item.To = chunk.To
		}
	}
	filled := make([]FilledRange, 0, len(order))
	for _, key := range order {
		item := *ranges[key]
		filled = append(filled, item)
		absences, err := b.Store.AbsencesIn(ctx, job.SpaceID, item.DatasetID, item.Frequency, nil, item.From, item.To)
		if err != nil {
			log.WarnContextf(ctx, "collector backfill filled notification skips source absences job=%s dataset=%s: %v", job.JobID, item.DatasetID, err)
			continue
		}
		for _, absence := range absences {
			from, to := absence.From, absence.To
			if from.Before(item.From) {
				from = item.From
			}
			if to.After(item.To) {
				to = item.To
			}
			filled = append(filled, FilledRange{SpaceID: job.SpaceID, DatasetID: item.DatasetID, Frequency: item.Frequency, From: from, To: to, Absent: []string{absence.SubjectID}})
		}
	}
	if len(filled) == 0 {
		return
	}
	if err := b.Notify(ctx, filled); err != nil {
		log.WarnContextf(ctx, "collector backfill filled notification failed job=%s: %v", job.JobID, err)
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
	records, err := b.Plans.ListAll(ctx, spaceID)
	if err != nil {
		return err
	}
	timeline, err := BuildTimeline(records)
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
