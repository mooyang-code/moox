package e2e_test

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/taskrunner"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	publicstoragepb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// These process-local tests cover the complete ready -> combination plan ->
// terminal aggregation -> marker hand-off without requiring NATS or DuckDB.
func TestViewReadyCombinationPeriodChain(t *testing.T) {
	period := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	storage := new(fakePeriodStorage)
	runner := newBarrierCombinationRunner(2)
	executor := trigger.NewViewReadyRunner(fakePeriodBindings{items: testBindings()}, fakePeriodFactors{items: testFactors()}, runner, storage, t.TempDir())
	ready := viewReady(period, "complete")

	done := make(chan error, 1)
	go func() { done <- executor.Execute(context.Background(), "space-a", "source-ready-1", ready) }()
	<-runner.twoStarted
	if got := runner.activeCount(); got != 2 {
		t.Fatalf("active combinations = %d, want 2", got)
	}
	if got := runner.startedCombinations(); !equalStrings(sortedStrings(got), []string{"/factor-a", "/factor-b"}) {
		t.Fatalf("first concurrent combinations = %v, want both cross-section panels", got)
	}
	if storage.markerCount() != 0 {
		t.Fatal("marker published before all combination tasks were terminal")
	}
	close(runner.release)
	if err := <-done; err != nil {
		t.Fatalf("execute ready period: %v", err)
	}

	wantOrder := []string{"/factor-a", "/factor-b"}
	if got := runner.plannedCombinations(); !equalStrings(got, wantOrder) {
		t.Fatalf("combination plan = %v, want %v", got, wantOrder)
	}
	if runner.maxActiveCount() != 2 {
		t.Fatalf("max active combinations = %d, want 2", runner.maxActiveCount())
	}
	if storage.markerCount() != 1 || storage.lastMarker().GetStatus() != "complete" {
		t.Fatalf("factor marker = %+v, want one complete marker", storage.lastMarker())
	}

	// Persisted marker preflight makes redelivery a no-op.
	if err := executor.Execute(context.Background(), "space-a", "source-ready-1", ready); err != nil {
		t.Fatalf("redelivered ready period: %v", err)
	}
	if got := len(runner.plannedCombinations()); got != 2 {
		t.Fatalf("redelivery planned %d combinations, want 2 total", got)
	}
}

func TestViewReadyCombinationFailureReportsDegradedMarker(t *testing.T) {
	period := time.Date(2026, 8, 10, 13, 0, 0, 0, time.UTC)
	storage := new(fakePeriodStorage)
	runner := &immediateCombinationRunner{fail: map[string]error{"/factor-b": errors.New("synthetic factor failure")}}
	executor := trigger.NewViewReadyRunner(fakePeriodBindings{items: testBindings()}, fakePeriodFactors{items: testFactors()}, runner, storage, t.TempDir())
	err := executor.Execute(context.Background(), "space-a", "source-ready-degraded", viewReady(period, "complete"))
	if err != nil {
		t.Fatalf("execute degraded ready period: %v", err)
	}
	if len(runner.tasks) != 2 {
		t.Fatalf("executed combinations = %d, want both panels", len(runner.tasks))
	}
	marker := storage.lastMarker()
	if storage.markerCount() != 1 || marker.GetStatus() != "degraded" {
		t.Fatalf("marker = %+v, want one degraded marker", marker)
	}
	if got := marker.GetBindings()[1].GetFailedSubjects(); !equalStrings(got, []string{"BTC", "ETH", "SOL"}) {
		t.Fatalf("factor-b failed subjects = %v, want panel universe", got)
	}
	if got := storage.clearedCombinations(); !equalStrings(got, []string{"/factor-b"}) {
		t.Fatalf("cleared combinations = %v, want only failed factor-b panel", got)
	}
}

func TestViewReadyPipelineReadsFixedPanel(t *testing.T) {
	period := time.Date(2026, 8, 10, 14, 0, 0, 0, time.UTC)
	storage := newPipelinePeriodStorage("")
	exec := &pipelineFactorExecutor{entered: make(chan string, 8), releaseBTC: closedSignal()}
	runner := taskrunner.NewService(2, storage, exec, taskrunner.WithViewReadConfig(2, 200*time.Millisecond), taskrunner.WithBatchExecution(false))
	executor := trigger.NewViewReadyRunner(
		fakePeriodBindings{items: testBindings()}, fakePeriodFactors{items: testFactors()}, runner, storage, t.TempDir(),
	)
	if err := executor.Execute(context.Background(), "space-a", "pipeline-ready-1", viewReady(period, "complete")); err != nil {
		t.Fatalf("execute panel ready period: %v", err)
	}
	if storage.markerCount() != 1 || storage.lastMarker().GetStatus() != "complete" {
		t.Fatalf("factor marker = %+v, want one complete marker", storage.lastMarker())
	}
	if got := storage.writtenCombinations(); !equalStrings(got, []string{"/factor-a", "/factor-b"}) {
		t.Fatalf("written combinations = %v", got)
	}
	reads := storage.readOrder()
	if len(reads) != 6 {
		t.Fatalf("View read count = %d (%v), want 6 panel members", len(reads), reads)
	}
}

func TestViewReadyPipelineReadTimeoutDegradesPanel(t *testing.T) {
	period := time.Date(2026, 8, 10, 15, 0, 0, 0, time.UTC)
	storage := newPipelinePeriodStorage("ETH")
	storage.alwaysTimeout = true
	runner := taskrunner.NewService(1, storage, &pipelineFactorExecutor{entered: make(chan string, 8), releaseBTC: closedSignal()},
		taskrunner.WithViewReadConfig(1, 10*time.Millisecond), taskrunner.WithBatchExecution(false))
	executor := trigger.NewViewReadyRunner(
		fakePeriodBindings{items: testBindings()}, fakePeriodFactors{items: testFactors()}, runner, storage, t.TempDir(),
	)
	err := executor.Execute(context.Background(), "space-a", "pipeline-ready-degraded", viewReady(period, "complete"))
	if err != nil {
		t.Fatalf("execute degraded pipeline: %v", err)
	}
	marker := storage.lastMarker()
	if marker.GetStatus() != "degraded" {
		t.Fatalf("marker status = %s, want degraded", marker.GetStatus())
	}
	if got := storage.writtenCombinations(); len(got) != 0 {
		t.Fatalf("written combinations = %v, want none after panel read timeout", got)
	}
}

func viewReady(period time.Time, status string) *publicstoragepb.ViewDataReady {
	return &publicstoragepb.ViewDataReady{
		ViewId: "prices-view", ViewConfigId: "prices-view@1", CompletionEventId: "ready",
		CompletionKind: "event.storage.merge.period.completed",
		DatasetId:      "prices", Status: status, VisibleScope: "view:prices-view",
		Frequency: "1m", PeriodTime: period.Unix(),
		CommittedPositions: []*publicstoragepb.CommittedPosition{{NodeId: "n", StoreId: "s", Sequence: 1}},
		ReadyAt:            timestamppb.New(period),
	}
}

func testBindings() []domain.FactorBinding {
	return []domain.FactorBinding{
		{BindingID: "binding-b", BindingGeneration: "incarnation-b", FactorID: "factor-b", SpaceID: "space-a", SourceViewID: "prices-view", ResultDatasetID: "prices-factor", Freq: "1m", SubjectMode: domain.SubjectModeInclude, SubjectsJSON: `["BTC","ETH","SOL"]`, Status: domain.BindingStatusEnabled},
		{BindingID: "binding-a", BindingGeneration: "incarnation-a", FactorID: "factor-a", SpaceID: "space-a", SourceViewID: "prices-view", ResultDatasetID: "prices-factor", Freq: "1m", SubjectMode: domain.SubjectModeInclude, SubjectsJSON: `["BTC","ETH","SOL"]`, Status: domain.BindingStatusEnabled},
	}
}

func testFactors() map[string]domain.FactorDef {
	return map[string]domain.FactorDef{
		"factor-a": {FactorType: domain.FactorTypeCrossSection, FactorID: "factor-a", Name: "factor-a", SourceHash: "hash-a", InputColumns: []string{"close"}, Outputs: []string{"score_a"}, LookbackPeriods: 1, Status: domain.FactorStatusEnabled},
		"factor-b": {FactorType: domain.FactorTypeCrossSection, FactorID: "factor-b", Name: "factor-b", SourceHash: "hash-b", InputColumns: []string{"close"}, Outputs: []string{"score_b"}, LookbackPeriods: 1, Status: domain.FactorStatusEnabled},
	}
}

type fakePeriodBindings struct{ items []domain.FactorBinding }

func (f fakePeriodBindings) ListExecutable(context.Context) ([]domain.FactorBinding, error) {
	return append([]domain.FactorBinding(nil), f.items...), nil
}

type fakePeriodFactors struct{ items map[string]domain.FactorDef }

func (f fakePeriodFactors) Get(_ context.Context, id string) (*domain.FactorDef, error) {
	item, ok := f.items[id]
	if !ok {
		return nil, errors.New("factor not found")
	}
	return &item, nil
}

type barrierCombinationRunner struct {
	workers    int
	release    chan struct{}
	twoStarted chan struct{}
	once       sync.Once
	mu         sync.Mutex
	planned    []taskrunner.Task
	started    []taskrunner.Task
	active     int
	maxActive  int
}

func newBarrierCombinationRunner(workers int) *barrierCombinationRunner {
	return &barrierCombinationRunner{workers: workers, release: make(chan struct{}), twoStarted: make(chan struct{})}
}

func (r *barrierCombinationRunner) RunAll(_ context.Context, tasks []taskrunner.Task) []taskrunner.Result {
	r.mu.Lock()
	r.planned = append(r.planned, tasks...)
	r.mu.Unlock()
	results := make([]taskrunner.Result, len(tasks))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range r.workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				r.mu.Lock()
				r.started = append(r.started, tasks[index])
				r.active++
				if r.active > r.maxActive {
					r.maxActive = r.active
				}
				if r.active == r.workers {
					r.once.Do(func() { close(r.twoStarted) })
				}
				r.mu.Unlock()
				<-r.release
				results[index].Task = tasks[index]
				results[index].Write = testFactorWrite()
				r.mu.Lock()
				r.active--
				r.mu.Unlock()
			}
		}()
	}
	for index := range tasks {
		jobs <- index
	}
	close(jobs)
	wg.Wait()
	return results
}

func (r *barrierCombinationRunner) activeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active
}

func (r *barrierCombinationRunner) maxActiveCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.maxActive
}

func (r *barrierCombinationRunner) startedCombinations() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return taskCombinations(r.started)
}

func (r *barrierCombinationRunner) plannedCombinations() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return taskCombinations(r.planned)
}

type immediateCombinationRunner struct {
	tasks []taskrunner.Task
	fail  map[string]error
}

func (r *immediateCombinationRunner) RunAll(_ context.Context, tasks []taskrunner.Task) []taskrunner.Result {
	r.tasks = append(r.tasks, tasks...)
	results := make([]taskrunner.Result, len(tasks))
	for index, task := range tasks {
		result := taskrunner.Result{Task: task, Err: r.fail[task.SubjectID+"/"+task.Factor.FactorID]}
		if result.Err == nil {
			result.Write = testFactorWrite()
		}
		results[index] = result
	}
	return results
}

func testFactorWrite() storageio.FactorWrite {
	return storageio.FactorWrite{Rows: 1, Receipts: []storageio.WriteReceipt{{
		CommitID: "test-patch", NodeID: "n", StoreID: "s", Sequence: 1,
	}}}
}

type fakePeriodStorage struct {
	mu      sync.Mutex
	markers []*storagepb.FactorPeriodComputedMarker
	cleared []*engine.FactorTask
}

type pipelinePeriodStorage struct {
	fakePeriodStorage
	muPipeline     sync.Mutex
	timeoutSubject string
	alwaysTimeout  bool
	attempts       map[string]int
	reads          []string
	writes         []string
}

func newPipelinePeriodStorage(timeoutSubject string) *pipelinePeriodStorage {
	return &pipelinePeriodStorage{timeoutSubject: timeoutSubject, attempts: make(map[string]int)}
}

func (p *pipelinePeriodStorage) ReadRangeChunk(context.Context, storageio.WindowKey, time.Time, time.Time, int, int, []string) (*storageio.RangeChunk, error) {
	return nil, errors.New("unexpected range read")
}

func (p *pipelinePeriodStorage) ReadPeriodChunk(ctx context.Context, key storageio.WindowKey, start, _ time.Time, _ int, columns []string) (*storageio.RangeChunk, error) {
	p.muPipeline.Lock()
	p.attempts[key.SubjectID]++
	attempt := p.attempts[key.SubjectID]
	p.reads = append(p.reads, key.SubjectID)
	p.muPipeline.Unlock()
	if key.SubjectID == p.timeoutSubject && (attempt == 1 || p.alwaysTimeout) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	values := make([]any, len(columns))
	for index := range values {
		values[index] = float64(index + 1)
	}
	return &storageio.RangeChunk{
		Frame: &engine.DataFrame{
			Columns: columns, Rows: [][]any{values}, SubjectIDs: []string{key.SubjectID}, DataTimes: []time.Time{start}, SeriesTags: []string{"venue:binance"},
		},
		TargetPeriods: []time.Time{start}, Complete: true,
	}, nil
}

func (p *pipelinePeriodStorage) WriteFactorPatch(_ context.Context, task *engine.FactorTask, result *engine.FactorResult) (uint64, error) {
	p.muPipeline.Lock()
	p.writes = append(p.writes, task.SubjectID+"/"+task.Factor.FactorID)
	p.muPipeline.Unlock()
	return uint64(len(result.Rows)), nil
}

func (p *pipelinePeriodStorage) readOrder() []string {
	p.muPipeline.Lock()
	defer p.muPipeline.Unlock()
	return append([]string(nil), p.reads...)
}

func (p *pipelinePeriodStorage) writtenCombinations() []string {
	p.muPipeline.Lock()
	defer p.muPipeline.Unlock()
	result := append([]string(nil), p.writes...)
	sort.Strings(result)
	return result
}

type pipelineFactorExecutor struct {
	entered    chan string
	releaseBTC chan struct{}
	mu         sync.Mutex
	batchCalls int
	batchItems int
}

func (p *pipelineFactorExecutor) Execute(ctx context.Context, task *engine.FactorTask, frame *engine.DataFrame) (*engine.FactorResult, error) {
	p.entered <- task.SubjectID
	if task.SubjectID == "BTC" {
		select {
		case <-p.releaseBTC:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return panelFactorResult(task, frame), nil
}

func (p *pipelineFactorExecutor) ExecuteBatch(ctx context.Context, batch *engine.BatchTask, frame *engine.DataFrame) (*engine.BatchResult, error) {
	if batch == nil || len(batch.Tasks) == 0 {
		return nil, errors.New("empty factor batch")
	}
	p.entered <- batch.Tasks[0].SubjectID
	if batch.Tasks[0].SubjectID == "BTC" {
		select {
		case <-p.releaseBTC:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	p.mu.Lock()
	p.batchCalls++
	p.batchItems += len(batch.Tasks)
	p.mu.Unlock()
	items := make([]engine.BatchItemResult, 0, len(batch.Tasks))
	for _, task := range batch.Tasks {
		items = append(items, engine.BatchItemResult{
			TaskID: task.TaskID, BindingID: task.BindingID,
			Result: panelFactorResult(&task, frame),
		})
	}
	return &engine.BatchResult{BatchID: batch.BatchID, Items: items}, nil
}

func (p *pipelineFactorExecutor) batchStats() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.batchCalls, p.batchItems
}

func (*pipelineFactorExecutor) Close() error { return nil }

func panelFactorResult(task *engine.FactorTask, frame *engine.DataFrame) *engine.FactorResult {
	result := &engine.FactorResult{Rows: make([]engine.FactorResultRow, 0, len(frame.Rows))}
	for index := range frame.Rows {
		values := make(map[string]any, len(task.Factor.Outputs))
		for _, output := range task.Factor.Outputs {
			values[output] = 1.0
		}
		row := engine.FactorResultRow{DataTime: frame.DataTimes[index], SeriesTag: frame.SeriesTags[index], Values: values}
		if index < len(frame.SubjectIDs) {
			row.SubjectID = frame.SubjectIDs[index]
		}
		result.Rows = append(result.Rows, row)
	}
	return result
}

func closedSignal() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (f *fakePeriodStorage) FactorPeriodComputed(context.Context, string, string, string, int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.markers) > 0, nil
}

func (f *fakePeriodStorage) ReportFactorPeriodComputed(_ context.Context, _ string, marker *storagepb.FactorPeriodComputedMarker) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markers = append(f.markers, marker)
	return nil
}

func (f *fakePeriodStorage) ClearFactorOutputs(_ context.Context, task *engine.FactorTask) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	copyTask := *task
	f.cleared = append(f.cleared, &copyTask)
	return nil
}

func (f *fakePeriodStorage) markerCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.markers)
}

func (f *fakePeriodStorage) lastMarker() *storagepb.FactorPeriodComputedMarker {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.markers) == 0 {
		return nil
	}
	return f.markers[len(f.markers)-1]
}

func (f *fakePeriodStorage) clearedCombinations() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]string, 0, len(f.cleared))
	for _, task := range f.cleared {
		result = append(result, task.SubjectID+"/"+task.Factor.FactorID)
	}
	sort.Strings(result)
	return result
}

func taskCombinations(tasks []taskrunner.Task) []string {
	result := make([]string, 0, len(tasks))
	for _, task := range tasks {
		result = append(result, task.SubjectID+"/"+task.Factor.FactorID)
	}
	return result
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func eventually(timeout time.Duration, condition func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return condition()
}
