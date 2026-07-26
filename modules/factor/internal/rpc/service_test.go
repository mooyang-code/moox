package rpc

import (
	"context"
	"fmt"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/scheduler"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"trpc.group/trpc-go/trpc-go/codec"
)

func TestCreateFactorValidationReturnsInvalidParam(t *testing.T) {
	svc := newRPCTestService(t)

	rsp, err := svc.CreateFactor(context.Background(), &factorpb.CreateFactorReq{})
	if err != nil {
		t.Fatalf("CreateFactor() error = %v", err)
	}
	if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_INVALID_PARAM {
		t.Fatalf("ret = %+v", rsp.GetRetInfo())
	}
}

func TestCreateFactorSuccess(t *testing.T) {
	svc := newRPCTestService(t)

	rsp, err := svc.CreateFactor(context.Background(), &factorpb.CreateFactorReq{Factor: &factorpb.FactorDef{
		FactorId:      "bias",
		Name:          "Bias",
		SourceCode:    "def signal(*args): return args[0]",
		ParamsJson:    "[20]",
		LookbackBars:  200,
		WritebackBars: 5,
		Status:        "enabled",
	}})
	if err != nil {
		t.Fatalf("CreateFactor() error = %v", err)
	}
	if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		t.Fatalf("ret = %+v", rsp.GetRetInfo())
	}
	if rsp.GetFactor().GetFactorId() != "bias" || rsp.GetFactor().GetSourceHash() == "" {
		t.Fatalf("factor = %+v", rsp.GetFactor())
	}
}

func TestCreateFactorWritesSourceAndDepends(t *testing.T) {
	dir := t.TempDir()
	svc := NewWithRuntime(openRPCTestDB(t), nil, nil, WithFactorsDir(dir))
	source := "extra_data_dict = {'coin-cap': ['circulating_supply']}\n\ndef signal(df, n, column):\n    return df\n"

	rsp, err := svc.CreateFactor(context.Background(), &factorpb.CreateFactorReq{Factor: &factorpb.FactorDef{
		FactorId:     "circulating_mcap",
		Name:         "CirculatingMcap",
		SourceCode:   source,
		ParamsJson:   "[20]",
		LookbackBars: 200,
		Status:       "enabled",
	}})
	if err != nil {
		t.Fatalf("CreateFactor() error = %v", err)
	}
	if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		t.Fatalf("ret = %+v", rsp.GetRetInfo())
	}
	raw, err := os.ReadFile(filepath.Join(dir, "CirculatingMcap.py"))
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	if string(raw) != source {
		t.Fatalf("written source = %q", string(raw))
	}
	if got := rsp.GetFactor().GetDependsJson(); got != `{"extra_columns":["circulating_supply"]}` {
		t.Fatalf("depends_json = %s", got)
	}
}

func TestUpsertBindingSuccess(t *testing.T) {
	svc := newRPCTestService(t)
	_, err := svc.CreateFactor(context.Background(), &factorpb.CreateFactorReq{Factor: &factorpb.FactorDef{
		FactorId:     "bias",
		Name:         "Bias",
		SourceCode:   "x",
		ParamsJson:   "[20]",
		LookbackBars: 200,
		Status:       "enabled",
	}})
	if err != nil {
		t.Fatalf("CreateFactor() error = %v", err)
	}

	rsp, err := svc.UpsertBinding(context.Background(), &factorpb.UpsertBindingReq{Binding: &factorpb.FactorBinding{
		FactorId:      "bias",
		SpaceId:       "crypto",
		SourceDataset: "binance_spot_kline",
		Freq:          "1m",
		SubjectMode:   "all",
		TargetDataset: "binance_spot_factor",
		Status:        "enabled",
	}})
	if err != nil {
		t.Fatalf("UpsertBinding() error = %v", err)
	}
	if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		t.Fatalf("ret = %+v", rsp.GetRetInfo())
	}
	if rsp.GetBinding().GetBindingId() == "" {
		t.Fatalf("binding id was not generated: %+v", rsp.GetBinding())
	}
}

func TestListFactorRunsReturnsEmptyPageWithoutPersistentRunStore(t *testing.T) {
	ctx := context.Background()
	svc := NewWithRuntime(openEmptyRPCTestDB(t), nil, nil)

	rsp, err := svc.ListFactorRuns(ctx, &factorpb.ListFactorRunsReq{
		SpaceId:       "crypto",
		SourceDataset: "binance_spot_kline",
		Page:          &commonpb.Page{Page: 1, Size: 1},
	})
	if err != nil {
		t.Fatalf("ListFactorRuns() error = %v", err)
	}
	if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		t.Fatalf("ret = %+v", rsp.GetRetInfo())
	}
	if len(rsp.GetRuns()) != 0 || rsp.GetPageResult().GetTotal() != 0 || rsp.GetPageResult().GetHasMore() {
		t.Fatalf("runs/page = %d/%+v", len(rsp.GetRuns()), rsp.GetPageResult())
	}
}

func openEmptyRPCTestDB(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	if err != nil {
		t.Fatalf("open empty store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestRecalcFactorEnqueuesSchedulerTask(t *testing.T) {
	ctx := context.Background()
	sched := newFakeRPCScheduler()
	svc := NewWithRuntime(openRPCTestDB(t), sched, nil, WithFactorsDir(t.TempDir()))
	_, err := svc.CreateFactor(ctx, &factorpb.CreateFactorReq{Factor: &factorpb.FactorDef{
		FactorId:      "circulating_mcap",
		Name:          "CirculatingMcap",
		SourceCode:    "extra_data_dict = {'coin-cap': ['circulating_supply']}\n\ndef signal(df, n, column):\n    return df\n",
		ParamsJson:    "[20]",
		LookbackBars:  200,
		WritebackBars: 5,
		Status:        "enabled",
	}})
	if err != nil {
		t.Fatalf("CreateFactor() error = %v", err)
	}
	_, err = svc.UpsertBinding(ctx, &factorpb.UpsertBindingReq{Binding: &factorpb.FactorBinding{
		FactorId:      "circulating_mcap",
		SpaceId:       "crypto",
		SourceDataset: "binance_spot_kline",
		Freq:          "1m",
		TargetDataset: "binance_spot_factor",
		Status:        "enabled",
	}})
	if err != nil {
		t.Fatalf("UpsertBinding() error = %v", err)
	}

	requestCtx, requestMsg := codec.WithNewMessage(ctx)
	requestMsg.WithCalleeMethod("factor.RecalcFactor")
	requestCtx, cancelRequest := context.WithCancel(requestCtx)
	rsp, err := svc.RecalcFactor(requestCtx, &factorpb.RecalcFactorReq{
		SpaceId:       "crypto",
		SourceDataset: "binance_spot_kline",
		SubjectId:     "BTC-USDT",
		Freq:          "1m",
		EndTime:       "2026-07-06T09:15:00Z",
	})
	if err != nil {
		t.Fatalf("RecalcFactor() error = %v", err)
	}
	cancelRequest()
	if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		t.Fatalf("ret = %+v", rsp.GetRetInfo())
	}
	select {
	case <-sched.done:
	case <-time.After(time.Second):
		t.Fatal("scheduler drain was not called")
	}
	if err := sched.drainContextErr(); err != nil {
		t.Fatalf("asynchronous drain inherited request cancellation: %v", err)
	}
	if got := sched.drainCalleeMethod(); got != "factor.RecalcFactor" {
		t.Fatalf("asynchronous drain callee method = %q", got)
	}
	if len(sched.tasks) != 1 {
		t.Fatalf("tasks = %+v", sched.tasks)
	}
	task := sched.tasks[0]
	if task.TriggerType != "recalc" || task.TargetDataset != "binance_spot_factor" || len(task.Factors) != 1 {
		t.Fatalf("task = %+v", task)
	}
	if got := task.Factors[0].ExtraColumns; len(got) != 1 || got[0] != "circulating_supply" {
		t.Fatalf("extra columns = %#v", got)
	}
	progress := waitRecalcProgress(t, ctx, svc, rsp.GetRecalcId())
	if progress.GetStatus() != "succeeded" || progress.GetTotal() != 1 || progress.GetFinished() != 1 {
		t.Fatalf("progress = %+v", progress)
	}
}

func TestRecalcProgressUsesOwnTaskResultsWhenAnotherDrainRuns(t *testing.T) {
	ctx := context.Background()
	sched := newExternallyDrainedScheduler(fmt.Errorf("external task failed"))
	externalDone := make(chan error, 1)
	go func() {
		externalDone <- sched.Drain(ctx)
	}()
	select {
	case <-sched.externalStarted:
	case <-time.After(time.Second):
		t.Fatal("external drain did not start")
	}

	svc := NewWithRuntime(openRPCTestDB(t), sched, nil, WithFactorsDir(t.TempDir()))
	_, err := svc.CreateFactor(ctx, &factorpb.CreateFactorReq{Factor: &factorpb.FactorDef{
		FactorId:      "bias",
		Name:          "Bias",
		SourceCode:    "def signal(df, n, column): return df\n",
		ParamsJson:    "[20]",
		LookbackBars:  200,
		WritebackBars: 5,
		Status:        "enabled",
	}})
	if err != nil {
		t.Fatalf("CreateFactor() error = %v", err)
	}
	_, err = svc.UpsertBinding(ctx, &factorpb.UpsertBindingReq{Binding: &factorpb.FactorBinding{
		FactorId:      "bias",
		SpaceId:       "crypto",
		SourceDataset: "binance_spot_kline",
		Freq:          "1m",
		TargetDataset: "binance_spot_factor",
		Status:        "enabled",
	}})
	if err != nil {
		t.Fatalf("UpsertBinding() error = %v", err)
	}

	rsp, err := svc.RecalcFactor(ctx, &factorpb.RecalcFactorReq{
		SpaceId:       "crypto",
		SourceDataset: "binance_spot_kline",
		SubjectId:     "BTC-USDT",
		Freq:          "1m",
		EndTime:       "2026-07-06T09:15:00Z",
	})
	if err != nil {
		t.Fatalf("RecalcFactor() error = %v", err)
	}
	if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		t.Fatalf("ret = %+v", rsp.GetRetInfo())
	}
	close(sched.releaseExternal)
	if err := <-externalDone; err == nil {
		t.Fatal("external Drain() error = nil, want failure")
	}
	progress := waitRecalcProgress(t, ctx, svc, rsp.GetRecalcId())
	if progress.GetStatus() != "failed: external task failed" || progress.GetFinished() != 1 {
		t.Fatalf("progress = %+v", progress)
	}
}

func TestRecalcFactorRejectsStartTimeRange(t *testing.T) {
	svc := NewWithRuntime(openRPCTestDB(t), newFakeRPCScheduler(), nil, WithFactorsDir(t.TempDir()))

	rsp, err := svc.RecalcFactor(context.Background(), &factorpb.RecalcFactorReq{
		SpaceId:       "crypto",
		SourceDataset: "binance_spot_kline",
		SubjectId:     "BTC-USDT",
		Freq:          "1m",
		StartTime:     "2026-07-06T09:00:00Z",
		EndTime:       "2026-07-06T09:15:00Z",
	})
	if err != nil {
		t.Fatalf("RecalcFactor() error = %v", err)
	}
	if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_INVALID_PARAM {
		t.Fatalf("ret = %+v, want invalid param", rsp.GetRetInfo())
	}
}

func newRPCTestService(t *testing.T) *Service {
	t.Helper()
	return NewWithRuntime(openRPCTestDB(t), nil, nil, WithFactorsDir(t.TempDir()))
}

type fakeRPCScheduler struct {
	mu    sync.Mutex
	once  sync.Once
	done  chan struct{}
	tasks []scheduler.Task
	ctx   context.Context
}

func newFakeRPCScheduler() *fakeRPCScheduler {
	return &fakeRPCScheduler{done: make(chan struct{})}
}

func (f *fakeRPCScheduler) Status() scheduler.Status {
	return scheduler.Status{QueueDepth: len(f.tasks)}
}

func (f *fakeRPCScheduler) EnqueueChecked(_ context.Context, task scheduler.Task) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tasks = append(f.tasks, task)
	return nil
}

func (f *fakeRPCScheduler) Drain(ctx context.Context) error {
	f.mu.Lock()
	f.ctx = ctx
	tasks := append([]scheduler.Task(nil), f.tasks...)
	f.mu.Unlock()
	for _, task := range tasks {
		if task.Completion != nil {
			task.Completion <- scheduler.TaskResult{TaskID: task.TaskID, Status: "succeeded"}
		}
	}
	f.once.Do(func() {
		close(f.done)
	})
	return nil
}

func (f *fakeRPCScheduler) drainContextErr() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ctx.Err()
}

func (f *fakeRPCScheduler) drainCalleeMethod() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return codec.Message(f.ctx).CalleeMethod()
}

type externallyDrainedScheduler struct {
	mu              sync.Mutex
	drainMu         sync.Mutex
	externalStarted chan struct{}
	releaseExternal chan struct{}
	failErr         error
	tasks           []scheduler.Task
	drains          int
}

func newExternallyDrainedScheduler(failErr error) *externallyDrainedScheduler {
	return &externallyDrainedScheduler{
		externalStarted: make(chan struct{}),
		releaseExternal: make(chan struct{}),
		failErr:         failErr,
	}
}

func (s *externallyDrainedScheduler) Status() scheduler.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return scheduler.Status{QueueDepth: len(s.tasks)}
}

func (s *externallyDrainedScheduler) EnqueueChecked(_ context.Context, task scheduler.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks = append(s.tasks, task)
	return nil
}

func (s *externallyDrainedScheduler) Drain(context.Context) error {
	s.drainMu.Lock()
	defer s.drainMu.Unlock()
	s.drains++
	if s.drains == 1 {
		close(s.externalStarted)
		<-s.releaseExternal
		s.mu.Lock()
		tasks := append([]scheduler.Task(nil), s.tasks...)
		s.tasks = nil
		s.mu.Unlock()
		for _, task := range tasks {
			if task.Completion != nil {
				task.Completion <- scheduler.TaskResult{TaskID: task.TaskID, Status: "failed", Error: s.failErr}
			}
		}
		return s.failErr
	}
	return nil
}

func waitRecalcProgress(t *testing.T, ctx context.Context, svc *Service, recalcID string) *factorpb.GetRecalcProgressRsp {
	t.Helper()
	deadline := time.After(time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		progress, err := svc.GetRecalcProgress(ctx, &factorpb.GetRecalcProgressReq{RecalcId: recalcID})
		if err != nil {
			t.Fatalf("GetRecalcProgress() error = %v", err)
		}
		if progress.GetStatus() != "running" && progress.GetStatus() != "queued" {
			return progress
		}
		select {
		case <-deadline:
			t.Fatalf("recalc progress did not finish: %+v", progress)
		case <-tick.C:
		}
	}
}

func openRPCTestDB(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := db.ApplySchema(factorschema.AllSQL()); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestFactorCRUD_AndBindingsLifecycle(t *testing.T) {
	svc := newRPCTestService(t)
	ctx := context.Background()

	create, err := svc.CreateFactor(ctx, &factorpb.CreateFactorReq{Factor: &factorpb.FactorDef{
		FactorId: "bias", Name: "Bias", SourceCode: "def signal(df): return df", ParamsJson: "[20]", Status: "enabled",
	}})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, create.GetRetInfo().GetCode())

	got, err := svc.GetFactor(ctx, &factorpb.GetFactorReq{FactorId: "bias"})
	require.NoError(t, err)
	assert.Equal(t, "bias", got.GetFactor().GetFactorId())

	_, err = svc.GetFactor(ctx, &factorpb.GetFactorReq{})
	require.NoError(t, err)

	update, err := svc.UpdateFactor(ctx, &factorpb.UpdateFactorReq{FactorId: "bias", Factor: &factorpb.FactorDef{
		Name: "Bias", SourceCode: "def signal(df, n): return df", ParamsJson: "[10]", Status: "enabled",
	}})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, update.GetRetInfo().GetCode())

	listed, err := svc.ListFactors(ctx, &factorpb.ListFactorsReq{Page: &commonpb.Page{Page: 1, Size: 10}})
	require.NoError(t, err)
	assert.NotEmpty(t, listed.GetFactors())

	status, err := svc.SetFactorStatus(ctx, &factorpb.SetFactorStatusReq{FactorId: "bias", Status: "disabled"})
	require.NoError(t, err)
	assert.Equal(t, "disabled", status.GetFactor().GetStatus())

	_, err = svc.SetFactorStatus(ctx, &factorpb.SetFactorStatusReq{})
	require.NoError(t, err)

	_, err = svc.CreateFactor(ctx, &factorpb.CreateFactorReq{Factor: &factorpb.FactorDef{
		FactorId: "bias2", Name: "Bias", SourceCode: "x", Status: "enabled",
	}})
	require.NoError(t, err)

	bind, err := svc.UpsertBinding(ctx, &factorpb.UpsertBindingReq{Binding: &factorpb.FactorBinding{
		FactorId: "bias", SpaceId: "crypto", SourceDataset: "kline", Freq: "1m", Status: "enabled",
	}})
	require.NoError(t, err)
	bindingID := bind.GetBinding().GetBindingId()
	require.NotEmpty(t, bindingID)

	bindings, err := svc.ListBindings(ctx, &factorpb.ListBindingsReq{SpaceId: "crypto", Page: &commonpb.Page{Page: 1, Size: 10}})
	require.NoError(t, err)
	assert.NotEmpty(t, bindings.GetBindings())

	del, err := svc.DeleteBinding(ctx, &factorpb.DeleteBindingReq{BindingId: bindingID})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, del.GetRetInfo().GetCode())

	_, err = svc.DeleteBinding(ctx, &factorpb.DeleteBindingReq{})
	require.NoError(t, err)
}

func TestGetEngineStatus_WithRuntimeProviders(t *testing.T) {
	db := openRPCTestDB(t)
	sched := newFakeRPCScheduler()
	eng := &fakeEngineStatus{workers: 2}
	svc := NewWithRuntime(db, sched, eng, WithFactorsDir(t.TempDir()))

	rsp, err := svc.GetEngineStatus(context.Background(), &factorpb.GetEngineStatusReq{})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Len(t, rsp.GetWorkers(), 2)
	assert.Equal(t, int32(sched.Status().QueueDepth), rsp.GetQueueDepth())
}

func TestGetRecalcProgress_Validation(t *testing.T) {
	svc := newRPCTestService(t)
	rsp, err := svc.GetRecalcProgress(context.Background(), &factorpb.GetRecalcProgressReq{})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())

	rsp, err = svc.GetRecalcProgress(context.Background(), &factorpb.GetRecalcProgressReq{RecalcId: "missing"})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestRecalcFactor_ValidationBranches(t *testing.T) {
	svc := NewWithRuntime(openRPCTestDB(t), nil, nil)
	rsp, err := svc.RecalcFactor(context.Background(), &factorpb.RecalcFactorReq{
		SpaceId: "s", SourceDataset: "d", Freq: "1m", SubjectId: "BTC",
	})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())

	svc = newRPCTestService(t)
	rsp, err = svc.RecalcFactor(context.Background(), &factorpb.RecalcFactorReq{})
	require.NoError(t, err)
	assert.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())

	rsp, err = svc.RecalcFactor(context.Background(), &factorpb.RecalcFactorReq{
		SpaceId: "s", SourceDataset: "d", Freq: "1m",
	})
	require.NoError(t, err)
	assert.Contains(t, rsp.GetRetInfo().GetMsg(), "subject_id")
}

type fakeEngineStatus struct {
	workers int
}

func (f *fakeEngineStatus) Status() engine.WorkerPoolStatus {
	return engine.WorkerPoolStatus{Workers: f.workers}
}
