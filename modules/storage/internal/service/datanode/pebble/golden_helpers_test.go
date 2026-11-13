package pebble

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/seriesset"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// 本文件是账本与完成标记金样（period_golden_test.go）的辅助函数。金样只经由改键前后都保留的入口读写，
// 改动内部实现时只允许改这里读取内部状态的函数，金样本身不动。

// goldenClock 是可调的 Storage 时钟，后台截止判定也按它判断。
type goldenClock struct{ nanos atomic.Int64 }

func newGoldenClock(now time.Time) *goldenClock {
	clock := &goldenClock{}
	clock.set(now)
	return clock
}

func (c *goldenClock) set(now time.Time) { c.nanos.Store(now.UnixNano()) }
func (c *goldenClock) now() time.Time    { return time.Unix(0, c.nanos.Load()).UTC() }

// openGoldenStore 在 path 打开库；maxEventBytes 为 0 表示不限事件大小。
func openGoldenStore(t *testing.T, path string, clock *goldenClock, maxEventBytes int) *Store {
	t.Helper()
	store, err := Open(Options{Path: path, PeriodNow: clock.now, MaxEventBytes: maxEventBytes})
	require.NoError(t, err)
	return store
}

func newGoldenStore(t *testing.T, clock *goldenClock) *Store {
	t.Helper()
	store := openGoldenStore(t, filepath.Join(t.TempDir(), "pebble"), clock, 0)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// goldenFinalize 跑一次截止判定（与后台每秒一次的判定相同），返回错误。
func goldenFinalize(t *testing.T, store *Store, now time.Time) error {
	t.Helper()
	_, err := store.FinalizeWaitingDatasetPeriods(context.Background(), now)
	return err
}

// goldenPeriodStatus 返回账本里一期的状态；周期不存在时返回空串。
func goldenPeriodStatus(t *testing.T, store *Store, spaceID, datasetID, frequency string, periodTime int64) string {
	t.Helper()
	record, found, err := store.readPeriodRecord(periodKey(spaceID, datasetID, frequency, periodTime))
	require.NoError(t, err)
	if !found {
		return ""
	}
	return record.Status
}

// goldenSeries 规整序列名单，返回规范顺序（序号即规范顺序下的位置）。
func goldenSeries(t *testing.T, series []seriesset.Series) []seriesset.Series {
	t.Helper()
	set, err := seriesset.Canonicalize(series)
	require.NoError(t, err)
	return set.Series
}

// goldenRegister 在 period 之前一分钟登记“从 period 起生效”的快照，登记后恢复时钟，返回周期提交用的期望。
func goldenRegister(t *testing.T, store *Store, clock *goldenClock, spaceID, datasetID, frequency string, series []seriesset.Series, period time.Time, latenessSeconds int64) DatasetPeriodExpectation {
	t.Helper()
	restore := clock.now()
	clock.set(period.Add(-time.Minute))
	result, err := store.RegisterSeriesSnapshot(context.Background(), spaceID, datasetID, frequency, series, period.Unix())
	clock.set(restore)
	require.NoError(t, err)
	return DatasetPeriodExpectation{
		SpaceID: spaceID, DatasetID: datasetID, Frequency: frequency, PeriodTime: period.Unix(),
		SeriesHash: result.SeriesHash, ExpectedCount: result.ExpectedCount, AllowedLatenessSeconds: latenessSeconds,
	}
}

func goldenRow(spaceID, datasetID, frequency string, series seriesset.Series, at time.Time, value float64) *pb.RowFieldUpsert {
	return &pb.RowFieldUpsert{
		Key: &pb.RowKey{SpaceId: spaceID, DatasetId: datasetID, Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
			SubjectId: series.SubjectID, SeriesTag: series.SeriesTag, Freq: frequency, DataTime: at.UTC().Format(time.RFC3339Nano),
		}}},
		Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}}}},
	}
}

// goldenStoredClose 读一行的 close 值；行不存在时 found=false。
func goldenStoredClose(t *testing.T, store *Store, key *pb.RowKey) (float64, bool) {
	t.Helper()
	rows, err := store.ReadRows(context.Background(), []*pb.RowKey{key}, []string{"close"}, nil)
	require.NoError(t, err)
	for _, row := range rows {
		for _, field := range row.GetFields() {
			if field.GetFieldId() == "close" {
				return field.GetValue().GetDoubleValue(), true
			}
		}
	}
	return 0, false
}

type goldenMarker struct {
	message *eventpb.EventMessage
	payload *storageeventpb.DatasetPeriodCompleted
}

// goldenMarkers 列出 Outbox 里某数据集的周期完成事件，按 Outbox 顺序。
func goldenMarkers(t *testing.T, store *Store, spaceID, datasetID string) []goldenMarker {
	t.Helper()
	entries, err := store.ListOutbox(context.Background(), 0, 10000)
	require.NoError(t, err)
	var out []goldenMarker
	for _, entry := range entries {
		message := &eventpb.EventMessage{}
		require.NoError(t, proto.Unmarshal(entry.Data, message))
		if message.GetEventName() != events.DatasetPeriodCompleted.Name() || message.GetSpaceId() != spaceID || message.GetSubjectId() != datasetID {
			continue
		}
		payload := &storageeventpb.DatasetPeriodCompleted{}
		require.NoError(t, proto.Unmarshal(message.GetPayload(), payload))
		out = append(out, goldenMarker{message: message, payload: payload})
	}
	return out
}

// goldenOutboxCount 返回 Outbox 里的条目总数。
func goldenOutboxCount(t *testing.T, store *Store) int {
	t.Helper()
	entries, err := store.ListOutbox(context.Background(), 0, 10000)
	require.NoError(t, err)
	return len(entries)
}
