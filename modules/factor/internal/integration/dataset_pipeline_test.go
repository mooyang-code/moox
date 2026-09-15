package integration

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/inputcache"
	"github.com/mooyang-code/moox/modules/factor/internal/merge"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/modules/factor/internal/taskrunner"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	publicstoragepb "github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestDatasetPipeline(t *testing.T) {
	t.Run("independent_programs_and_docs", testDatasetPipelineDocsAndClose)
	t.Run("two_sources_two_subjects", testDatasetPipelineTwoSources)
	t.Run("source_timeout_degraded", testDatasetPipelineTimeout)
	t.Run("kill_and_recover", testDatasetPipelineRestart)
	t.Run("cache_full_falls_back", testDatasetPipelineCacheFull)
	t.Run("view_rebuild_does_not_block_timeseries", testDatasetPipelineViewRebuild)
	t.Run("timeseries_cross_section_strategy", testDatasetPipelineComputeAndStrategy)
}

func testDatasetPipelineDocsAndClose(t *testing.T) {
	root := factorModuleRoot(t)
	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	require.NoError(t, err)
	text := string(readme)
	require.NotContains(t, text, "ViewSourceSubjectReady")
	require.NotContains(t, text, "ViewSourcePeriodReady")
	require.NotContains(t, text, "ViewFactorPeriodReady")
	require.Contains(t, text, "moox-factor-merge")
	require.Contains(t, text, "moox-factor-engine")
	require.Contains(t, text, "DatasetRowsUpserted")
	require.Contains(t, text, "ViewDataReady")
	require.DirExists(t, filepath.Join(root, "cmd", "server"))
	require.DirExists(t, filepath.Join(root, "cmd", "engine"))
	require.DirExists(t, filepath.Join(root, "cmd", "merge"))

	ledger, err := merge.Open(merge.Options{Path: filepath.Join(t.TempDir(), "merge.db")})
	require.NoError(t, err)
	require.NoError(t, ledger.Close())
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "control.db")})
	require.NoError(t, err)
	require.NoError(t, db.Close())
}

func testDatasetPipelineTwoSources(t *testing.T) {
	assembler, commits, _ := openPipelineAssembler(t, t.TempDir())
	period := time.Date(2026, 9, 14, 1, 0, 0, 0, time.UTC)
	btc := pipelineKey("BTC-USDT", period)
	eth := pipelineKey("ETH-USDT", period)
	require.NoError(t, assembler.ApplyArrival(context.Background(), btc, "dataset_binance_spot_kline_1m", completeKline()))
	require.NoError(t, assembler.ApplyArrival(context.Background(), eth, "dataset_binance_spot_kline_1m", completeKline()))
	require.Empty(t, commits.ids)
	require.NoError(t, assembler.ApplyArrival(context.Background(), btc, "dataset_binance_swap_kline_1m", completeKline()))
	require.NoError(t, assembler.ApplyArrival(context.Background(), eth, "dataset_binance_swap_kline_1m", completeKline()))
	require.Len(t, commits.ids, 2)
	require.True(t, commits.ready[0] && commits.ready[1])
}

func testDatasetPipelineTimeout(t *testing.T) {
	assembler, commits, ledger := openPipelineAssembler(t, t.TempDir())
	reports := new(pipelineReporter)
	periods, err := merge.NewPeriodLedger(ledger, reports)
	require.NoError(t, err)
	assembler.SetPeriodLedger(periods)
	period := time.Date(2026, 9, 14, 1, 1, 0, 0, time.UTC)
	key := pipelineKey("BTC-USDT", period)
	periodKey := merge.PeriodKey{DatasetID: key.DatasetID, SnapshotID: key.SnapshotID, Frequency: key.Frequency, PeriodTime: period}
	require.NoError(t, periods.Freeze(context.Background(), periodKey, []string{"BTC-USDT", "ETH-USDT"}, period.Add(time.Minute)))
	require.NoError(t, assembler.ApplyArrival(context.Background(), key, "dataset_binance_spot_kline_1m", completeKline()))
	require.NoError(t, assembler.ApplyArrival(context.Background(), key, "dataset_binance_swap_kline_1m", completeKline()))
	require.Len(t, commits.ids, 1)
	require.NoError(t, periods.Finalize(context.Background(), periodKey, period.Add(2*time.Minute)))
	require.Len(t, reports.markers, 1)
	require.Equal(t, "degraded", reports.markers[0].Status)
	require.Equal(t, []string{"ETH-USDT"}, reports.markers[0].FailedSubjects)
	accepted, err := periods.Accepts(context.Background(), periodKey, "ETH-USDT")
	require.NoError(t, err)
	require.True(t, accepted, "late dual-source rows may still CommitInput after merge report")
	eth := pipelineKey("ETH-USDT", period)
	require.NoError(t, assembler.ApplyArrival(context.Background(), eth, "dataset_binance_spot_kline_1m", completeKline()))
	require.NoError(t, assembler.ApplyArrival(context.Background(), eth, "dataset_binance_swap_kline_1m", completeKline()))
	require.Len(t, commits.ids, 2)
	require.Len(t, reports.markers, 1)
	require.Equal(t, []string{"ETH-USDT"}, reports.markers[0].FailedSubjects)
}

func testDatasetPipelineRestart(t *testing.T) {
	dir := t.TempDir()
	first, _, _ := openPipelineAssembler(t, dir)
	period := time.Date(2026, 9, 14, 1, 2, 0, 0, time.UTC)
	key := pipelineKey("BTC-USDT", period)
	require.NoError(t, first.ApplyArrival(context.Background(), key, "dataset_binance_spot_kline_1m", completeKline()))
	second, commits, _ := openPipelineAssembler(t, dir)
	require.NoError(t, second.ApplyArrival(context.Background(), key, "dataset_binance_swap_kline_1m", completeKline()))
	require.Len(t, commits.ids, 1)
}

func testDatasetPipelineCacheFull(t *testing.T) {
	ctx := context.Background()
	cfg := inputcache.DefaultConfig()
	cfg.Dir = t.TempDir()
	cfg.MaxBytes = 1
	cfg.RebuildKeepRows = 1
	generation, err := inputcache.NewGeneration(ctx, cfg.Dir, []inputcache.Column{{Name: "id", Type: "VARCHAR"}}, []string{"id"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, generation.Close()) })
	require.NoError(t, generation.Use(func(db *inputcache.Database, _ uint64) error {
		return db.Upsert(ctx, [][]any{{"BTC"}, {"ETH"}}, time.Now())
	}))
	result, err := inputcache.MaintainCapacity(ctx, cfg, []*inputcache.Generation{generation})
	require.NoError(t, err)
	require.True(t, result.PauseWrites, "full cache must pause writes so readers fall back to Storage")
}

func testDatasetPipelineViewRebuild(t *testing.T) {
	db := openPipelineStore(t)
	runner := new(recordingRunner)
	period := time.Date(2026, 9, 14, 1, 4, 0, 0, time.UTC)
	rows := trigger.NewDatasetRowsRunner(db.Bindings(), db.Factors(), runner, db, t.TempDir()).WithNow(func() time.Time { return period.Add(30 * time.Second) })
	require.NoError(t, rows.HandleDatasetRows(context.Background(), "merge-rebuild", readyRows("BTC-USDT", period)))
	require.Len(t, runner.tasks, 1)
	require.Zero(t, runner.tasks[0].ExpectedActiveIndexRevision, "timeseries must not fence a View rebuild generation")
}

func testDatasetPipelineComputeAndStrategy(t *testing.T) {
	db := openPipelineStore(t)
	tsRunner := new(recordingRunner)
	period := time.Date(2026, 9, 14, 1, 3, 0, 0, time.UTC)
	rows := trigger.NewDatasetRowsRunner(db.Bindings(), db.Factors(), tsRunner, db, t.TempDir()).WithNow(func() time.Time { return period.Add(30 * time.Second) })
	require.NoError(t, rows.HandleDatasetRows(context.Background(), "merge-btc", readyRows("BTC-USDT", period)))
	require.NoError(t, rows.HandleDatasetRows(context.Background(), "merge-eth", readyRows("ETH-USDT", period)))
	require.Len(t, tsRunner.tasks, 2)
	patch := readyRows("BTC-USDT", period)
	patch.WriteKind = "factor_patch"
	require.NoError(t, rows.HandleDatasetRows(context.Background(), "factor-writeback", patch))
	require.Len(t, tsRunner.tasks, 2, "factor write-back must not retrigger timeseries")

	xsRunner := new(recordingRunner)
	storage := new(pipelinePeriodStorage)
	cross := trigger.NewViewReadyRunner(db.Bindings(), db.Factors(), xsRunner, storage, t.TempDir())
	require.NoError(t, cross.Execute(context.Background(), "crypto", "result-ready", viewReady(period, events.FactorPeriodComputed.Name())))
	require.Empty(t, xsRunner.tasks, "result ViewDataReady must not start a new cross-section")
	require.NoError(t, cross.Execute(context.Background(), "crypto", "merge-ready", viewReady(period, events.MergePeriodCompleted.Name())))
	require.NotEmpty(t, xsRunner.tasks)
	require.False(t, trigger.AcceptsStrategyViewReady(events.MergePeriodCompleted.Name(), true))
	require.True(t, trigger.AcceptsStrategyViewReady(events.FactorPeriodComputed.Name(), true))
}

type recordingRunner struct {
	tasks []taskrunner.Task
}

func (r *recordingRunner) RunAll(_ context.Context, tasks []taskrunner.Task) []taskrunner.Result {
	r.tasks = append(r.tasks, tasks...)
	out := make([]taskrunner.Result, len(tasks))
	for i, task := range tasks {
		out[i] = taskrunner.Result{Task: task}
	}
	return out
}

type pipelinePeriodStorage struct {
	marker *storagepb.FactorPeriodComputedMarker
}

func (p *pipelinePeriodStorage) FactorPeriodComputed(context.Context, string, string, string, int64) (bool, error) {
	return false, nil
}

func (p *pipelinePeriodStorage) ReportFactorPeriodComputed(_ context.Context, _ string, marker *storagepb.FactorPeriodComputedMarker) error {
	p.marker = marker
	return nil
}

func (p *pipelinePeriodStorage) ClearFactorOutputs(context.Context, *engine.FactorTask) error {
	return nil
}

type commitRecorder struct {
	ids   []string
	ready []bool
}

func (c *commitRecorder) CommitInput(_ context.Context, commitID string, _ merge.RowKey, _ map[string]float64, ready bool) (merge.WriteReceipt, error) {
	c.ids = append(c.ids, commitID)
	c.ready = append(c.ready, ready)
	return merge.WriteReceipt{CommitID: commitID, NodeID: "n", StoreID: "s", Sequence: uint64(len(c.ids))}, nil
}

type pipelineReporter struct {
	markers []merge.PeriodMarker
}

func (r *pipelineReporter) Report(_ context.Context, marker merge.PeriodMarker) error {
	r.markers = append(r.markers, marker)
	return nil
}

func openPipelineAssembler(t *testing.T, dir string) (*merge.Assembler, *commitRecorder, *merge.Ledger) {
	t.Helper()
	ledger, err := merge.Open(merge.Options{Path: filepath.Join(dir, "merge.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ledger.Close() })
	commits := &commitRecorder{}
	assembler, err := merge.NewAssembler(ledger, pipelineDefinition(), commits)
	require.NoError(t, err)
	return assembler, commits, ledger
}

func openPipelineStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "engine.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	require.NoError(t, db.Factors().Create(context.Background(), domain.FactorDef{
		FactorID: "bias5", Name: "bias5", FactorType: domain.FactorTypeTimeSeries,
		SourceCode: "def compute(df, params, context):\n    return df", SourceHash: "hash",
		InputColumns: []string{"dataset_binance_spot_kline_1m__close"}, Outputs: []string{"bias5"},
		LookbackPeriods: 2, ParamsJSON: "{}", Status: domain.FactorStatusEnabled,
	}))
	require.NoError(t, db.Factors().Create(context.Background(), domain.FactorDef{
		FactorID: "rank", Name: "rank", FactorType: domain.FactorTypeCrossSection,
		SourceCode: "def compute(df, params, context):\n    return df", SourceHash: "hash",
		InputColumns: []string{"dataset_binance_spot_kline_1m__close"}, Outputs: []string{"rank"},
		LookbackPeriods: 1, ParamsJSON: "{}", Status: domain.FactorStatusEnabled,
	}))
	require.NoError(t, db.Bindings().Upsert(context.Background(), domain.FactorBinding{
		BindingID: "ts-binding", FactorID: "bias5", SpaceID: "crypto",
		SourceViewID: "view_binance_kline_1m", ResultDatasetID: "mdataset_binance_kline_1m", ResultViewID: "view_binance_kline_1m",
		Freq: "1m", SubjectMode: domain.SubjectModeAll, Status: domain.BindingStatusEnabled,
	}))
	require.NoError(t, db.Bindings().Upsert(context.Background(), domain.FactorBinding{
		BindingID: "xs-binding", FactorID: "rank", SpaceID: "crypto",
		SourceViewID: "view_binance_kline_1m", ResultDatasetID: "mdataset_binance_kline_1m", ResultViewID: "view_binance_kline_1m",
		Freq: "1m", SubjectMode: domain.SubjectModeInclude, SubjectsJSON: `["BTC-USDT","ETH-USDT"]`, Status: domain.BindingStatusEnabled,
	}))
	return db
}

func pipelineDefinition() domain.MergedDataset {
	spot := "dataset_binance_spot_kline_1m"
	swap := "dataset_binance_swap_kline_1m"
	fields := []string{"open", "high", "low", "close", "volume", "quote_volume", "trade_num"}
	def := domain.MergedDataset{
		DatasetID: "mdataset_binance_kline_1m", SpaceID: "crypto", Frequency: "1m", MergeMode: domain.MergeModeSystem,
		ConfigSnapshotID: "snap-1",
		KeyContract: domain.KeyContract{
			SubjectID: "subject_id", Frequency: "frequency", PeriodTime: "period_time", SeriesTag: "series_tag", PeriodBoundary: "close",
		},
		Sources: []domain.SourceDatasetRef{
			{DatasetID: spot, Frequency: "1m", PeriodBoundary: "close", Fields: append([]string(nil), fields...)},
			{DatasetID: swap, Frequency: "1m", PeriodBoundary: "close", Fields: append([]string(nil), fields...)},
		},
	}
	for _, source := range def.Sources {
		for _, field := range source.Fields {
			def.FieldMappings = append(def.FieldMappings, domain.FieldMapping{
				SourceDatasetID: source.DatasetID, SourceField: field, TargetField: domain.MappedSourceField(source.DatasetID, field),
			})
		}
	}
	return def
}

func pipelineKey(subject string, period time.Time) merge.RowKey {
	return merge.RowKey{
		DatasetID: "mdataset_binance_kline_1m", SnapshotID: "snap-1", SubjectID: subject,
		Frequency: "1m", PeriodTime: period.UTC(), SeriesTag: "default",
	}
}

func completeKline() map[string]float64 {
	return map[string]float64{"open": 1, "high": 2, "low": 0.5, "close": 1.5, "volume": 10, "quote_volume": 20, "trade_num": 3}
}

func readyRows(subject string, period time.Time) *publicstoragepb.DatasetRowsUpserted {
	return &publicstoragepb.DatasetRowsUpserted{
		SpaceId: "crypto", DatasetId: "mdataset_binance_kline_1m", WriteKind: "input_commit",
		SourceNodeId: "node-1", SourceStoreId: "store-1", SourceSequence: 1,
		Rows: []*publicstoragepb.RowUpsert{{
			Key: &publicstoragepb.RowKey{
				SpaceId: "crypto", DatasetId: "mdataset_binance_kline_1m",
				Kind: &publicstoragepb.RowKey_TimeSeries{TimeSeries: &publicstoragepb.TimeSeriesRowKey{
					SubjectId: subject, Freq: "1m", DataTime: period.Format(time.RFC3339Nano),
				}},
			},
			Fields: []*publicstoragepb.FieldValue{{
				FieldId: "dataset_binance_spot_kline_1m__close",
				Value:   &publicstoragepb.TypedValue{Value: &publicstoragepb.TypedValue_DoubleValue{DoubleValue: 1.25}},
			}},
			Attributes: map[string]*publicstoragepb.TypedValue{
				"moox.input_ready": {Value: &publicstoragepb.TypedValue_BoolValue{BoolValue: true}},
				"moox.commit_id":   {Value: &publicstoragepb.TypedValue_StringValue{StringValue: "commit-" + subject}},
			},
		}},
	}
}

func viewReady(period time.Time, kind string) *publicstoragepb.ViewDataReady {
	return &publicstoragepb.ViewDataReady{
		ViewId: "view_binance_kline_1m", ViewConfigId: "view_binance_kline_1m@1", CompletionEventId: "ready",
		CompletionKind: kind, DatasetId: "mdataset_binance_kline_1m", Status: "complete",
		VisibleScope: "view:view_binance_kline_1m", Frequency: "1m", PeriodTime: period.Unix(),
		CommittedPositions: []*publicstoragepb.CommittedPosition{{NodeId: "n", StoreId: "s", Sequence: 1}},
		ReadyAt:            timestamppb.New(period),
	}
}

func factorModuleRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Join(filepath.Dir(thisFile), "..", "..")
}
