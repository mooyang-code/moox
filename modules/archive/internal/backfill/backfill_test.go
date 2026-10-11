package backfill

import (
	"context"
	"errors"
	"fmt"
	"google.golang.org/protobuf/proto"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	"github.com/mooyang-code/moox/modules/archive/internal/journal"
	"github.com/mooyang-code/moox/modules/archive/internal/writer"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlanRequiresExplicitConfirmation(t *testing.T) {
	plan := Plan{SpaceID: "crypto", DatasetID: "spot_kline_1m", SubjectID: "BTC-USDT", Freq: "1m", Start: "2026-06-01T00:00:00Z", End: "2026-07-01T00:00:00Z"}
	if plan.Confirm {
		t.Fatal("new plan unexpectedly confirmed")
	}
	if len(plan.Partitions()) != 2 {
		t.Fatalf("partitions = %v", plan.Partitions())
	}
}

func TestRowsToPatches(t *testing.T) {
	writtenAt := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	rows := []*storagepb.TimeSeriesRow{{
		Key: &storagepb.TimeSeriesKey{
			SpaceId: "crypto", DatasetId: "kline", SubjectId: "BTC", Freq: "1m",
			DataTime: "2026-01-02T03:04:05Z", SeriesTag: "venue:okx",
		},
		Attributes: map[string]string{"source": "live"},
		Fields: []*storagepb.FieldValue{{
			FieldId: "close",
			Value:   &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: 1.25}},
		}},
	}}
	patches, err := rowsToPatches(rows, writtenAt)
	require.NoError(t, err)
	require.Len(t, patches, 1)
	assert.Equal(t, "crypto", patches[0].Partition.SpaceID)
	assert.Equal(t, "venue:okx", patches[0].Partition.SeriesTag)
	assert.InDelta(t, 1.25, *patches[0].Columns["close"].Double, 0.001)
	_, err = rowsToPatches([]*storagepb.TimeSeriesRow{nil}, writtenAt)
	require.Error(t, err)
}

func TestPlanPartitions(t *testing.T) {
	plan := Plan{Start: "2026-01-15T00:00:00Z", End: "2026-02-10T00:00:00Z"}
	assert.Equal(t, []string{"202601", "202602"}, plan.Partitions())
	assert.Nil(t, Plan{Start: "bad", End: "2026-02-10T00:00:00Z"}.Partitions())
}

func TestBackfillMessageIDIncludesSelectorPresenceAndValue(t *testing.T) {
	base := Plan{SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1h"}
	empty := ""
	binance := "venue:binance"
	base.Start, base.End = "2026-01-01T00:00:00Z", "2026-02-01T00:00:00Z"
	wildcard := backfillMessageID(base, "run-1", 1)
	base.SeriesTag = &empty
	defaultTag := backfillMessageID(base, "run-1", 1)
	base.SeriesTag = &binance
	exact := backfillMessageID(base, "run-1", 1)
	require.NotEqual(t, wildcard, defaultTag)
	require.NotEqual(t, defaultTag, exact)
	require.Contains(t, exact, "series_tag=venue%3Abinance")
	require.Contains(t, exact, "start=2026-01-01T00%3A00%3A00Z")
	require.Contains(t, exact, "end=2026-02-01T00%3A00%3A00Z")
	require.NotEqual(t, exact, backfillMessageID(base, "run-2", 1))
	otherRange := base
	otherRange.End = "2026-03-01T00:00:00Z"
	require.NotEqual(t, exact, backfillMessageID(otherRange, "run-1", 1))
}

func TestNewRunIDDoesNotCollide(t *testing.T) {
	first, err := newRunID()
	require.NoError(t, err)
	second, err := newRunID()
	require.NoError(t, err)
	require.Len(t, first, 32)
	require.NotEqual(t, first, second)
}

func TestBackfillerRunRequiresConfirm(t *testing.T) {
	b := New(nil, nil, nil, nil)
	_, err := b.Run(nil, Plan{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "confirm")
}

type captureAccess struct {
	request *storagepb.ReadTimeSeriesRowsReq
}

func (c *captureAccess) Invoke(_ context.Context, service, method string, request, _ any) error {
	if service != "trpc.moox.storage.PrimaryStore" || method != "ReadTimeSeriesRows" {
		return fmt.Errorf("unexpected RPC %s/%s", service, method)
	}
	c.request = request.(*storagepb.ReadTimeSeriesRowsReq)
	return errors.New("stop after request capture")
}

func TestBackfillerCarriesPrimaryAuth(t *testing.T) {
	access := &captureAccess{}
	auth := &commonpb.AuthInfo{AppId: "archive-backfill", AppKey: "derived-key"}
	b := New(access, auth, nil, nil)
	tag := ""
	_, err := b.Run(t.Context(), Plan{
		SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC-USDT", Freq: "1m",
		SeriesTag: &tag, Start: "2026-07-29T00:00:00Z", End: "2026-07-29T01:00:00Z", Confirm: true,
	})
	require.ErrorContains(t, err, "request capture")
	require.Equal(t, auth, access.request.GetAuthInfo())
	require.Equal(t, "crypto", access.request.GetSpaceId())
	require.Equal(t, "kline", access.request.GetDatasetId())
	require.Len(t, access.request.GetSelectors(), 1)
	require.NotNil(t, access.request.GetSelectors()[0].SeriesTag)
	require.Empty(t, access.request.GetSelectors()[0].GetSeriesTag())
}

func TestBackfillerAbsentSeriesTagUsesWildcardSelector(t *testing.T) {
	access := &captureAccess{}
	b := New(access, nil, nil, nil)
	_, err := b.Run(t.Context(), Plan{SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1m", Start: "2026-01-01T00:00:00Z", End: "2026-01-02T00:00:00Z", Confirm: true})
	require.ErrorContains(t, err, "request capture")
	require.Nil(t, access.request.GetSelectors()[0].SeriesTag)
}

func TestBackfillerRejectsIncompleteViewBeforeJournalAppend(t *testing.T) {
	store, err := journal.Open(t.TempDir())
	require.NoError(t, err)
	defer store.Close()
	root := t.TempDir()
	w := writer.New(store, root, 1024)
	row := &storagepb.TimeSeriesRow{
		Key: &storagepb.TimeSeriesKey{
			SpaceId: "crypto", DatasetId: "kline", SubjectId: "BTC", Freq: "1h",
			DataTime: "2026-01-02T00:00:00Z", SeriesTag: "venue:binance",
		},
		Fields: []*storagepb.FieldValue{{
			FieldId: "close",
			Value:   &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: 1.25}},
		}},
	}
	access := staticAccess{response: &storagepb.ReadTimeSeriesRowsRsp{
		RetInfo:           &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
		Rows:              []*storagepb.TimeSeriesRow{row},
		PageResult:        &commonpb.PageResult{},
		Complete:          false,
		ServedIndexedFrom: "2026-01-01T00:00:00Z",
		ServedIndexedTo:   "2026-01-01T01:00:00Z",
	}}
	plan := Plan{
		SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1h",
		Start: "2026-01-01T00:00:00Z", End: "2026-02-01T00:00:00Z", Confirm: true,
	}

	total, err := New(access, nil, store, w).Run(t.Context(), plan)

	require.ErrorContains(t, err, "source view is incomplete")
	require.Zero(t, total)
	dirty, err := store.DirtyPartitions(t.Context(), 10)
	require.NoError(t, err)
	require.Empty(t, dirty)
	key := domain.PartitionKey{
		SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1h",
		SeriesTag: "venue:binance", Month: "202601",
	}
	path, err := key.AbsolutePath(root)
	require.NoError(t, err)
	require.NoFileExists(t, path)
}

type staticAccess struct {
	row      *storagepb.TimeSeriesRow
	response *storagepb.ReadTimeSeriesRowsRsp
}

func (s staticAccess) Invoke(_ context.Context, service, method string, _, output any) error {
	if service != "trpc.moox.storage.PrimaryStore" || method != "ReadTimeSeriesRows" {
		return fmt.Errorf("unexpected RPC %s/%s", service, method)
	}
	response := s.response
	if response == nil {
		response = &storagepb.ReadTimeSeriesRowsRsp{
			RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, Rows: []*storagepb.TimeSeriesRow{s.row}, PageResult: &commonpb.PageResult{}, Complete: true,
		}
	}
	proto.Merge(output.(*storagepb.ReadTimeSeriesRowsRsp), response)
	return nil
}

func TestBackfillerSameRangeCanRunAgainAsNewGeneration(t *testing.T) {
	store, err := journal.Open(t.TempDir())
	require.NoError(t, err)
	defer store.Close()
	w := writer.New(store, filepath.Join(t.TempDir(), "archive"), 1024)
	row := &storagepb.TimeSeriesRow{
		Key: &storagepb.TimeSeriesKey{
			SpaceId: "crypto", DatasetId: "kline", SubjectId: "BTC", Freq: "1h",
			DataTime: "2026-01-02T00:00:00Z", SeriesTag: "venue:binance",
		},
		Fields: []*storagepb.FieldValue{{
			FieldId: "close",
			Value:   &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: 1.25}},
		}},
	}
	plan := Plan{
		SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1h",
		Start: "2026-01-01T00:00:00Z", End: "2026-02-01T00:00:00Z", Confirm: true,
	}
	b := New(staticAccess{row: row}, nil, store, w)
	first, err := b.Run(t.Context(), plan)
	require.NoError(t, err)
	second, err := b.Run(t.Context(), plan)
	require.NoError(t, err)
	require.Equal(t, 1, first)
	require.Equal(t, 1, second)
	key := domain.PartitionKey{SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1h", SeriesTag: "venue:binance", Month: "202601"}
	state, err := store.PartitionState(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, uint64(2), state.Generation)
}

func TestBackfillPatchNeverOverridesNewerRealDataAndYieldsToLaterEvents(t *testing.T) {
	partition := domain.PartitionKey{SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1m", Month: "202601"}
	dataTime := time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)
	double := func(v float64) domain.Scalar {
		return domain.Scalar{Type: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Double: &v}
	}
	patch := func(at time.Time, columns map[string]domain.Scalar) domain.RowPatch {
		return domain.RowPatch{Partition: partition, DataTime: dataTime, WrittenAt: at, Columns: columns}
	}
	real := time.Date(2026, 1, 2, 3, 5, 0, 0, time.UTC)

	// 真实更新 Y 已归档，异步 View 仍返回旧值 X 时回填：不能覆盖 Y，但可补上缺失字段
	row := domain.MergePatch(domain.ArchiveRow{}, patch(real, map[string]domain.Scalar{"close": double(2)}))
	row = domain.MergePatch(row, patch(BackfillWrittenAt, map[string]domain.Scalar{"close": double(1), "volume": double(9)}))
	assert.InDelta(t, 2.0, *row.Columns["close"].Double, 0.001, "回填的陈旧值不能覆盖较新的真实数据")
	assert.InDelta(t, 9.0, *row.Columns["volume"].Double, 0.001, "回填应补上缺失字段")

	// 回填值之后到达的真实事件可以覆盖回填值
	row = domain.MergePatch(row, patch(real.Add(time.Minute), map[string]domain.Scalar{"volume": double(10)}))
	assert.InDelta(t, 10.0, *row.Columns["volume"].Double, 0.001)

	// 已删除字段的墓碑不会被回填复活
	row = domain.MergePatch(row, patch(real.Add(2*time.Minute), map[string]domain.Scalar{"close": {Null: true}}))
	row = domain.MergePatch(row, patch(BackfillWrittenAt, map[string]domain.Scalar{"close": double(1)}))
	assert.NotContains(t, row.Columns, "close")
}
