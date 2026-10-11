package klinecheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	autoScanInterval  = time.Hour
	autoScanOverlap   = 10 * time.Minute
	autoScanWindow    = 24 * time.Hour
	autoMaxChunks     = 2000
	maxChunkAttempts  = 5
	chunkInvokeLimit  = 90 * time.Second
	chunkSafetyMargin = 30 * time.Second
)

// JobRequest 是补采作业的范围，保存在作业记录里。
type JobRequest struct {
	SpaceID   string    `json:"space_id"`
	DatasetID string    `json:"dataset_id,omitempty"`
	SubjectID string    `json:"subject_id,omitempty"`
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
	// Force 为 true 时不检查缺口，范围内应有的 K 线全部重拉。
	Force bool `json:"force,omitempty"`
}

// BackfillStore 是补采需要的持久化接口，由 *store.BackfillRepository 实现。
type BackfillStore interface {
	CreateJob(context.Context, store.BackfillJob, []store.BackfillChunk) error
	GetJob(context.Context, string) (*store.BackfillJob, []store.BackfillChunk, error)
	ActiveJobs(context.Context, string) ([]store.BackfillJob, error)
	ListJobs(context.Context, string, int, int) ([]store.BackfillJob, int64, error)
	SetJobStatus(context.Context, string, string, string) error
	CancelJob(context.Context, string) error
	ClaimChunks(context.Context, string, int, time.Time) ([]store.BackfillChunk, error)
	InFlightChunks(context.Context, string) (int, error)
	FinishChunk(context.Context, int64, string, int, string) error
	ChunkStatusCounts(context.Context, string) (map[string]int, error)
	Watermark(context.Context, string, string, string) (time.Time, error)
	SetWatermark(context.Context, string, string, string, time.Time) error
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
	Checker   *Checker
	Store     BackfillStore
	Plans     PlanHistory
	Invoker   Invoker
	Functions func(spaceID string) FunctionProvider
	// BaseCtx 是异步执行分块的上下文（进程生命周期）。
	BaseCtx     context.Context
	MaxInFlight int
	Now         func() time.Time

	mu       sync.Mutex
	lastScan map[string]time.Time
	workers  sync.WaitGroup
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
func splitGap(gap Gap, step time.Duration) [][2]time.Time {
	var parts [][2]time.Time
	for from := gap.From; from.Before(gap.To); {
		to := from.Add(time.Duration(MaxChunkBars) * step)
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
	if b == nil || b.Checker == nil || b.Store == nil {
		return "", 0, fmt.Errorf("backfiller is not initialized")
	}
	result, err := b.Checker.Check(ctx, Request{SpaceID: request.SpaceID, DatasetID: request.DatasetID, SubjectID: request.SubjectID, From: request.From, To: request.To, AssumeMissing: request.Force})
	if err != nil {
		return "", 0, err
	}
	if len(result.Gaps) == 0 {
		return "", 0, nil
	}
	if result.Truncated {
		return "", 0, fmt.Errorf("the range has more than %d gaps; narrow the range", MaxGaps)
	}
	jobID := uuid.NewString()
	var chunks []store.BackfillChunk
	for _, gap := range result.Gaps {
		frequency, err := marketdata.ParseFrequency(gap.Frequency)
		if err != nil {
			return "", 0, err
		}
		for _, part := range splitGap(gap, frequency.Duration()) {
			chunks = append(chunks, store.BackfillChunk{DatasetID: gap.DatasetID, Frequency: gap.Frequency, SubjectID: gap.SubjectID, From: part[0], To: part[1], ChunkID: chunkID(jobID, gap.DatasetID, gap.Frequency, gap.SubjectID, part[0])})
		}
	}
	if len(chunks) > MaxChunksPerJob {
		return "", 0, fmt.Errorf("the range expands to %d chunks (limit %d); narrow the range", len(chunks), MaxChunksPerJob)
	}
	raw, _ := json.Marshal(request)
	if err := b.Store.CreateJob(ctx, store.BackfillJob{JobID: jobID, SpaceID: request.SpaceID, Source: source, Request: string(raw)}, chunks); err != nil {
		return "", 0, err
	}
	return jobID, len(chunks), nil
}

// JobStatus 是作业的进度。
type JobStatus struct {
	Job    store.BackfillJob
	Counts map[string]int
	Failed []store.BackfillChunk
}

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
		if chunk.Status == store.BackfillFailed && len(status.Failed) < 20 {
			status.Failed = append(status.Failed, chunk)
		}
	}
	return status, nil
}

// List 分页返回空间的补采作业及各状态的分块数。
func (b *Backfiller) List(ctx context.Context, spaceID string, page, size int) ([]JobStatus, int64, error) {
	jobs, total, err := b.Store.ListJobs(ctx, spaceID, page, size)
	if err != nil {
		return nil, 0, err
	}
	items := make([]JobStatus, 0, len(jobs))
	for _, job := range jobs {
		counts, err := b.Store.ChunkStatusCounts(ctx, job.JobID)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, JobStatus{Job: job, Counts: counts})
	}
	return items, total, nil
}

// Cancel 取消作业：尚未开始的分块不再执行，运行中的分块等其结束。
func (b *Backfiller) Cancel(ctx context.Context, jobID string) error {
	return b.Store.CancelJob(ctx, jobID)
}

// Tick 推进该空间的补采作业：在名额内认领分块并异步执行，作业全部结束后标记结果。
func (b *Backfiller) Tick(ctx context.Context, spaceID string) error {
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
					b.runChunk(b.baseContext(), spaceID, chunk)
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

// datasetMarket 从最近一份已生效计划里找到数据集所在的市场与交易所符号映射。
func (b *Backfiller) datasetMarket(ctx context.Context, spaceID, datasetID, frequency string) (*marketfetch.Plan, error) {
	records, err := b.Plans.ListAll(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	key := DatasetKey(datasetID, frequency)
	for i := len(records) - 1; i >= 0; i-- {
		content, err := marketfetch.DecodeFetchPlanContent(records[i].Plan)
		if err != nil {
			return nil, err
		}
		for _, name := range content.SortedFunctionNames() {
			function := content.Functions[name]
			plan := content.FinalPlan(function)
			if plan == nil {
				plan = content.TransitionalPlan(function)
			}
			if plan == nil {
				continue
			}
			for _, dataset := range plan.Datasets {
				if marketfetch.PlanDatasetKeyOf(dataset) == key {
					return plan, nil
				}
			}
		}
	}
	return nil, fmt.Errorf("dataset %s %s is not part of any fetch plan", datasetID, frequency)
}

// chunkRequest 构造补采分块的调用请求：一个标的、一段连续时间，只写行，不涉及周期。
func chunkRequest(spaceID, functionName string, plan *marketfetch.Plan, chunk store.BackfillChunk) (marketfetch.Request, error) {
	frequency, err := marketdata.ParseFrequency(chunk.Frequency)
	if err != nil {
		return marketfetch.Request{}, err
	}
	bars := int(chunk.To.Sub(chunk.From) / frequency.Duration())
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
		NodeID: functionName, FunctionName: functionName, RequestID: chunk.ChunkID, WriteIDByContent: true, Concurrency: 1,
		Items: []domain.CollectionItem{item}, Targets: []domain.WriteTarget{target},
	}, nil
}

// runChunk 调用补采函数执行一个分块，随后复查该块的缺口。
func (b *Backfiller) runChunk(ctx context.Context, spaceID string, chunk store.BackfillChunk) {
	fail := func(err error) {
		log.WarnContextf(ctx, "collector backfill chunk failed space=%s chunk=%s: %v", spaceID, chunk.ChunkID, err)
		if finishErr := b.Store.FinishChunk(ctx, chunk.ID, store.BackfillFailed, 0, err.Error()); finishErr != nil {
			log.WarnContextf(ctx, "collector backfill chunk state write failed chunk=%s: %v", chunk.ChunkID, finishErr)
		}
	}
	if chunk.Attempts > maxChunkAttempts {
		fail(fmt.Errorf("gave up after %d attempts", chunk.Attempts-1))
		return
	}
	plan, err := b.datasetMarket(ctx, spaceID, chunk.DatasetID, chunk.Frequency)
	if err != nil {
		fail(err)
		return
	}
	provider := b.Functions(spaceID)
	if provider == nil {
		fail(fmt.Errorf("no backfill function provider for space %s", spaceID))
		return
	}
	functionName, err := provider.EnsureBackfillFunction(ctx, plan.Market.MarketType)
	if err != nil {
		fail(err)
		return
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
	callCtx, cancel := context.WithTimeout(ctx, chunkInvokeLimit)
	result, err := b.Invoker.Invoke(callCtx, spaceID, functionName, event, cloudnodepb.ScfInvokeType_SCF_INVOKE_TYPE_REQUEST_RESPONSE)
	cancel()
	if err != nil {
		// 同步调用超时或断连时结果不明：不标记失败，名额保留到最迟结束时间后，由重新认领再执行。
		log.WarnContextf(ctx, "collector backfill invoke outcome unknown space=%s chunk=%s: %v", spaceID, chunk.ChunkID, err)
		return
	}
	if success, _ := result.Result["success"].(bool); !success {
		fail(fmt.Errorf("function reported failure: %v", result.Result["message"]))
		return
	}
	check, err := b.Checker.Check(ctx, Request{SpaceID: spaceID, DatasetID: chunk.DatasetID, SubjectID: chunk.SubjectID, From: chunk.From, To: chunk.To})
	if err != nil {
		fail(fmt.Errorf("verify chunk: %w", err))
		return
	}
	if check.Missing > 0 {
		fail(fmt.Errorf("%d bars are still missing after the backfill", check.Missing))
		return
	}
	if err := b.Store.FinishChunk(ctx, chunk.ID, store.BackfillSucceeded, bars(chunk), ""); err != nil {
		log.WarnContextf(ctx, "collector backfill chunk state write failed chunk=%s: %v", chunk.ChunkID, err)
	}
}

func bars(chunk store.BackfillChunk) int {
	frequency, err := marketdata.ParseFrequency(chunk.Frequency)
	if err != nil || frequency.Duration() <= 0 {
		return 0
	}
	return int(chunk.To.Sub(chunk.From) / frequency.Duration())
}

// AutoScan 每小时一次：从扫描水位（向前重叠 10 分钟）起，检查已过截止时间的周期，发现缺口就建立自动补采作业。
// 每次最多扫描 24 小时、展开不超过 autoMaxChunks 个分块；Collector 停机恢复后从水位追赶。
func (b *Backfiller) AutoScan(ctx context.Context, spaceID string) error {
	b.mu.Lock()
	if b.lastScan == nil {
		b.lastScan = map[string]time.Time{}
	}
	if last := b.lastScan[spaceID]; !last.IsZero() && b.now().Sub(last) < autoScanInterval {
		b.mu.Unlock()
		return nil
	}
	b.lastScan[spaceID] = b.now()
	b.mu.Unlock()
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
	now := b.now()
	for _, key := range keys {
		datasetID, frequencyName, _ := strings.Cut(key, "|")
		segments := timeline[key]
		if len(segments) == 0 {
			continue
		}
		frequency, err := marketdata.ParseFrequency(frequencyName)
		if err != nil {
			continue
		}
		lateness := time.Duration(segments[len(segments)-1].LatenessS) * time.Second
		to := now.Add(-lateness - frequency.Duration()).Truncate(frequency.Duration())
		watermark, err := b.Store.Watermark(ctx, spaceID, datasetID, frequencyName)
		if err != nil {
			return err
		}
		from := time.Unix(segments[0].From, 0).UTC()
		if !watermark.IsZero() {
			from = watermark.Add(-autoScanOverlap)
		}
		if from.Before(to.Add(-autoScanWindow)) {
			// 追赶：每次只向前推进一个窗口。
			to = from.Add(autoScanWindow)
		}
		if !from.Before(to) {
			continue
		}
		jobID, chunks, err := b.Submit(ctx, JobRequest{SpaceID: spaceID, DatasetID: datasetID, From: from, To: to}, "auto")
		if err != nil {
			log.WarnContextf(ctx, "collector auto backfill scan failed space=%s dataset=%s: %v", spaceID, key, err)
			continue
		}
		if chunks > autoMaxChunks {
			log.WarnContextf(ctx, "collector auto backfill job is large space=%s dataset=%s chunks=%d", spaceID, key, chunks)
		}
		if jobID != "" {
			log.InfoContextf(ctx, "collector auto backfill job created space=%s dataset=%s job=%s chunks=%d", spaceID, key, jobID, chunks)
		}
		if err := b.Store.SetWatermark(ctx, spaceID, datasetID, frequencyName, to); err != nil {
			return err
		}
	}
	return nil
}

// ShortHash 返回字符串的短摘要，用于日志与名称。
func ShortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:4])
}
