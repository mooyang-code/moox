package storageio

import (
	"context"
	"fmt"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

func TestSubjectWindowSplitsOversizedMicrobatchWithoutPagingHistory(t *testing.T) {
	for _, tc := range []struct{ subjects, lookback, calls int }{{51, 1000, 2}, {513, 2, 2}} {
		view := &windowRPCStub{rsp: &pb.QueryTimeSeriesRowsRsp{RetInfo: &commonpb.RetInfo{}, ServedActiveIndexId: "idx", ServedInputContractVersion: "hash:1"}}
		var ids []string
		for i := 0; i < tc.subjects; i++ {
			ids = append(ids, fmt.Sprint(i))
		}
		base := time.Unix(600, 0).UTC()
		chunks, err := (&Client{view: view}).ReadPeriodChunks(context.Background(), WindowKey{SpaceID: "s", SourceViewID: "v", Freq: "1m", FilterSourceSeriesTag: true, ExpectedActiveIndexID: "idx", InputContractVersion: "hash:1"}, ids, base, base.Add(time.Minute), tc.lookback, nil)
		require.NoError(t, err)
		require.Len(t, chunks, tc.subjects)
		require.Len(t, view.reqs, tc.calls)
		seen := map[string]bool{}
		for _, req := range view.reqs {
			require.LessOrEqual(t, len(req.Selectors), 512)
			require.LessOrEqual(t, len(req.Selectors)*int(req.RowsPerSeries), 50000)
			require.Nil(t, req.Page)
			for _, selector := range req.Selectors {
				require.False(t, seen[selector.SubjectId])
				seen[selector.SubjectId] = true
			}
		}
		require.Len(t, seen, tc.subjects)
	}
}

func TestSubjectWindowSplitsProjectedCellBudget(t *testing.T) {
	view := &windowRPCStub{rsp: &pb.QueryTimeSeriesRowsRsp{RetInfo: &commonpb.RetInfo{}, ServedActiveIndexId: "idx", ServedInputContractVersion: "hash:1"}}
	columns := make([]string, 46)
	for i := range columns {
		columns[i] = fmt.Sprintf("value%d", i)
	}
	base := time.Unix(600, 0).UTC()
	key := WindowKey{SpaceID: "s", SourceViewID: "v", Freq: "1m", FilterSourceSeriesTag: true, ExpectedActiveIndexID: "idx", InputContractVersion: "hash:1"}
	chunks, err := (&Client{view: view}).ReadPeriodChunks(context.Background(), key, []string{"BTC", "ETH"}, base, base.Add(time.Minute), 10000, columns)
	require.NoError(t, err)
	require.Len(t, chunks, 2)
	require.Len(t, view.reqs, 2)
	for _, req := range view.reqs {
		require.Len(t, req.Selectors, 1)
		require.Nil(t, req.Page)
	}
	_, err = (&Client{view: view}).ReadPeriodChunks(context.Background(), key, []string{"BTC"}, base, base.Add(time.Minute), 10000, append(columns, "extra"))
	require.ErrorContains(t, err, "cell budget")
	require.Len(t, view.reqs, 2, "impossible single-subject request must fail before RPC")
}

type windowRPCStub struct {
	reqs []*pb.QueryTimeSeriesRowsReq
	rsp  *pb.QueryTimeSeriesRowsRsp
	rsps []*pb.QueryTimeSeriesRowsRsp
}

func (v *windowRPCStub) QueryTimeSeriesRows(_ context.Context, req *pb.QueryTimeSeriesRowsReq, _ ...client.Option) (*pb.QueryTimeSeriesRowsRsp, error) {
	v.reqs = append(v.reqs, req)
	if len(v.rsps) > 0 {
		rsp := v.rsps[0]
		v.rsps = v.rsps[1:]
		return rsp, nil
	}
	return v.rsp, nil
}

func TestSubjectWindowOmitsPhysicalIndexPin(t *testing.T) {
	base := time.Unix(600, 0).UTC()
	success := &pb.QueryTimeSeriesRowsRsp{
		RetInfo: &commonpb.RetInfo{}, ServedActiveIndexId: "idx-a", ServedInputContractVersion: "hash:1",
		Rows: []*pb.TimeSeriesRow{klineRowFor("BTC", base, 1)},
	}
	success.Rows[0].Key.SeriesTag = ""
	view := &windowRPCStub{rsp: success}
	key := WindowKey{SpaceID: "crypto", SourceViewID: "view", SourceDataset: "view", Freq: "1m", FilterSourceSeriesTag: true, InputContractVersion: "hash:1"}
	chunks, err := (&Client{view: view}).ReadPeriodChunks(context.Background(), key, []string{"BTC"}, base, base.Add(time.Minute), 2, []string{"close"})
	require.NoError(t, err)
	require.Len(t, view.reqs, 1)
	require.Empty(t, view.reqs[0].GetExpectedActiveIndexId())
	require.Equal(t, "hash:1", view.reqs[0].GetExpectedInputContractVersion())
	require.Equal(t, []time.Time{base}, chunks["BTC"].TargetPeriods)
}

func TestSubjectWindowBoundsLookbackStartTime(t *testing.T) {
	base := time.Date(2026, 9, 13, 4, 0, 0, 0, time.UTC)
	view := &windowRPCStub{rsp: &pb.QueryTimeSeriesRowsRsp{RetInfo: &commonpb.RetInfo{}, ServedActiveIndexId: "idx", ServedInputContractVersion: "hash:1"}}
	key := WindowKey{SpaceID: "s", SourceViewID: "v", Freq: "1m", FilterSourceSeriesTag: true, ExpectedActiveIndexID: "idx", InputContractVersion: "hash:1"}
	_, err := (&Client{view: view}).ReadPeriodChunks(context.Background(), key, []string{"BTC", "ETH"}, base, base.Add(time.Minute), 58, nil)
	require.NoError(t, err)
	require.Len(t, view.reqs, 1)
	require.Equal(t, base.Add(-57*time.Minute).UTC().Format(time.RFC3339Nano), view.reqs[0].GetTimeRange().GetStartTime())
	require.Equal(t, base.Add(time.Minute).UTC().Format(time.RFC3339Nano), view.reqs[0].GetTimeRange().GetEndTime())
}

func TestSubjectWindowUsesSingleBoundedRPCAndRejectsWrongContract(t *testing.T) {
	base := time.Unix(600, 0).UTC()
	view := &windowRPCStub{rsp: &pb.QueryTimeSeriesRowsRsp{RetInfo: &commonpb.RetInfo{}, ServedActiveIndexId: "idx", ServedInputContractVersion: "hash:1", Rows: []*pb.TimeSeriesRow{klineRowFor("BTC", base, 1), klineRowFor("BTC", base.Add(-time.Minute), 2)}}}
	for _, row := range view.rsp.Rows {
		row.Key.SeriesTag = ""
	}
	key := WindowKey{SpaceID: "crypto", SourceViewID: "view", SourceDataset: "view", SubjectID: "BTC", Freq: "1m", FilterSourceSeriesTag: true, ExpectedActiveIndexID: "idx", InputContractVersion: "hash:1"}
	c := &Client{view: view}
	chunks, err := c.ReadPeriodChunks(context.Background(), key, []string{"BTC", "MISSING"}, base, base.Add(time.Minute), 20, []string{"close"})
	require.NoError(t, err)
	require.Len(t, view.reqs, 1, "sparse subjects must not trigger historical pagination")
	require.EqualValues(t, 20, view.reqs[0].GetRowsPerSeries())
	require.Nil(t, view.reqs[0].GetPage())
	require.Len(t, view.reqs[0].GetSelectors(), 2)
	require.NotNil(t, view.reqs[0].GetSelectors()[0].SeriesTag)
	require.Empty(t, view.reqs[0].GetSelectors()[0].DatasetId, "logical View alias must not be used as primary dataset")
	require.Empty(t, chunks["MISSING"].TargetPeriods)
	require.Equal(t, []time.Time{base}, chunks["BTC"].TargetPeriods)
	require.False(t, chunks["BTC"].Complete)
	view.rsp.ServedInputContractVersion = "hash:2"
	_, err = c.ReadPeriodChunk(context.Background(), key, base, base.Add(time.Minute), 20, []string{"close"})
	require.ErrorContains(t, err, "contract changed")
}
