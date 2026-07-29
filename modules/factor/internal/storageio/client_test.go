package storageio

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

func TestReadRangeChunkPrependsLookbackAndReturnsTargetPeriods(t *testing.T) {
	base := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	access := &fakeAccessClient{readRows: [][]*storagepb.TimeSeriesRow{
		{klineRow(base.Add(3*time.Minute), 4), klineRow(base.Add(4*time.Minute), 5)},
		{klineRow(base.Add(2*time.Minute), 3), klineRow(base.Add(time.Minute), 2), klineRow(base, 1)},
	}}
	c := &Client{access: access}
	chunk, err := c.ReadRangeChunk(context.Background(), WindowKey{
		SpaceID: "crypto", SourceDataset: "bars", SubjectID: "BTC-USDT", Freq: "1m",
	}, base.Add(3*time.Minute), base.Add(5*time.Minute), 4, 2000, []string{"close"})
	require.NoError(t, err)
	require.Len(t, chunk.Frame.DataTimes, 5)
	require.Equal(t, []time.Time{base.Add(3 * time.Minute), base.Add(4 * time.Minute)}, chunk.TargetPeriods)
	require.Equal(t, base, chunk.Frame.DataTimes[0])
	require.Equal(t, storagepb.SortOrder_SORT_ORDER_ASC, access.readReqs[0].GetOrder())
	require.Equal(t, storagepb.SortOrder_SORT_ORDER_DESC, access.readReqs[1].GetOrder())
}

func TestReadRangeChunkUsesCompleteTimeCohorts(t *testing.T) {
	base := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	rows := make([]*storagepb.TimeSeriesRow, 0, 2001*2)
	for period := 0; period < 2001; period++ {
		at := base.Add(time.Duration(period) * time.Minute)
		rows = append(rows,
			taggedKlineRow(at, "venue:binance", float64(period)),
			taggedKlineRow(at, "venue:okx", float64(period)),
		)
	}
	access := &pagedAccessClient{rows: rows}
	client := &Client{access: access}
	first, err := client.ReadRangeChunk(context.Background(), WindowKey{
		SpaceID: "crypto", SourceDataset: "bars", SubjectID: "BTC-USDT", Freq: "1m",
	}, base, base.Add(3000*time.Minute), 1, 2000, []string{"close"})
	require.NoError(t, err)
	require.Len(t, first.TargetPeriods, 2000)
	require.Len(t, first.Frame.Rows, 4000)
	require.Equal(t, base.Add(1999*time.Minute), first.TargetPeriods[1999])
	require.Equal(t, "venue:okx", first.Frame.SeriesTags[len(first.Frame.SeriesTags)-1])

	secondStart := first.TargetPeriods[len(first.TargetPeriods)-1].Add(time.Nanosecond)
	second, err := client.ReadRangeChunk(context.Background(), WindowKey{
		SpaceID: "crypto", SourceDataset: "bars", SubjectID: "BTC-USDT", Freq: "1m",
	}, secondStart, base.Add(3000*time.Minute), 1, 2000, []string{"close"})
	require.NoError(t, err)
	require.Equal(t, []time.Time{base.Add(2000 * time.Minute)}, second.TargetPeriods)
	require.Len(t, second.Frame.Rows, 2)
}

func TestReadRangeChunkLookbackCountsPeriodsNotRows(t *testing.T) {
	base := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	var rows []*storagepb.TimeSeriesRow
	for period := 0; period < 5; period++ {
		at := base.Add(time.Duration(period) * time.Minute)
		tags := []string{"venue:binance", "venue:okx"}
		if period == 2 {
			tags = append(tags, "venue:bybit")
		}
		for _, tag := range tags {
			rows = append(rows, taggedKlineRow(at, tag, float64(period)))
		}
	}
	client := &Client{access: &pagedAccessClient{rows: rows}}
	chunk, err := client.ReadRangeChunk(context.Background(), WindowKey{
		SpaceID: "crypto", SourceDataset: "bars", SubjectID: "BTC-USDT", Freq: "1m",
	}, base.Add(3*time.Minute), base.Add(4*time.Minute), 3, 2000, []string{"close"})
	require.NoError(t, err)
	require.Equal(t, []time.Time{base.Add(3 * time.Minute)}, chunk.TargetPeriods)
	periods := distinctTimes(chunk.Frame.DataTimes)
	require.Equal(t, []time.Time{
		base.Add(time.Minute), base.Add(2 * time.Minute), base.Add(3 * time.Minute),
	}, periods)
	require.Len(t, chunk.Frame.Rows, 7)
}

func TestWriteFactorPatchUsesResultIdentityAndWritesExplicitNullCells(t *testing.T) {
	access := &fakeAccessClient{}
	c := &Client{access: access}
	times := []time.Time{time.Unix(1, 0).UTC(), time.Unix(2, 0).UTC(), time.Unix(3, 0).UTC()}
	rowsWritten, err := c.WriteFactorPatch(context.Background(), &engine.FactorTask{
		TaskID: "t", SpaceID: "crypto", TargetDataset: "factor",
		SubjectID: "BTC", Freq: "1m",
		Factor: engine.FactorSpec{
			FactorID: "factor-a", SourceHash: "hash-a", Outputs: []string{"Bias_20", "Cci_14"},
		},
	}, &engine.FactorResult{Rows: []engine.FactorResultRow{
		{DataTime: times[0], SeriesTag: "", Values: map[string]any{"Bias_20": nil, "Cci_14": 1.0}},
		{DataTime: times[1], SeriesTag: "venue:binance", Values: map[string]any{"Bias_20": 2.0, "Cci_14": 3.0}},
		{DataTime: times[2], SeriesTag: "venue_pair:binance-okx", Values: map[string]any{"Bias_20": nil, "Cci_14": nil}},
	}})
	require.NoError(t, err)
	require.EqualValues(t, 3, rowsWritten)
	require.Len(t, access.writeReqs, 1)
	require.Len(t, access.writeReqs[0].GetRows(), 3)
	require.Equal(t, "venue:binance", access.writeReqs[0].GetRows()[1].GetKey().GetTimeSeries().GetSeriesTag())
	require.Equal(t, "factor-a", access.writeReqs[0].GetRows()[0].GetAttributes()["factor.id"].GetStringValue())
	require.Equal(t, "hash-a", access.writeReqs[0].GetRows()[0].GetAttributes()["factor.source_hash"].GetStringValue())
	nullField := fieldByID(t, access.writeReqs[0].GetRows()[0], "Bias_20")
	require.Equal(t, storagepb.NullValue_NULL_VALUE_NULL, nullField.GetValue().GetNullValue())
	require.Len(t, access.writeReqs[0].GetRows()[1].GetFields(), 2)
	require.Len(t, access.writeReqs[0].GetRows()[2].GetFields(), 2)
	require.Equal(t, storagepb.NullValue_NULL_VALUE_NULL, fieldByID(t, access.writeReqs[0].GetRows()[2], "Bias_20").GetValue().GetNullValue())
	require.Equal(t, storagepb.NullValue_NULL_VALUE_NULL, fieldByID(t, access.writeReqs[0].GetRows()[2], "Cci_14").GetValue().GetNullValue())
}

func TestWriteFactorPatchRejectsMissingOutput(t *testing.T) {
	c := &Client{access: &fakeAccessClient{}}
	_, err := c.WriteFactorPatch(context.Background(), &engine.FactorTask{
		TaskID: "t", SpaceID: "crypto", TargetDataset: "factor",
		SubjectID: "BTC", Freq: "1m", Factor: engine.FactorSpec{Outputs: []string{"Bias_20"}},
	}, &engine.FactorResult{
		Rows: []engine.FactorResultRow{{
			DataTime: time.Unix(1, 0).UTC(), Values: map[string]any{},
		}},
	})
	require.ErrorContains(t, err, "missing output Bias_20")
}

func TestNormalizeStorageTarget(t *testing.T) {
	require.Equal(t, "ip://127.0.0.1:11003", NormalizeStorageTarget("127.0.0.1:11003", "11003"))
}

type fakeAccessClient struct {
	readRows  [][]*storagepb.TimeSeriesRow
	readReqs  []*storagepb.ReadTimeSeriesRowsReq
	writeReqs []*storagepb.PrimaryUpsertFieldsReq
}

func (f *fakeAccessClient) ReadTimeSeriesRows(_ context.Context, req *storagepb.ReadTimeSeriesRowsReq, _ ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	f.readReqs = append(f.readReqs, req)
	var rows []*storagepb.TimeSeriesRow
	if len(f.readRows) > 0 {
		rows, f.readRows = f.readRows[0], f.readRows[1:]
	}
	return &storagepb.ReadTimeSeriesRowsRsp{RetInfo: successRet(), Rows: rows}, nil
}
func (f *fakeAccessClient) UpsertFields(_ context.Context, req *storagepb.PrimaryUpsertFieldsReq, _ ...client.Option) (*storagepb.PrimaryUpsertFieldsRsp, error) {
	f.writeReqs = append(f.writeReqs, req)
	return &storagepb.PrimaryUpsertFieldsRsp{RetInfo: successRet()}, nil
}

func klineRow(at time.Time, close float64) *storagepb.TimeSeriesRow {
	return taggedKlineRow(at, "", close)
}

func taggedKlineRow(at time.Time, tag string, close float64) *storagepb.TimeSeriesRow {
	return &storagepb.TimeSeriesRow{
		Key: &storagepb.TimeSeriesKey{
			SpaceId: "crypto", DatasetId: "bars", SubjectId: "BTC-USDT",
			Freq: "1m", DataTime: at.Format(time.RFC3339Nano), SeriesTag: tag,
		},
		Fields: []*storagepb.FieldValue{doubleField("close", close)},
	}
}

type pagedAccessClient struct {
	rows []*storagepb.TimeSeriesRow
}

func (f *pagedAccessClient) ReadTimeSeriesRows(
	_ context.Context,
	req *storagepb.ReadTimeSeriesRowsReq,
	_ ...client.Option,
) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	start, end := time.Time{}, time.Time{}
	if raw := req.GetTimeRange().GetStartTime(); raw != "" {
		start, _ = time.Parse(time.RFC3339Nano, raw)
	}
	if raw := req.GetTimeRange().GetEndTime(); raw != "" {
		end, _ = time.Parse(time.RFC3339Nano, raw)
	}
	filtered := make([]*storagepb.TimeSeriesRow, 0, len(f.rows))
	for _, row := range f.rows {
		at, _ := time.Parse(time.RFC3339Nano, row.GetKey().GetDataTime())
		if (!start.IsZero() && at.Before(start)) || (!end.IsZero() && !at.Before(end)) {
			continue
		}
		filtered = append(filtered, row)
	}
	sort.Slice(filtered, func(i, j int) bool {
		left, _ := time.Parse(time.RFC3339Nano, filtered[i].GetKey().GetDataTime())
		right, _ := time.Parse(time.RFC3339Nano, filtered[j].GetKey().GetDataTime())
		if req.GetOrder() == storagepb.SortOrder_SORT_ORDER_DESC {
			if !left.Equal(right) {
				return left.After(right)
			}
			return filtered[i].GetKey().GetSeriesTag() > filtered[j].GetKey().GetSeriesTag()
		}
		if !left.Equal(right) {
			return left.Before(right)
		}
		return filtered[i].GetKey().GetSeriesTag() < filtered[j].GetKey().GetSeriesTag()
	})
	page, size := req.GetPage().GetPage(), req.GetPage().GetSize()
	offset := int((page - 1) * size)
	if offset > len(filtered) {
		offset = len(filtered)
	}
	limit := offset + int(size)
	if limit > len(filtered) {
		limit = len(filtered)
	}
	return &storagepb.ReadTimeSeriesRowsRsp{
		RetInfo: successRet(), Rows: filtered[offset:limit],
		PageResult: &commonpb.PageResult{
			Page: page, Size: size, Total: uint32(len(filtered)), HasMore: limit < len(filtered),
		},
	}, nil
}

func (f *pagedAccessClient) UpsertFields(
	context.Context,
	*storagepb.PrimaryUpsertFieldsReq,
	...client.Option,
) (*storagepb.PrimaryUpsertFieldsRsp, error) {
	return &storagepb.PrimaryUpsertFieldsRsp{RetInfo: successRet()}, nil
}

func distinctTimes(values []time.Time) []time.Time {
	var out []time.Time
	for _, value := range values {
		if len(out) == 0 || !out[len(out)-1].Equal(value) {
			out = append(out, value)
		}
	}
	return out
}
func successRet() *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS, Msg: "success"}
}

func fieldByID(t *testing.T, row *storagepb.RowFieldUpsert, fieldID string) *storagepb.FieldValue {
	t.Helper()
	for _, field := range row.GetFields() {
		if field.GetFieldId() == fieldID {
			return field
		}
	}
	t.Fatalf("field %q not found in row", fieldID)
	return nil
}
