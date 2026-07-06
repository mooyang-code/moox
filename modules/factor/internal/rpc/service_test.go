package rpc

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/scheduler"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/mooyang-code/moox/packages/commonpb"
	"gorm.io/gorm"
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

func TestListFactorRunsReturnsPageResult(t *testing.T) {
	ctx := context.Background()
	svc := newRPCTestService(t)
	for i := 0; i < 2; i++ {
		if err := svc.runs.Insert(ctx, domain.FactorRun{
			RunID:         "run-" + string(rune('a'+i)),
			TriggerType:   "event",
			SpaceID:       "crypto",
			SourceDataset: "binance_spot_kline",
			TargetDataset: "binance_spot_factor",
			SubjectID:     "BTC-USDT",
			Freq:          "1m",
			BarTime:       time.Date(2026, 7, 6, 9, 15+i, 0, 0, time.UTC).Format(time.RFC3339),
			FactorCount:   1,
			Status:        domain.RunStatusSucceeded,
		}); err != nil {
			t.Fatalf("insert run: %v", err)
		}
	}

	rsp, err := svc.ListFactorRuns(ctx, &factorpb.ListFactorRunsReq{
		SpaceId:       "crypto",
		SourceDataset: "binance_spot_kline",
		Page:          &commonpb.Page{Page: 1, Size: 1},
	})
	if err != nil {
		t.Fatalf("ListFactorRuns() error = %v", err)
	}
	if len(rsp.GetRuns()) != 1 || rsp.GetPageResult().GetTotal() != 2 || !rsp.GetPageResult().GetHasMore() {
		t.Fatalf("runs/page = %d/%+v", len(rsp.GetRuns()), rsp.GetPageResult())
	}
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
	select {
	case <-sched.done:
	case <-time.After(time.Second):
		t.Fatal("scheduler drain was not called")
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
	progress, err := svc.GetRecalcProgress(ctx, &factorpb.GetRecalcProgressReq{RecalcId: rsp.GetRecalcId()})
	if err != nil {
		t.Fatalf("GetRecalcProgress() error = %v", err)
	}
	if progress.GetStatus() != "succeeded" || progress.GetTotal() != 1 || progress.GetFinished() != 1 {
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
}

func newFakeRPCScheduler() *fakeRPCScheduler {
	return &fakeRPCScheduler{done: make(chan struct{})}
}

func (f *fakeRPCScheduler) Status() scheduler.Status {
	return scheduler.Status{QueueDepth: len(f.tasks)}
}

func (f *fakeRPCScheduler) Enqueue(_ context.Context, task scheduler.Task) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tasks = append(f.tasks, task)
}

func (f *fakeRPCScheduler) Drain(context.Context) error {
	f.once.Do(func() {
		close(f.done)
	})
	return nil
}

func openRPCTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Exec(factorschema.AllSQL()).Error; err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return db
}
