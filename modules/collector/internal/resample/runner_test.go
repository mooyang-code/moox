package resample

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	collectorschema "github.com/mooyang-code/moox/modules/collector/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

type runnerSource struct{ subjects []domain.DatasetSubject }

func (s runnerSource) GetDataset(context.Context, string, string) (storagesource.DatasetInfo, error) {
	return storagesource.DatasetInfo{DataSourceID: "crypto_market", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Freqs: []string{"1m"}, Attributes: map[string]string{"market_type": "spot"}}, nil
}
func (s runnerSource) ListSubjects(context.Context, string, string, string) ([]domain.DatasetSubject, error) {
	return s.subjects, nil
}

func TestRunnerTickPlansAndProcessesRealtimeBucket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "collector.db")
	db, err := store.Open(&store.Options{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(collectorschema.AllSQL()))
	params := `{"provider":"moox","market_type":"spot","source_dataset_id":"source_bars","source_frequency":"1m","source_series_tag":"venue:binance","target_dataset_id":"spot_kline_derived_5m","target_frequency":"5m","alignment":"epoch_utc","settle_delay_ms":0}`
	require.NoError(t, db.TaskRules().Create(context.Background(), domain.TaskRule{SpaceID: "crypto", RuleID: "rule-5m", DataType: "kline_resample", Provider: "moox", MarketType: "spot", CollectParams: params, PrepareState: domain.PrepareStateReady, Enabled: true}))
	start := time.Unix(300, 0).UTC()
	now := start.Add(5 * time.Minute)
	fake := &fakePrimary{}
	for i := 0; i < 5; i++ {
		at := start.Add(time.Duration(i) * time.Minute)
		fields := []*storagepb.FieldValue{}
		for _, item := range []struct {
			name  string
			value float64
		}{{"open", 1}, {"high", 2}, {"low", 0.5}, {"close", 1.5}, {"volume", 1}, {"quote_volume", 2}} {
			fields = append(fields, &storagepb.FieldValue{FieldId: item.name, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: item.value}}})
		}
		fields = append(fields, &storagepb.FieldValue{FieldId: "trade_num", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_IntValue{IntValue: 1}}})
		fake.rows = append(fake.rows, &storagepb.RowFieldValues{Key: rowKey("crypto", "source_bars", "BTC", "1m", at, "venue:binance"), Fields: fields})
	}
	runner := &Runner{Rules: db.TaskRules(), Instances: db.TaskInstances(), Source: runnerSource{subjects: []domain.DatasetSubject{{SubjectID: "BTC", Status: "active"}}}, Primary: fake, Config: RunnerConfig{SpaceID: "crypto", WorkerConcurrency: 1, WorkerJobTimeout: time.Second, RepairLookbackBuckets: 0}}
	require.NoError(t, runner.Tick(context.Background(), now))
	require.Len(t, fake.writes, 1)
	instances, _, err := db.TaskInstances().List(context.Background(), store.TaskInstanceFilter{SpaceID: "crypto", RuleID: "rule-5m", Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, instances, 1)
}
