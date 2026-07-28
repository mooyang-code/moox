package storageio

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

func TestReadRangeChunkPrependsLookbackAndReturnsTargetTimes(t *testing.T) {
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
	require.Equal(t, []time.Time{base.Add(3 * time.Minute), base.Add(4 * time.Minute)}, chunk.TargetTimes)
	require.Equal(t, base, chunk.Frame.DataTimes[0])
	require.Equal(t, storagepb.SortOrder_SORT_ORDER_ASC, access.readReqs[0].GetOrder())
	require.Equal(t, storagepb.SortOrder_SORT_ORDER_DESC, access.readReqs[1].GetOrder())
}

func TestWriteFactorPatchWritesExplicitNullCells(t *testing.T) {
	access := &fakeAccessClient{}
	c := &Client{access: access}
	times := []time.Time{time.Unix(1, 0).UTC(), time.Unix(2, 0).UTC(), time.Unix(3, 0).UTC()}
	err := c.WriteFactorPatch(context.Background(), &engine.FactorTask{
		TaskID: "t", SpaceID: "crypto", TargetDataset: "factor",
		SubjectID: "BTC", Freq: "1m",
	}, times, &engine.FactorResult{Columns: map[string][]any{
		"Bias_20": {nil, 2.0, nil}, "Cci_14": {1.0, 3.0, nil},
	}})
	require.NoError(t, err)
	require.Len(t, access.writeReqs, 1)
	require.Len(t, access.writeReqs[0].GetRows(), 3)
	nullField := fieldByID(t, access.writeReqs[0].GetRows()[0], "Bias_20")
	require.Equal(t, storagepb.NullValue_NULL_VALUE_NULL, nullField.GetValue().GetNullValue())
	require.Len(t, access.writeReqs[0].GetRows()[1].GetFields(), 2)
	require.Len(t, access.writeReqs[0].GetRows()[2].GetFields(), 2)
	require.Equal(t, storagepb.NullValue_NULL_VALUE_NULL, fieldByID(t, access.writeReqs[0].GetRows()[2], "Bias_20").GetValue().GetNullValue())
	require.Equal(t, storagepb.NullValue_NULL_VALUE_NULL, fieldByID(t, access.writeReqs[0].GetRows()[2], "Cci_14").GetValue().GetNullValue())
}

func TestWriteFactorPatchRejectsShortColumn(t *testing.T) {
	c := &Client{access: &fakeAccessClient{}}
	err := c.WriteFactorPatch(context.Background(), &engine.FactorTask{
		TaskID: "t", SpaceID: "crypto", TargetDataset: "factor",
		SubjectID: "BTC", Freq: "1m",
	}, []time.Time{time.Unix(1, 0).UTC(), time.Unix(2, 0).UTC()}, &engine.FactorResult{
		Columns: map[string][]any{"Bias_20": {1.0}},
	})
	require.ErrorContains(t, err, "factor column Bias_20 has 1 values for 2 target rows")
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
	return &storagepb.TimeSeriesRow{
		Key: &storagepb.TimeSeriesKey{
			SpaceId: "crypto", DatasetId: "bars", SubjectId: "BTC-USDT",
			Freq: "1m", DataTime: at.Format(time.RFC3339Nano),
		},
		Fields: []*storagepb.FieldValue{doubleField("close", close)},
	}
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
