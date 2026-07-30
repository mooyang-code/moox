package backfill

import (
	"context"
	"errors"
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
	"trpc.group/trpc-go/trpc-go/client"
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

func TestNormalizeTarget(t *testing.T) {
	assert.Equal(t, "ip://127.0.0.1:20102", NormalizeTarget("", "20102"))
	assert.Equal(t, "ip://127.0.0.1:20102", NormalizeTarget("127.0.0.1:20102", "20102"))
	assert.Equal(t, "http://storage:20102", NormalizeTarget("http://storage:20102", "20102"))
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
	b := New(nil, nil, nil, nil, nil)
	_, err := b.Run(nil, Plan{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "confirm")
}

type captureAccess struct {
	request *storagepb.ReadTimeSeriesRowsReq
}

func (c *captureAccess) ReadTimeSeriesRows(_ context.Context, request *storagepb.ReadTimeSeriesRowsReq, _ ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	c.request = request
	return nil, errors.New("stop after request capture")
}

func TestBackfillerCarriesPrimaryAuth(t *testing.T) {
	access := &captureAccess{}
	auth := &commonpb.AuthInfo{AppId: "archive-backfill", AppKey: "derived-key"}
	b := New(access, nil, auth, nil, nil)
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
	b := New(access, nil, nil, nil, nil)
	_, err := b.Run(t.Context(), Plan{SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1m", Start: "2026-01-01T00:00:00Z", End: "2026-01-02T00:00:00Z", Confirm: true})
	require.ErrorContains(t, err, "request capture")
	require.Nil(t, access.request.GetSelectors()[0].SeriesTag)
}

type staticAccess struct{ row *storagepb.TimeSeriesRow }

func (s staticAccess) ReadTimeSeriesRows(_ context.Context, _ *storagepb.ReadTimeSeriesRowsReq, _ ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	return &storagepb.ReadTimeSeriesRowsRsp{
		RetInfo:    &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
		Rows:       []*storagepb.TimeSeriesRow{s.row},
		PageResult: &commonpb.PageResult{},
	}, nil
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
	b := New(staticAccess{row: row}, nil, nil, store, w)
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
