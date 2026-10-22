package resample

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketstorage"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/barclock"
	"github.com/mooyang-code/moox/packages/seriesset"
	"github.com/stretchr/testify/require"
)

// fakeStorage 是内存里的 Primary 与周期提交：按行键保存行，记录每次写入。
type fakeStorage struct {
	mu        sync.Mutex
	rows      map[string]*storagepb.RowFieldUpsert
	upserts   [][]*storagepb.RowFieldUpsert
	commits   []*storagepb.DatasetPeriodExpectation
	commitErr error
	readErr   error
}

func newFakeStorage() *fakeStorage { return &fakeStorage{rows: map[string]*storagepb.RowFieldUpsert{}} }

func keyText(key *storagepb.RowKey) string {
	series := key.GetTimeSeries()
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s", key.GetSpaceId(), key.GetDatasetId(), series.GetSubjectId(), series.GetFreq(), series.GetDataTime(), series.GetSeriesTag())
}

func (f *fakeStorage) ReadRows(_ context.Context, keys []*storagepb.RowKey, _ []string, _ []string) ([]*storagepb.RowFieldValues, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readErr != nil {
		return nil, f.readErr
	}
	var out []*storagepb.RowFieldValues
	for _, key := range keys {
		if row, ok := f.rows[keyText(key)]; ok {
			out = append(out, &storagepb.RowFieldValues{Key: key, Fields: row.GetFields(), Attributes: row.GetAttributes()})
		}
	}
	return out, nil
}

func (f *fakeStorage) UpsertFieldsWithSource(_ context.Context, rows []*storagepb.RowFieldUpsert, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts = append(f.upserts, rows)
	for _, row := range rows {
		f.rows[keyText(row.GetKey())] = row
	}
	return nil
}

func (f *fakeStorage) CommitTimeSeriesBatch(_ context.Context, expectation *storagepb.DatasetPeriodExpectation, items []*storagepb.TimeSeriesBatchRow, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.commitErr != nil {
		return f.commitErr
	}
	f.commits = append(f.commits, expectation)
	for _, item := range items {
		f.rows[keyText(item.GetRow().GetKey())] = item.GetRow()
	}
	return nil
}

func (f *fakeStorage) putSource(space, dataset, subject, freq, tag string, at time.Time, open, high, low, closeValue, volume, quote float64, trades int64) {
	row := &storagepb.RowFieldUpsert{
		Key:    rowKey(space, dataset, subject, freq, at, tag),
		Fields: []*storagepb.FieldValue{doubleField("open", open), doubleField("high", high), doubleField("low", low), doubleField("close", closeValue), doubleField("volume", volume), doubleField("quote_volume", quote), intField("trade_num", trades)},
	}
	f.rows[keyText(row.GetKey())] = row
}

func (f *fakeStorage) target(space, dataset, subject, freq, tag string, at time.Time) *storagepb.RowFieldUpsert {
	return f.rows[keyText(rowKey(space, dataset, subject, freq, at, tag))]
}

func closeOf(row *storagepb.RowFieldUpsert) float64 {
	for _, field := range row.GetFields() {
		if field.GetFieldId() == "close" {
			return field.GetValue().GetDoubleValue()
		}
	}
	return -1
}

type fakeRegistrar struct {
	calls []int64
	err   error
}

func (r *fakeRegistrar) RegisterSeriesSnapshot(_ context.Context, _, _, _ string, series []seriesset.Series, effectiveFrom int64) (marketstorage.SnapshotRegistration, error) {
	if r.err != nil {
		return marketstorage.SnapshotRegistration{}, r.err
	}
	set, err := seriesset.Canonicalize(series)
	if err != nil {
		return marketstorage.SnapshotRegistration{}, err
	}
	r.calls = append(r.calls, effectiveFrom)
	return marketstorage.SnapshotRegistration{Hash: set.Hash, Count: uint32(len(set.Series))}, nil
}

func openState(t *testing.T) *store.Store {
	t.Helper()
	mgr, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.AllSQL()))
	return mgr
}

func makeTask(t *testing.T, space, source, target string) *Task {
	t.Helper()
	params := `{"provider":"binance","market_type":"spot","source_dataset_id":"dataset_src","source_frequency":"` + source + `","source_series_tag":"venue:binance","target_dataset_id":"dataset_task","target_frequency":"` + target + `"}`
	task, err := NewTask(domain.CollectionTask{SpaceID: space, TaskID: "task", DataType: "kline_resample", CollectParams: params, Enabled: true, ResultDatasetID: "dataset_task", ResultViewID: "view_task"})
	require.NoError(t, err)
	return task
}

func utc(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return parsed.UTC()
}

func TestRunBucketAggregatesFiveMinuteBarsIntoFortyFiveMinutes(t *testing.T) {
	storage := newFakeStorage()
	task := makeTask(t, "crypto", "5m", "45m")
	bucket, _, err := task.Clock.BucketAt(utc("2026-10-21T09:50:00Z"))
	require.NoError(t, err)
	require.Equal(t, utc("2026-10-21T09:45:00Z"), bucket.Start)
	for index := 0; index < 9; index++ {
		at := bucket.Start.Add(time.Duration(index) * 5 * time.Minute)
		for _, subject := range []string{"BTC", "ETH"} {
			price := float64(100 + index)
			storage.putSource("crypto", "dataset_src", subject, "5m", "venue:binance", at, price, price+5, price-5, price+1, 2, 20, 3)
		}
	}
	// ETH 缺一根：被跳过，不输出半根。
	delete(storage.rows, keyText(rowKey("crypto", "dataset_src", "ETH", "5m", bucket.Start.Add(20*time.Minute), "venue:binance")))

	engine := &Engine{Primary: storage}
	result, err := engine.RunBucket(context.Background(), task, bucket, []string{"BTC", "ETH"}, "")
	require.NoError(t, err)
	require.Equal(t, 1, result.Written)
	require.Len(t, result.Skipped, 1)
	require.Equal(t, "ETH", result.Skipped[0].SubjectID)

	row := storage.target("crypto", "dataset_task", "BTC", "45m", "venue:binance", bucket.Key)
	require.NotNil(t, row)
	var open, high, low, closeValue, volume, trades float64
	for _, field := range row.GetFields() {
		switch field.GetFieldId() {
		case "open":
			open = field.GetValue().GetDoubleValue()
		case "high":
			high = field.GetValue().GetDoubleValue()
		case "low":
			low = field.GetValue().GetDoubleValue()
		case "close":
			closeValue = field.GetValue().GetDoubleValue()
		case "volume":
			volume = field.GetValue().GetDoubleValue()
		case "trade_num":
			trades = float64(field.GetValue().GetIntValue())
		}
	}
	require.Equal(t, 100.0, open)
	require.Equal(t, 113.0, high, "最后一根 high = 108+5")
	require.Equal(t, 95.0, low)
	require.Equal(t, 109.0, closeValue)
	require.Equal(t, 18.0, volume)
	require.Equal(t, 27.0, trades)
	require.Nil(t, storage.target("crypto", "dataset_task", "ETH", "45m", "venue:binance", bucket.Key))
}

func TestRunBucketIsIdempotentForRepeatedExecution(t *testing.T) {
	storage := newFakeStorage()
	task := makeTask(t, "crypto", "5m", "15m")
	bucket, _, _ := task.Clock.BucketAt(utc("2026-10-21T10:01:00Z"))
	for index := 0; index < 3; index++ {
		storage.putSource("crypto", "dataset_src", "BTC", "5m", "venue:binance", bucket.Start.Add(time.Duration(index)*5*time.Minute), 1, 2, 0.5, float64(index), 1, 1, 1)
	}
	engine := &Engine{Primary: storage}
	for i := 0; i < 3; i++ {
		result, err := engine.RunBucket(context.Background(), task, bucket, []string{"BTC"}, "")
		require.NoError(t, err)
		require.Equal(t, 1, result.Written)
	}
	require.Len(t, storage.rows, 4, "3 根源 + 1 根目标，重复执行写的是同一行")
	require.Equal(t, 2.0, closeOf(storage.target("crypto", "dataset_task", "BTC", "15m", "venue:binance", bucket.Key)))
}

func TestRunBucketAStockHourlyUsesTradingSessionBars(t *testing.T) {
	storage := newFakeStorage()
	task := makeTask(t, "stockcn", "1m", "1h")
	// 北京时间 10:30–11:30 的桶（02:30Z 起）正好 60 根 1m；11:30 之后是午休。
	bucket, ok, err := task.Clock.BucketAt(utc("2026-10-21T02:45:00Z"))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, utc("2026-10-21T02:30:00Z"), bucket.Start)
	for index := 0; index < 60; index++ {
		storage.putSource("stockcn", "dataset_src", "600000", "1m", "venue:binance", bucket.Start.Add(time.Duration(index)*time.Minute), 10, 11, 9, 10+float64(index)/100, 100, 1000, 5)
	}
	engine := &Engine{Primary: storage}
	result, err := engine.RunBucket(context.Background(), task, bucket, []string{"600000"}, "")
	require.NoError(t, err)
	require.Equal(t, 1, result.Written)
	row := storage.target("stockcn", "dataset_task", "600000", "1h", "venue:binance", bucket.Key)
	require.NotNil(t, row)
	require.InDelta(t, 10.59, closeOf(row), 1e-9)

	// 缺一根就不输出。
	delete(storage.rows, keyText(rowKey("stockcn", "dataset_src", "600000", "1m", bucket.Start.Add(7*time.Minute), "venue:binance")))
	delete(storage.rows, keyText(rowKey("stockcn", "dataset_task", "600000", "1h", bucket.Key, "venue:binance")))
	result, err = engine.RunBucket(context.Background(), task, bucket, []string{"600000"}, "")
	require.NoError(t, err)
	require.Equal(t, 0, result.Written)
	require.Len(t, result.Skipped, 1)
}

type fakeAbsences struct{ absences []store.SourceAbsence }

func (f fakeAbsences) AbsencesIn(_ context.Context, _, _, _ string, subjects []string, from, to time.Time) ([]store.SourceAbsence, error) {
	var out []store.SourceAbsence
	for _, absence := range f.absences {
		if absence.From.Before(to) && absence.To.After(from) {
			out = append(out, absence)
		}
	}
	return out, nil
}

func TestRunBucketTreatsConfirmedSourceAbsencesAsNotExpected(t *testing.T) {
	storage := newFakeStorage()
	task := makeTask(t, "crypto", "5m", "15m")
	bucket, _, _ := task.Clock.BucketAt(utc("2026-10-21T10:01:00Z"))
	// 中间一根行情源确认没有。
	for _, index := range []int{0, 2} {
		storage.putSource("crypto", "dataset_src", "BTC", "5m", "venue:binance", bucket.Start.Add(time.Duration(index)*5*time.Minute), 1, 2, 0.5, float64(index), 1, 1, 1)
	}
	absence := store.SourceAbsence{SubjectID: "BTC", From: bucket.Start.Add(5 * time.Minute), To: bucket.Start.Add(10 * time.Minute)}
	engine := &Engine{Primary: storage, Absences: fakeAbsences{absences: []store.SourceAbsence{absence}}}
	result, err := engine.RunBucket(context.Background(), task, bucket, []string{"BTC"}, "")
	require.NoError(t, err)
	require.Equal(t, 1, result.Written, "行情源确认没有的 K 线不计入应有数量")

	// 整个桶都确认没有（例如停牌）：不产出，也不算失败。
	other, _, _ := task.Clock.BucketAt(utc("2026-10-21T11:01:00Z"))
	engine.Absences = fakeAbsences{absences: []store.SourceAbsence{{SubjectID: "BTC", From: other.Start, To: other.End}}}
	result, err = engine.RunBucket(context.Background(), task, other, []string{"BTC"}, "")
	require.NoError(t, err)
	require.Zero(t, result.Written)
	require.Len(t, result.Skipped, 1)
	require.Contains(t, result.Skipped[0].Reason, "确认行情源没有")
}

func TestRunBucketPropagatesSourceReadFailure(t *testing.T) {
	storage := newFakeStorage()
	storage.readErr = errors.New("storage down")
	task := makeTask(t, "crypto", "5m", "15m")
	bucket, _, _ := task.Clock.BucketAt(utc("2026-10-21T10:01:00Z"))
	_, err := (&Engine{Primary: storage}).RunBucket(context.Background(), task, bucket, []string{"BTC"}, "")
	require.ErrorContains(t, err, "storage down")
}

func commitFixture(t *testing.T, now time.Time) (*Engine, *fakeStorage, *Task, barclock.Bucket, *store.Store) {
	t.Helper()
	mgr := openState(t)
	storage := newFakeStorage()
	task := makeTask(t, "crypto", "5m", "15m")
	bucket, _, _ := task.Clock.BucketAt(now.Add(-10 * time.Minute))
	for index := 0; index < 3; index++ {
		for _, subject := range []string{"BTC", "ETH", "SOL"} {
			storage.putSource("crypto", "dataset_src", subject, "5m", "venue:binance", bucket.Start.Add(time.Duration(index)*5*time.Minute), 1, 2, 0.5, 1, 1, 1, 1)
		}
	}
	engine := &Engine{Primary: storage, Period: storage, State: mgr.Resample(), Now: func() time.Time { return now }}
	return engine, storage, task, bucket, mgr
}

func TestWriteUsesPeriodCommitInsideSnapshotAndPlainWriteOutside(t *testing.T) {
	now := utc("2026-10-21T10:16:00Z")
	engine, storage, task, bucket, mgr := commitFixture(t, now)
	ctx := context.Background()
	require.NoError(t, mgr.Resample().AddSnapshot(ctx, store.ResampleSnapshot{SpaceID: "crypto", TaskID: "task", EffectiveFrom: bucket.Key.Add(-time.Hour).Unix(), SeriesHash: "unused", Subjects: []string{"BTC", "ETH"}}))
	set, err := seriesset.Canonicalize(task.series([]string{"BTC", "ETH"}))
	require.NoError(t, err)
	require.NoError(t, mgr.Resample().AddSnapshot(ctx, store.ResampleSnapshot{SpaceID: "crypto", TaskID: "task", EffectiveFrom: bucket.Key.Add(-30 * time.Minute).Unix(), SeriesHash: set.Hash, Subjects: []string{"BTC", "ETH"}}))

	// 桶 10:00–10:15，收盘后 1 分钟、迟到容忍 2 分钟内：BTC/ETH 周期提交，SOL 在快照之外普通写入。
	result, err := engine.RunBucket(ctx, task, bucket, []string{"BTC", "ETH", "SOL"}, "")
	require.NoError(t, err)
	require.Equal(t, 3, result.Written)
	require.Equal(t, 2, result.Committed)
	require.Len(t, storage.commits, 1)
	require.Equal(t, set.Hash, storage.commits[0].GetSeriesHash())
	require.EqualValues(t, 2, storage.commits[0].GetExpectedCount())
	require.Equal(t, bucket.Key.Unix(), storage.commits[0].GetPeriodTime())
	require.Len(t, storage.upserts, 1)
	require.Equal(t, "SOL", storage.upserts[0][0].GetKey().GetTimeSeries().GetSubjectId())

	notices, err := mgr.Resample().UnsealedNotices(ctx, 10)
	require.NoError(t, err)
	require.Len(t, notices, 1, "只有普通写入的标的记为补写通知")
	require.Equal(t, "SOL", notices[0].SubjectID)
	require.Equal(t, store.NoticeOriginResample, notices[0].Origin)
}

func TestWriteFallsBackToPlainWriteWhenPeriodCommitFailsOrDeadlinePassed(t *testing.T) {
	now := utc("2026-10-21T10:16:00Z")
	engine, storage, task, bucket, mgr := commitFixture(t, now)
	ctx := context.Background()
	set, _ := seriesset.Canonicalize(task.series([]string{"BTC", "ETH", "SOL"}))
	require.NoError(t, mgr.Resample().AddSnapshot(ctx, store.ResampleSnapshot{SpaceID: "crypto", TaskID: "task", EffectiveFrom: bucket.Key.Add(-time.Hour).Unix(), SeriesHash: set.Hash, Subjects: []string{"BTC", "ETH", "SOL"}}))

	storage.commitErr = errors.New("period already completed")
	result, err := engine.RunBucket(ctx, task, bucket, []string{"BTC", "ETH", "SOL"}, "job-1")
	require.NoError(t, err)
	require.Equal(t, 3, result.Written)
	require.Zero(t, result.Committed, "周期提交失败改用普通写入")
	require.Empty(t, storage.commits)

	// 截止之后（收盘 + 2 分钟）直接普通写入，不尝试周期提交。
	storage.commitErr = nil
	late := *engine
	late.Now = func() time.Time { return bucket.End.Add(5 * time.Minute) }
	result, err = late.RunBucket(ctx, task, bucket, []string{"BTC"}, "")
	require.NoError(t, err)
	require.Zero(t, result.Committed)
	require.Empty(t, storage.commits)

	// 首次生效点之前的桶也是普通写入。
	early, _, _ := task.Clock.BucketAt(bucket.Start.Add(-2 * time.Hour))
	result, err = engine.RunBucket(ctx, task, early, []string{"BTC"}, "")
	require.NoError(t, err)
	require.Zero(t, result.Committed)
}

func TestEnsureSnapshotRegistersFromNextBucketAndFollowsSubjectChanges(t *testing.T) {
	mgr := openState(t)
	registrar := &fakeRegistrar{}
	now := utc("2026-10-21T10:07:00Z")
	engine := &Engine{Snapshots: registrar, State: mgr.Resample(), Now: func() time.Time { return now }}
	task := makeTask(t, "crypto", "5m", "15m")
	ctx := context.Background()

	require.NoError(t, engine.EnsureSnapshot(ctx, task, []string{"ETH", "BTC"}))
	require.Equal(t, []int64{utc("2026-10-21T10:15:00Z").Unix()}, registrar.calls, "生效点是当前时间之后的第一个目标桶边界")
	require.NoError(t, engine.EnsureSnapshot(ctx, task, []string{"BTC", "ETH"}))
	require.Len(t, registrar.calls, 1, "标的集合没变不重复登记")

	// 上一份快照还没生效时不登记新的。
	require.NoError(t, engine.EnsureSnapshot(ctx, task, []string{"BTC", "ETH", "SOL"}))
	require.Len(t, registrar.calls, 1)

	now = utc("2026-10-21T10:20:00Z")
	require.NoError(t, engine.EnsureSnapshot(ctx, task, []string{"BTC", "ETH", "SOL"}))
	require.Equal(t, []int64{utc("2026-10-21T10:15:00Z").Unix(), utc("2026-10-21T10:30:00Z").Unix()}, registrar.calls)
	latest, ok, err := mgr.Resample().LatestSnapshot(ctx, "crypto", "task")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{"BTC", "ETH", "SOL"}, latest.Subjects)

	registrar.err = marketstorage.ErrStaleEffectiveFrom
	now = utc("2026-10-21T10:40:00Z")
	require.NoError(t, engine.EnsureSnapshot(ctx, task, []string{"BTC"}), "生效点过期不报错，下一轮重选")
}

func TestEnsureSnapshotSkipsAStockHolidayBeyondStorageLimit(t *testing.T) {
	mgr := openState(t)
	registrar := &fakeRegistrar{}
	// 2026-09-30 晚间，国庆长假（10-01 起）之后第一个交易日离当前超过 Storage 允许的 7 天就不登记。
	engine := &Engine{Snapshots: registrar, State: mgr.Resample(), Now: func() time.Time { return utc("2026-09-30T12:00:00Z") }}
	task := makeTask(t, "stockcn", "1m", "1h")
	require.NoError(t, engine.EnsureSnapshot(context.Background(), task, []string{"600000"}))
	for _, at := range registrar.calls {
		require.LessOrEqual(t, time.Unix(at, 0).UTC().Sub(utc("2026-09-30T12:00:00Z")), 7*24*time.Hour)
	}
}

func TestNewTaskRejectsPeriodsThatDoNotDivideTheSession(t *testing.T) {
	params := `{"provider":"eastmoney","market_type":"equity","source_dataset_id":"dataset_src","source_frequency":"1m","source_series_tag":"venue:x","target_dataset_id":"dataset_task","target_frequency":"45m"}`
	_, err := NewTask(domain.CollectionTask{SpaceID: "stockcn", TaskID: "task", DataType: "kline_resample", CollectParams: params, ResultDatasetID: "dataset_task", ResultViewID: "view_task"})
	require.ErrorContains(t, err, "不能整除")
	_, err = NewTask(domain.CollectionTask{SpaceID: "stockhk", TaskID: "task", DataType: "kline_resample", CollectParams: params, ResultDatasetID: "dataset_task", ResultViewID: "view_task"})
	require.ErrorContains(t, err, "没有登记交易日历")
}

func TestWithoutTimes(t *testing.T) {
	base := utc("2026-10-21T10:00:00Z")
	times := []time.Time{base, base.Add(time.Minute), base.Add(2 * time.Minute)}
	got := withoutTimes(times, []time.Time{base.Add(time.Minute)})
	sorted := append([]time.Time(nil), got...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Before(sorted[j]) })
	require.Equal(t, []time.Time{base, base.Add(2 * time.Minute)}, sorted)
}
