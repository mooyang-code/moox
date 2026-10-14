package primarystore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// commitMetricsNode 只实现周期提交：由测试决定 DataNode 返回什么。
type commitMetricsNode struct {
	commit func(*pb.CommitTimeSeriesBatchReq) (*pb.CommitTimeSeriesBatchRsp, error)
}

func (*commitMetricsNode) UpsertFields(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
	return nil, errors.New("unexpected ordinary upsert")
}

func (*commitMetricsNode) ReadFields(context.Context, *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
	return nil, errors.New("unexpected ordinary read")
}

func (n *commitMetricsNode) CommitTimeSeriesBatch(_ context.Context, req *pb.CommitTimeSeriesBatchReq) (*pb.CommitTimeSeriesBatchRsp, error) {
	return n.commit(req)
}

func commitMetricsService(t *testing.T, node *commitMetricsNode) (*Service, *recordingDatasetMetrics) {
	t.Helper()
	metrics := &recordingDatasetMetrics{}
	svc, err := New(Options{
		Resolver:       func(context.Context, string, string) (DataNodeClient, error) { return node, nil },
		DatasetMetrics: metrics,
		Snapshot: func() metadata.RequestSnapshot {
			return collectorPeriodMetadataSnapshot("crypto", "market_kline", "1m")
		},
		AuthSigner: func(*pb.AuthInfo) (*pb.AuthInfo, error) {
			return &pb.AuthInfo{AppId: "storage-primary", AppKey: "k"}, nil
		},
		Authorizer: func(*pb.AuthInfo) error { return nil },
	})
	require.NoError(t, err)
	return svc, metrics
}

func commitRequest(times ...time.Time) *pb.PrimaryCommitTimeSeriesBatchReq {
	items := make([]*pb.TimeSeriesBatchRow, 0, len(times))
	for i, at := range times {
		items = append(items, &pb.TimeSeriesBatchRow{SeriesIndex: uint32(i), Row: &pb.RowFieldUpsert{
			Key: &pb.RowKey{SpaceId: "crypto", DatasetId: "market_kline", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
				SubjectId: "BTC-USDT", Freq: "1m", DataTime: at.UTC().Format(time.RFC3339Nano),
			}}},
			Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}},
		}})
	}
	return &pb.PrimaryCommitTimeSeriesBatchReq{
		AuthInfo:    &pb.AuthInfo{AppId: "collector", AppKey: "caller-key"},
		Expectation: &pb.DatasetPeriodExpectation{SpaceId: "crypto", DatasetId: "market_kline", Frequency: "1m", PeriodTime: 1, SeriesHash: "h", ExpectedCount: 1},
		Items:       items, WriteSource: "collector",
	}
}

func TestPeriodCommitAdvancesTheDatasetOutputWatermark(t *testing.T) {
	node := &commitMetricsNode{commit: func(req *pb.CommitTimeSeriesBatchReq) (*pb.CommitTimeSeriesBatchRsp, error) {
		keys := make([]*pb.RowKey, 0, len(req.GetItems()))
		for _, item := range req.GetItems() {
			keys = append(keys, item.GetRow().GetKey())
		}
		return &pb.CommitTimeSeriesBatchRsp{RetInfo: successRetInfo(), Keys: keys}, nil
	}}
	svc, metrics := commitMetricsService(t, node)
	t0 := time.Date(2026, 10, 14, 1, 58, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)

	rsp, err := svc.CommitTimeSeriesBatch(context.Background(), commitRequest(t0, t1))
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())

	require.Len(t, metrics.observations, 1)
	got := metrics.observations[0]
	require.Equal(t, "success", got.Result)
	require.Equal(t, "1m", got.Key.Freq)
	require.Equal(t, uint64(2), got.Rows)
	require.True(t, got.OutputWatermark.Equal(t1), "watermark=%v want=%v", got.OutputWatermark, t1)
}

func TestPeriodCommitWithNothingAcceptedIsEmptyAndKeepsTheWatermark(t *testing.T) {
	node := &commitMetricsNode{commit: func(*pb.CommitTimeSeriesBatchReq) (*pb.CommitTimeSeriesBatchRsp, error) {
		return &pb.CommitTimeSeriesBatchRsp{RetInfo: successRetInfo()}, nil
	}}
	svc, metrics := commitMetricsService(t, node)
	_, err := svc.CommitTimeSeriesBatch(context.Background(), commitRequest(time.Date(2026, 10, 14, 1, 58, 0, 0, time.UTC)))
	require.NoError(t, err)
	require.Len(t, metrics.observations, 1)
	require.Equal(t, "empty", metrics.observations[0].Result)
	require.True(t, metrics.observations[0].OutputWatermark.IsZero())
}

func TestPeriodCommitFailureIsRecordedAsErrorWithoutAWatermark(t *testing.T) {
	cases := map[string]func(*pb.CommitTimeSeriesBatchReq) (*pb.CommitTimeSeriesBatchRsp, error){
		"transport error": func(*pb.CommitTimeSeriesBatchReq) (*pb.CommitTimeSeriesBatchRsp, error) {
			return nil, errors.New("boom")
		},
		"data node rejects": func(*pb.CommitTimeSeriesBatchReq) (*pb.CommitTimeSeriesBatchRsp, error) {
			return &pb.CommitTimeSeriesBatchRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_INNER_ERR, Msg: "bad"}}, nil
		},
		"no ret_info": func(*pb.CommitTimeSeriesBatchReq) (*pb.CommitTimeSeriesBatchRsp, error) {
			return &pb.CommitTimeSeriesBatchRsp{}, nil
		},
	}
	for name, commit := range cases {
		t.Run(name, func(t *testing.T) {
			svc, metrics := commitMetricsService(t, &commitMetricsNode{commit: commit})
			rsp, err := svc.CommitTimeSeriesBatch(context.Background(), commitRequest(time.Date(2026, 10, 14, 1, 58, 0, 0, time.UTC)))
			require.NoError(t, err)
			require.NotEqual(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
			require.Len(t, metrics.observations, 1)
			require.Equal(t, "error", metrics.observations[0].Result)
			require.True(t, metrics.observations[0].OutputWatermark.IsZero())
		})
	}
}
