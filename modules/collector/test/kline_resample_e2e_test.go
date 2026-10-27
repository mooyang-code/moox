package test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/resample"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	collectorschema "github.com/mooyang-code/moox/modules/collector/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

type e2eSource struct{ subjects []string }

func (s e2eSource) GetDataset(context.Context, string, string) (storagesource.DatasetInfo, error) {
	return storagesource.DatasetInfo{DataSourceID: "binance", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Freq: "5m", SubjectTags: []string{"tag"}}, nil
}

func (s e2eSource) ResolveSubjects(context.Context, string, []string) ([]domain.Subject, error) {
	out := make([]domain.Subject, len(s.subjects))
	for i, id := range s.subjects {
		out[i] = domain.Subject{SubjectID: id, Status: "active"}
	}
	return out, nil
}

// e2ePrimary 是内存里的 Primary：源行和目标行放在一起，写入覆盖同一行键。
type e2ePrimary struct {
	mu     sync.Mutex
	rows   map[string]*storagepb.RowFieldUpsert
	writes int
}

func newE2EPrimary() *e2ePrimary { return &e2ePrimary{rows: map[string]*storagepb.RowFieldUpsert{}} }

func e2eKeyText(key *storagepb.RowKey) string {
	ts := key.GetTimeSeries()
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s", key.GetSpaceId(), key.GetDatasetId(), ts.GetSubjectId(), ts.GetFreq(), ts.GetDataTime(), ts.GetSeriesTag())
}

func (p *e2ePrimary) ReadRows(_ context.Context, keys []*storagepb.RowKey, _ []string, _ []string) ([]*storagepb.RowFieldValues, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []*storagepb.RowFieldValues
	for _, key := range keys {
		if row := p.rows[e2eKeyText(key)]; row != nil {
			out = append(out, &storagepb.RowFieldValues{Key: row.Key, Fields: row.Fields, Attributes: row.Attributes})
		}
	}
	return out, nil
}

func (p *e2ePrimary) UpsertFields(_ context.Context, rows []*storagepb.RowFieldUpsert) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, row := range rows {
		p.rows[e2eKeyText(row.GetKey())] = row
		p.writes++
	}
	return nil
}

func e2eRow(space, dataset, subject, freq string, at time.Time, closeValue float64) *storagepb.RowFieldUpsert {
	double := func(name string, value float64) *storagepb.FieldValue {
		return &storagepb.FieldValue{FieldId: name, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: value}}}
	}
	return &storagepb.RowFieldUpsert{
		Key:    &storagepb.RowKey{SpaceId: space, DatasetId: dataset, Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: subject, Freq: freq, DataTime: at.UTC().Format(time.RFC3339Nano), SeriesTag: "venue:binance"}}},
		Fields: []*storagepb.FieldValue{double("open", closeValue-1), double("high", closeValue+1), double("low", closeValue-2), double("close", closeValue), double("volume", 2), double("quote_volume", 20), {FieldId: "trade_num", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_IntValue{IntValue: 3}}}},
	}
}

func e2eField(row *storagepb.RowFieldUpsert, name string) float64 {
	for _, field := range row.GetFields() {
		if field.GetFieldId() == name {
			return field.GetValue().GetDoubleValue()
		}
	}
	return -1
}

func e2eHarness(t *testing.T, space, dataset, sourceFreq, targetFreq string) (*store.Store, *e2ePrimary, *resample.Engine, domain.CollectionTask) {
	t.Helper()
	db, err := store.Open(&store.Options{Path: t.TempDir() + "/collector.db"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(collectorschema.AllSQL()))
	params := fmt.Sprintf(`{"provider":"binance","market_type":"spot","source_dataset_id":%q,"source_frequency":%q,"source_series_tag":"venue:binance","target_dataset_id":"dataset_e2e","target_frequency":%q}`, dataset, sourceFreq, targetFreq)
	task := domain.CollectionTask{SpaceID: space, TaskID: "e2e-resample", DataType: "kline_resample", PrepareState: domain.PrepareStateReady, Enabled: true, CollectParams: params, ResultDatasetID: "dataset_e2e", ResultViewID: "view_e2e"}
	require.NoError(t, db.Tasks().Create(context.Background(), task))
	primary := newE2EPrimary()
	return db, primary, &resample.Engine{Primary: primary, State: db.Resample(), Absences: db.Backfill(), Now: func() time.Time { return time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC) }}, task
}

// TestKlineResample45mHistoryJobE2E 覆盖 crypto 5m → 45m：历史作业回填 3 天，每个标的每天正好 32 根，
// 键都是 45 分钟的整数倍，OHLCV 与手算一致；源数据被改写后重跑作业会覆盖旧结果；缺一根源 K 线的桶不输出半根。
func TestKlineResample45mHistoryJobE2E(t *testing.T) {
	ctx := context.Background()
	db, primary, engine, task := e2eHarness(t, "crypto", "dataset_src", "5m", "45m")
	subjects := []string{"BTC", "ETH"}
	from := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	for at := from; at.Before(to); at = at.Add(5 * time.Minute) {
		for _, subject := range subjects {
			primary.rows[e2eKeyText(e2eRow("crypto", "dataset_src", subject, "5m", at, float64(at.Unix()%1000)).Key)] = e2eRow("crypto", "dataset_src", subject, "5m", at, float64(at.Unix()%1000))
		}
	}
	// ETH 在 10-20 的一个桶里缺一根源 K 线：该桶不输出。
	missing := time.Date(2026, 10, 20, 10, 20, 0, 0, time.UTC)
	delete(primary.rows, e2eKeyText(e2eRow("crypto", "dataset_src", "ETH", "5m", missing, 0).Key))

	runner := &resample.JobRunner{State: db.Resample(), Tasks: db.Tasks(), Source: e2eSource{subjects: subjects}, Engine: engine, Config: resample.JobRunnerConfig{BucketsPerRound: 40}}
	rt, err := resample.NewTask(task)
	require.NoError(t, err)
	n := 0
	job, _, err := resample.SubmitJob(ctx, db.Resample(), rt, resample.SubmitJobRequest{SpaceID: "crypto", TaskID: task.TaskID, RequestID: "r1", Start: from, End: to},
		func() string { n++; return fmt.Sprintf("job-%d", n) }, time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, 96, job.TotalBuckets, "3 天 × 每天 32 个 45m 桶")
	require.NoError(t, runner.Drain(ctx, 10*time.Second))
	done, err := db.Resample().GetJob(ctx, "crypto", job.JobID)
	require.NoError(t, err)
	require.Equal(t, store.ResampleJobSucceeded, done.Status)
	require.EqualValues(t, 2*96-1, done.Written)
	require.EqualValues(t, 1, done.Skipped)

	for day := 0; day < 3; day++ {
		count := 0
		for bucket := 0; bucket < 32; bucket++ {
			at := from.Add(time.Duration(day)*24*time.Hour + time.Duration(bucket)*45*time.Minute)
			if row := primary.rows[e2eKeyText(e2eRow("crypto", "dataset_e2e", "BTC", "45m", at, 0).Key)]; row != nil {
				count++
				require.Zero(t, at.Unix()%(45*60), "键是 45 分钟的整数倍")
			}
		}
		require.Equal(t, 32, count, "BTC 第 %d 天正好 32 根", day)
	}
	// 手算一根：2026-10-19 00:00 的桶由 9 根 5m 组成，收盘价取最后一根。
	row := primary.rows[e2eKeyText(e2eRow("crypto", "dataset_e2e", "BTC", "45m", from, 0).Key)]
	require.NotNil(t, row)
	last := from.Add(40 * time.Minute)
	require.Equal(t, float64(last.Unix()%1000), e2eField(row, "close"))
	require.Equal(t, float64(from.Unix()%1000)-1, e2eField(row, "open"))
	require.Equal(t, 18.0, e2eField(row, "volume"))
	skipped := primary.rows[e2eKeyText(e2eRow("crypto", "dataset_e2e", "ETH", "45m", time.Date(2026, 10, 20, 9, 45, 0, 0, time.UTC), 0).Key)]
	require.Nil(t, skipped, "缺一根源 K 线的桶不输出半根")

	// 源数据被改写：补回缺的那根并改一根的收盘价，再提交作业重算，旧结果被覆盖。
	primary.rows[e2eKeyText(e2eRow("crypto", "dataset_src", "ETH", "5m", missing, 0).Key)] = e2eRow("crypto", "dataset_src", "ETH", "5m", missing, 500)
	primary.rows[e2eKeyText(e2eRow("crypto", "dataset_src", "BTC", "5m", last, 0).Key)] = e2eRow("crypto", "dataset_src", "BTC", "5m", last, 777)
	_, _, err = resample.SubmitJob(ctx, db.Resample(), rt, resample.SubmitJobRequest{SpaceID: "crypto", TaskID: task.TaskID, RequestID: "r2", Start: from, End: from.Add(24 * time.Hour), Subjects: []string{"BTC", "ETH"}},
		func() string { n++; return fmt.Sprintf("job-%d", n) }, time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.NoError(t, runner.Drain(ctx, 10*time.Second))
	row = primary.rows[e2eKeyText(e2eRow("crypto", "dataset_e2e", "BTC", "45m", from, 0).Key)]
	require.Equal(t, 777.0, e2eField(row, "close"), "重算覆盖旧结果")

	// 这些历史作业的写入都是普通写入，记成了补写通知。
	notices, err := db.Resample().UnsealedNotices(ctx, 1000)
	require.NoError(t, err)
	require.NotEmpty(t, notices)
	require.Equal(t, "dataset_e2e", notices[0].DatasetID)
	require.Equal(t, store.NoticeOriginResample, notices[0].Origin)
}

// TestKlineResampleAStockHourlyE2E 覆盖 A 股 1m → 1h：桶从各交易时段开盘起切分（09:30、10:30、13:00、14:00），午休和非交易日没有桶；
// 1m → 45m 在提交任务时就被拒绝。
func TestKlineResampleAStockHourlyE2E(t *testing.T) {
	ctx := context.Background()
	db, primary, engine, task := e2eHarness(t, "stockcn", "dataset_src", "1m", "1h")
	subjects := []string{"600000"}
	day := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC) // 周三
	for _, session := range [][2]time.Duration{{90 * time.Minute, 210 * time.Minute}, {300 * time.Minute, 420 * time.Minute}} {
		for at := day.Add(session[0]); at.Before(day.Add(session[1])); at = at.Add(time.Minute) {
			primary.rows[e2eKeyText(e2eRow("stockcn", "dataset_src", "600000", "1m", at, 10).Key)] = e2eRow("stockcn", "dataset_src", "600000", "1m", at, 10)
		}
	}
	runner := &resample.JobRunner{State: db.Resample(), Tasks: db.Tasks(), Source: e2eSource{subjects: subjects}, Engine: engine}
	rt, err := resample.NewTask(task)
	require.NoError(t, err)
	_, _, err = resample.SubmitJob(ctx, db.Resample(), rt, resample.SubmitJobRequest{SpaceID: "stockcn", TaskID: task.TaskID, RequestID: "r1", Start: day, End: day.Add(24 * time.Hour)},
		func() string { return "job-a" }, time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.NoError(t, runner.Drain(ctx, 10*time.Second))
	for _, open := range []time.Duration{90 * time.Minute, 150 * time.Minute, 300 * time.Minute, 360 * time.Minute} {
		require.NotNil(t, primary.rows[e2eKeyText(e2eRow("stockcn", "dataset_e2e", "600000", "1h", day.Add(open), 0).Key)], "开盘 %s 的桶", open)
	}
	require.Len(t, primary.rows, 240+4, "240 根 1m 源 K 线 + 每个交易日 4 根 1h，午休与盘后没有桶")

	params := `{"provider":"binance","market_type":"spot","source_dataset_id":"dataset_src","source_frequency":"1m","source_series_tag":"venue:binance","target_dataset_id":"dataset_e2e","target_frequency":"45m"}`
	bad := task
	bad.CollectParams = params
	_, err = resample.NewTask(bad)
	require.ErrorContains(t, err, "不能整除")
}
