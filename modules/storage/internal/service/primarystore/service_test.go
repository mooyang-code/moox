package primarystore

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/report"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type primaryPeriodNodeIDAdapter struct {
	*datanode.Service
}

func (n primaryPeriodNodeIDAdapter) EnsureDatasetPeriod(ctx context.Context, req *pb.EnsureDatasetPeriodReq) (*pb.EnsureDatasetPeriodRsp, error) {
	bound := proto.Clone(req).(*pb.EnsureDatasetPeriodReq)
	bound.NodeId = "node-a"
	return n.Service.EnsureDatasetPeriod(ctx, bound)
}

func (n primaryPeriodNodeIDAdapter) GetDatasetPeriodStatus(ctx context.Context, req *pb.GetDatasetPeriodStatusReq) (*pb.GetDatasetPeriodStatusRsp, error) {
	bound := proto.Clone(req).(*pb.GetDatasetPeriodStatusReq)
	bound.NodeId = "node-a"
	return n.Service.GetDatasetPeriodStatus(ctx, bound)
}

func (n primaryPeriodNodeIDAdapter) CommitTimeSeriesBatch(ctx context.Context, req *pb.CommitTimeSeriesBatchReq) (*pb.CommitTimeSeriesBatchRsp, error) {
	bound := proto.Clone(req).(*pb.CommitTimeSeriesBatchReq)
	bound.NodeId = "node-a"
	return n.Service.CommitTimeSeriesBatch(ctx, bound)
}

func (n primaryPeriodNodeIDAdapter) RecordDatasetPeriodFailures(ctx context.Context, req *pb.RecordDatasetPeriodFailuresReq) (*pb.RecordDatasetPeriodFailuresRsp, error) {
	bound := proto.Clone(req).(*pb.RecordDatasetPeriodFailuresReq)
	bound.NodeId = "node-a"
	return n.Service.RecordDatasetPeriodFailures(ctx, bound)
}

func TestPrimaryPeriodReceiptsDeadlineAndReadonlyStatus(t *testing.T) {
	ctx := context.Background()
	deadline := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	var now atomic.Int64
	now.Store(deadline.Add(-time.Nanosecond).UnixNano())
	const secret = "period-status-secret"
	node, err := datanode.NewService(datanode.Options{NodeID: "node-a", AuthSecret: secret, Pebble: pebble.Options{
		NodeID: "node-a", Path: filepath.Join(t.TempDir(), "node"), PeriodNow: func() time.Time { return time.Unix(0, now.Load()).UTC() },
	}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, node.Close()) })
	service, err := New(Options{
		Node:     primaryPeriodNodeIDAdapter{Service: node},
		Snapshot: func() metadata.RequestSnapshot { return collectorPeriodMetadataSnapshot("crypto", "bars", "1m") },
		AuthSigner: func(*pb.AuthInfo) (*pb.AuthInfo, error) {
			return &pb.AuthInfo{AppId: "primary", AppKey: datanode.ServiceAuthKey(secret, "primary")}, nil
		},
	})
	require.NoError(t, err)
	auth := &pb.AuthInfo{AppId: "collector", AppKey: "caller-key"}
	exp := &pb.DatasetPeriodExpectation{SpaceId: "crypto", DatasetId: "bars", Frequency: "1m", PeriodTime: deadline.Add(-time.Minute).Unix(), SeriesHash: "hash", ExpectedCount: 2, DeadlineAt: deadline.Unix(), ReservationId: "release-a", SeriesSnapshot: []*pb.DatasetPeriodSeries{
		{SeriesIndex: 0, SubjectId: "BTC-USDT", SeriesTag: "okx"}, {SeriesIndex: 1, SubjectId: "BTC-USDT", SeriesTag: "binance"},
	}}
	query := proto.Clone(exp).(*pb.DatasetPeriodExpectation)
	query.SeriesSnapshot = nil
	status, err := service.GetDatasetPeriodStatus(ctx, &pb.PrimaryGetDatasetPeriodStatusReq{AuthInfo: auth, Expectation: query})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NOT_FOUND, status.GetRetInfo().GetCode())
	require.Empty(t, status.GetStatus())
	ensured, err := service.EnsureDatasetPeriod(ctx, &pb.PrimaryEnsureDatasetPeriodReq{AuthInfo: auth, Expectation: exp})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, ensured.GetRetInfo().GetCode())
	require.Equal(t, deadline.Unix(), ensured.GetDeadlineAt())
	retry := proto.Clone(exp).(*pb.DatasetPeriodExpectation)
	retry.DeadlineAt += 3600
	ensured, err = service.EnsureDatasetPeriod(ctx, &pb.PrimaryEnsureDatasetPeriodReq{AuthInfo: auth, Expectation: retry})
	require.NoError(t, err)
	require.Equal(t, deadline.Unix(), ensured.GetDeadlineAt())
	assertStatus := func(expectation *pb.DatasetPeriodExpectation, want string) {
		t.Helper()
		response, err := service.GetDatasetPeriodStatus(ctx, &pb.PrimaryGetDatasetPeriodStatusReq{AuthInfo: auth, Expectation: expectation})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_SUCCESS, response.GetRetInfo().GetCode())
		require.Equal(t, want, response.GetStatus())
		require.Equal(t, exp.GetSeriesHash(), response.GetSeriesHash())
		require.Equal(t, exp.GetExpectedCount(), response.GetExpectedCount())
		require.Equal(t, deadline.Unix(), response.GetDeadlineAt())
	}
	assertStatus(query, "waiting")
	for _, change := range []func(*pb.DatasetPeriodExpectation){func(x *pb.DatasetPeriodExpectation) { x.SeriesHash = "wrong" }, func(x *pb.DatasetPeriodExpectation) { x.ExpectedCount++ }, func(x *pb.DatasetPeriodExpectation) { x.ReservationId = "release-b" }} {
		bad := proto.Clone(query).(*pb.DatasetPeriodExpectation)
		change(bad)
		status, err = service.GetDatasetPeriodStatus(ctx, &pb.PrimaryGetDatasetPeriodStatusReq{AuthInfo: auth, Expectation: bad})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_CONFLICT, status.GetRetInfo().GetCode())
	}
	failed, err := service.RecordDatasetPeriodFailures(ctx, &pb.PrimaryRecordDatasetPeriodFailuresReq{AuthInfo: auth, Expectation: query, SeriesIndexes: []uint32{1, 0, 1}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, failed.GetRetInfo().GetCode())
	require.Len(t, failed.GetResults(), 2)
	for index, result := range failed.GetResults() {
		require.Equal(t, uint32(index), result.GetSeriesIndex())
		require.Equal(t, pb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_RECORDED, result.GetDisposition())
	}
	items := []*pb.TimeSeriesBatchRow{}
	for index, tag := range []string{"okx", "binance"} {
		items = append(items, &pb.TimeSeriesBatchRow{SeriesIndex: uint32(index), Row: primaryPeriodRow(exp, tag)})
	}
	committed, err := service.CommitTimeSeriesBatch(ctx, &pb.PrimaryCommitTimeSeriesBatchReq{AuthInfo: auth, Expectation: query, Items: items, WriteSource: "collector"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, committed.GetRetInfo().GetCode())
	require.Equal(t, []uint32{0, 1}, committed.GetAcceptedSeriesIndexes())
	assertStatus(query, "complete")
	failed, err = service.RecordDatasetPeriodFailures(ctx, &pb.PrimaryRecordDatasetPeriodFailuresReq{AuthInfo: auth, Expectation: query, SeriesIndexes: []uint32{1, 0}})
	require.NoError(t, err)
	for _, result := range failed.GetResults() {
		require.Equal(t, pb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_ALREADY_SUCCEEDED, result.GetDisposition())
	}
	degraded := proto.Clone(exp).(*pb.DatasetPeriodExpectation)
	degraded.PeriodTime -= 60
	_, err = service.EnsureDatasetPeriod(ctx, &pb.PrimaryEnsureDatasetPeriodReq{AuthInfo: auth, Expectation: degraded})
	require.NoError(t, err)
	now.Store(deadline.UnixNano())
	assertStatus(degraded, "waiting")
	committed, err = service.CommitTimeSeriesBatch(ctx, &pb.PrimaryCommitTimeSeriesBatchReq{AuthInfo: auth, Expectation: degraded, Items: []*pb.TimeSeriesBatchRow{{SeriesIndex: 0, Row: primaryPeriodRow(degraded, "okx")}}, WriteSource: "collector"})
	require.NoError(t, err)
	require.Equal(t, "degraded", committed.GetPeriodStatus())
	require.Empty(t, committed.GetAcceptedSeriesIndexes())
	failed, err = service.RecordDatasetPeriodFailures(ctx, &pb.PrimaryRecordDatasetPeriodFailuresReq{AuthInfo: auth, Expectation: degraded, SeriesIndexes: []uint32{0, 1}})
	require.NoError(t, err)
	for _, result := range failed.GetResults() {
		require.Equal(t, pb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_MISSED_DEADLINE, result.GetDisposition())
	}
	assertStatus(degraded, "degraded")
}

func primaryPeriodRow(exp *pb.DatasetPeriodExpectation, tag string) *pb.RowFieldUpsert {
	return &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: exp.GetSpaceId(), DatasetId: exp.GetDatasetId(), Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", SeriesTag: tag, Freq: exp.GetFrequency(), DataTime: time.Unix(exp.GetPeriodTime(), 0).UTC().Format(time.RFC3339Nano)}}}, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}}}
}

func TestPrimaryPeriodRejectsNilSnapshotSlots(t *testing.T) {
	for _, count := range []uint32{2, 3} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			ctx := context.Background()
			period := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
			const secret = "nil-snapshot-secret"
			node, err := datanode.NewService(datanode.Options{NodeID: "node-a", AuthSecret: secret, Pebble: pebble.Options{NodeID: "node-a", Path: filepath.Join(t.TempDir(), "node"), PeriodNow: func() time.Time { return period }}})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, node.Close()) })
			service, err := New(Options{
				Node:     primaryPeriodNodeIDAdapter{Service: node},
				Snapshot: func() metadata.RequestSnapshot { return collectorPeriodMetadataSnapshot("crypto", "bars", "1m") },
				AuthSigner: func(*pb.AuthInfo) (*pb.AuthInfo, error) {
					return &pb.AuthInfo{AppId: "primary", AppKey: datanode.ServiceAuthKey(secret, "primary")}, nil
				},
			})
			require.NoError(t, err)
			auth := &pb.AuthInfo{AppId: "collector", AppKey: "caller-key"}
			exp := &pb.DatasetPeriodExpectation{SpaceId: "crypto", DatasetId: "bars", Frequency: "1m", PeriodTime: period.Unix(), SeriesHash: "hash", ExpectedCount: count, DeadlineAt: period.Add(time.Hour).Unix(), SeriesSnapshot: []*pb.DatasetPeriodSeries{{SeriesIndex: 0, SubjectId: "BTC-USDT", SeriesTag: "okx"}, nil, {SeriesIndex: 1, SubjectId: "ETH-USDT", SeriesTag: "okx"}}}
			rsp, err := service.EnsureDatasetPeriod(ctx, &pb.PrimaryEnsureDatasetPeriodReq{AuthInfo: auth, Expectation: exp})
			require.NoError(t, err)
			require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
			exp.SeriesSnapshot = nil
			status, err := service.GetDatasetPeriodStatus(ctx, &pb.PrimaryGetDatasetPeriodStatusReq{AuthInfo: auth, Expectation: exp})
			require.NoError(t, err)
			require.Equal(t, pb.ErrorCode_NOT_FOUND, status.GetRetInfo().GetCode())
			entries, err := node.Store().ListOutbox(ctx, 0, 100)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}

func TestPrimaryEnsureRejectsNonPositiveDeadlineBeforeDataNodeCall(t *testing.T) {
	for _, deadlineAt := range []int64{0, -1} {
		t.Run(fmt.Sprint(deadlineAt), func(t *testing.T) {
			node := &periodAuthorizationNode{}
			service, err := New(Options{
				Node:     node,
				Snapshot: func() metadata.RequestSnapshot { return collectorPeriodMetadataSnapshot("crypto", "bars", "1m") },
				AuthSigner: func(*pb.AuthInfo) (*pb.AuthInfo, error) {
					return &pb.AuthInfo{AppId: "primary", AppKey: "node-key"}, nil
				},
			})
			require.NoError(t, err)

			expectation := &pb.DatasetPeriodExpectation{
				SpaceId: "crypto", DatasetId: "bars", Frequency: "1m", PeriodTime: 123,
				SeriesHash: "hash", ExpectedCount: 1, DeadlineAt: deadlineAt,
				SeriesSnapshot: []*pb.DatasetPeriodSeries{{SeriesIndex: 0, SubjectId: "BTC-USDT", SeriesTag: "okx"}},
			}
			rsp, err := service.EnsureDatasetPeriod(context.Background(), &pb.PrimaryEnsureDatasetPeriodReq{
				AuthInfo: &pb.AuthInfo{AppId: "collector", AppKey: "caller-key"}, Expectation: expectation,
			})
			require.NoError(t, err)
			require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
			require.Zero(t, node.ensure)
		})
	}
}

type emptyPeriodNode struct {
	DataNodeClient
	nilResponse bool
}

func (n emptyPeriodNode) EnsureDatasetPeriod(context.Context, *pb.EnsureDatasetPeriodReq) (*pb.EnsureDatasetPeriodRsp, error) {
	if n.nilResponse {
		return nil, nil
	}
	return &pb.EnsureDatasetPeriodRsp{}, nil
}

func (n emptyPeriodNode) GetDatasetPeriodStatus(context.Context, *pb.GetDatasetPeriodStatusReq) (*pb.GetDatasetPeriodStatusRsp, error) {
	if n.nilResponse {
		return nil, nil
	}
	return &pb.GetDatasetPeriodStatusRsp{}, nil
}

func (n emptyPeriodNode) CommitTimeSeriesBatch(context.Context, *pb.CommitTimeSeriesBatchReq) (*pb.CommitTimeSeriesBatchRsp, error) {
	if n.nilResponse {
		return nil, nil
	}
	return &pb.CommitTimeSeriesBatchRsp{}, nil
}

func (n emptyPeriodNode) RecordDatasetPeriodFailures(context.Context, *pb.RecordDatasetPeriodFailuresReq) (*pb.RecordDatasetPeriodFailuresRsp, error) {
	if n.nilResponse {
		return nil, nil
	}
	return &pb.RecordDatasetPeriodFailuresRsp{}, nil
}

func TestPrimaryPeriodRejectsMissingRPCResponses(t *testing.T) {
	for _, nilResponse := range []bool{false, true} {
		service, err := New(Options{
			Node:       emptyPeriodNode{nilResponse: nilResponse},
			Snapshot:   func() metadata.RequestSnapshot { return collectorPeriodMetadataSnapshot("crypto", "bars", "1m") },
			AuthSigner: func(auth *pb.AuthInfo) (*pb.AuthInfo, error) { return auth, nil },
		})
		require.NoError(t, err)
		ctx := context.Background()
		auth := &pb.AuthInfo{AppId: "collector", AppKey: "caller-key"}
		exp := &pb.DatasetPeriodExpectation{SpaceId: "crypto", DatasetId: "bars", Frequency: "1m", PeriodTime: 1, SeriesHash: "hash", ExpectedCount: 1, DeadlineAt: time.Now().Add(time.Hour).Unix()}
		ensured, err := service.EnsureDatasetPeriod(ctx, &pb.PrimaryEnsureDatasetPeriodReq{AuthInfo: auth, Expectation: exp})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_INNER_ERR, ensured.GetRetInfo().GetCode())
		status, err := service.GetDatasetPeriodStatus(ctx, &pb.PrimaryGetDatasetPeriodStatusReq{AuthInfo: auth, Expectation: exp})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_INNER_ERR, status.GetRetInfo().GetCode())
		committed, err := service.CommitTimeSeriesBatch(ctx, &pb.PrimaryCommitTimeSeriesBatchReq{AuthInfo: auth, Expectation: exp, Items: []*pb.TimeSeriesBatchRow{{Row: primaryPeriodRow(exp, "")}}, WriteSource: "collector"})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_INNER_ERR, committed.GetRetInfo().GetCode())
		failed, err := service.RecordDatasetPeriodFailures(ctx, &pb.PrimaryRecordDatasetPeriodFailuresReq{AuthInfo: auth, Expectation: exp, SeriesIndexes: []uint32{0}})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_INNER_ERR, failed.GetRetInfo().GetCode())
	}
}

type recordingNode struct {
	write func(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error)
	read  func(context.Context, *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error)
}

type recordingMarkerNode struct {
	*recordingNode
	markerCalls []string
}

type recordingView struct {
	query func(context.Context, *pb.QueryTimeSeriesRowsReq) (*pb.QueryTimeSeriesRowsRsp, error)
}

func (v *recordingView) QueryTimeSeriesRows(ctx context.Context, req *pb.QueryTimeSeriesRowsReq) (*pb.QueryTimeSeriesRowsRsp, error) {
	return v.query(ctx, req)
}

func (*recordingView) SearchRecordRows(context.Context, *pb.SearchRecordRowsReq) (*pb.SearchRecordRowsRsp, error) {
	return nil, errors.New("unexpected record query")
}

func (n *recordingNode) UpsertFields(ctx context.Context, req *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
	return n.write(ctx, req)
}

func (n *recordingNode) ReadFields(ctx context.Context, req *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
	return n.read(ctx, req)
}

func (n *recordingMarkerNode) AppendCollectorPeriodCompleted(context.Context, *pb.AppendCollectorPeriodCompletedReq) (*pb.AppendCollectorPeriodCompletedRsp, error) {
	n.markerCalls = append(n.markerCalls, "collector")
	return &pb.AppendCollectorPeriodCompletedRsp{RetInfo: successRetInfo()}, nil
}

func (n *recordingMarkerNode) AppendFactorPeriodComputed(context.Context, *pb.AppendFactorPeriodComputedReq) (*pb.AppendFactorPeriodComputedRsp, error) {
	n.markerCalls = append(n.markerCalls, "factor")
	return &pb.AppendFactorPeriodComputedRsp{RetInfo: successRetInfo()}, nil
}

func (n *recordingMarkerNode) AppendDatasetSyncPointMarker(context.Context, *pb.AppendDatasetSyncPointMarkerReq) (*pb.AppendDatasetSyncPointMarkerRsp, error) {
	n.markerCalls = append(n.markerCalls, "sync-point")
	return &pb.AppendDatasetSyncPointMarkerRsp{RetInfo: successRetInfo()}, nil
}

func (*recordingMarkerNode) GetFactorPeriodComputedMarker(context.Context, *pb.GetFactorPeriodComputedMarkerReq) (*pb.GetFactorPeriodComputedMarkerRsp, error) {
	return &pb.GetFactorPeriodComputedMarkerRsp{RetInfo: successRetInfo()}, nil
}

func (n *recordingNode) GetNodeState(context.Context, *pb.GetNodeStateReq) (*pb.GetNodeStateRsp, error) {
	return &pb.GetNodeStateRsp{}, nil
}

func (n *recordingNode) CleanupExpiredBuckets(context.Context, *pb.CleanupExpiredBucketsReq) (*pb.CleanupExpiredBucketsRsp, error) {
	return &pb.CleanupExpiredBucketsRsp{}, nil
}

func TestPrimaryRoutesAndValidatesBeforeDataNode(t *testing.T) {
	node, err := datanode.NewService(datanode.Options{NodeID: "node-a", AuthSecret: "node-secret", Pebble: pebble.Options{NodeID: "node-a", Path: filepath.Join(t.TempDir(), "node")}})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	svc, err := New(Options{Node: node, AuthSigner: func(_ *pb.AuthInfo) (*pb.AuthInfo, error) {
		return &pb.AuthInfo{AppId: "primary", AppKey: datanode.ServiceAuthKey("node-secret", "primary")}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	key := &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}}
	rsp, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "ignored"}, Rows: []*pb.RowFieldUpsert{{Key: key, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1.2}}}}}}})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("write rsp=%v err=%v", rsp, err)
	}
	bad, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{Rows: []*pb.RowFieldUpsert{{Key: key}}})
	if err != nil || bad.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
		t.Fatalf("bad rsp=%v err=%v", bad, err)
	}
}

func TestPrimaryNormalizesMissingStockCNSeriesTag(t *testing.T) {
	var captured *pb.UpsertFieldsReq
	node := &recordingNode{write: func(_ context.Context, req *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
		captured = req
		return &pb.UpsertFieldsRsp{RetInfo: successRetInfo()}, nil
	}}
	svc, err := New(Options{Node: node})
	require.NoError(t, err)
	row := &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: "stockcn", DatasetId: "dataset_stockcn_equity_kline_1m", Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "600000.XSHG", Freq: "1m", DataTime: "2026-08-28T06:59:00Z"}}}, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}}}
	_, err = svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{AuthInfo: &pb.AuthInfo{AppId: "collector"}, Rows: []*pb.RowFieldUpsert{row}})
	require.NoError(t, err)
	require.NotNil(t, captured)
	require.Equal(t, "default", captured.GetRows()[0].GetKey().GetTimeSeries().GetSeriesTag())
}

func TestPrimaryWriteMethodsStillAllowInternalCallers(t *testing.T) {
	node := &recordingMarkerNode{recordingNode: &recordingNode{
		write: func(_ context.Context, req *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
			return &pb.UpsertFieldsRsp{RetInfo: successRetInfo(), Keys: []*pb.RowKey{req.GetRows()[0].GetKey()}}, nil
		},
		read: func(context.Context, *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
			return &pb.ReadFieldsRsp{RetInfo: successRetInfo()}, nil
		},
	}}
	svc, err := New(Options{Node: node, Snapshot: func() metadata.RequestSnapshot { return markerRoutingSnapshot{} }})
	if err != nil {
		t.Fatal(err)
	}
	row := &pb.RowFieldUpsert{
		Key:    &pb.RowKey{SpaceId: "space", DatasetId: "dataset", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "record", Version: "1"}}},
		Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "allowed"}}}},
	}
	upsert, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "collector"}, Rows: []*pb.RowFieldUpsert{row},
	})
	if err != nil || upsert.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("upsert rsp=%v err=%v", upsert, err)
	}
	collected, err := svc.ReportCollectorPeriodCompleted(context.Background(), &pb.ReportCollectorPeriodCompletedReq{
		AuthInfo: &pb.AuthInfo{AppId: "collector"}, SpaceId: "space", Marker: &pb.CollectorPeriodCompletedMarker{DatasetId: "collector-data"},
	})
	if err != nil || collected.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("collected rsp=%v err=%v", collected, err)
	}
	computed, err := svc.ReportFactorPeriodComputed(context.Background(), &pb.ReportFactorPeriodComputedReq{
		AuthInfo: &pb.AuthInfo{AppId: "factor"}, SpaceId: "space", Marker: &pb.FactorPeriodComputedMarker{DatasetId: "factor-data"},
	})
	if err != nil || computed.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("computed rsp=%v err=%v", computed, err)
	}
	syncPoint, err := svc.AppendDatasetSyncPoint(context.Background(), &pb.AppendDatasetSyncPointReq{
		AuthInfo: &pb.AuthInfo{AppId: "storage-view"}, SpaceId: "space",
		SyncPoint: &pb.DatasetSyncPointMarker{DatasetId: "dataset", RequestId: "request", Source: "import"},
	})
	if err != nil || syncPoint.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("sync point rsp=%v err=%v", syncPoint, err)
	}
	if !reflect.DeepEqual(node.markerCalls, []string{"collector", "factor", "sync-point"}) {
		t.Fatalf("marker calls=%v", node.markerCalls)
	}
}

func TestFactorResultRequiresDedicatedWriteRPC(t *testing.T) {
	resolved := 0
	node := &recordingMarkerNode{recordingNode: &recordingNode{
		write: func(_ context.Context, _ *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
			t.Fatal("factor_result generic upsert reached DataNode")
			return nil, nil
		},
	}}
	svc, err := New(Options{
		Resolver: func(context.Context, string, string) (DataNodeClient, error) {
			resolved++
			return node, nil
		},
		Snapshot: func() metadata.RequestSnapshot {
			return factorResultSnapshot{}
		},
	})
	require.NoError(t, err)
	row := &pb.RowFieldUpsert{
		Key:    &pb.RowKey{SpaceId: "space", DatasetId: "factor_result", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "row", Version: "1"}}},
		Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "ok"}}}},
	}
	for _, appID := range []string{"factor", "moox-factor", "collector"} {
		upsert, upsertErr := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{AuthInfo: &pb.AuthInfo{AppId: appID}, Rows: []*pb.RowFieldUpsert{row}})
		require.NoError(t, upsertErr, appID)
		require.Equal(t, pb.ErrorCode_NO_PERMISSION, upsert.GetRetInfo().GetCode(), appID)
		require.Contains(t, upsert.GetRetInfo().GetMsg(), "requires WriteFactorRows", appID)
		require.Equal(t, 0, resolved, appID)
	}
	for _, appID := range []string{"factor", "moox-factor"} {
		computed, markerErr := svc.ReportFactorPeriodComputed(context.Background(), &pb.ReportFactorPeriodComputedReq{
			AuthInfo: &pb.AuthInfo{AppId: appID}, SpaceId: "space",
			Marker: &pb.FactorPeriodComputedMarker{DatasetId: "factor_result"},
		})
		require.NoError(t, markerErr, appID)
		require.Equal(t, pb.ErrorCode_SUCCESS, computed.GetRetInfo().GetCode(), appID)
	}
}

type factorResultSnapshot struct{}

type markerRoutingSnapshot struct{}

func (markerRoutingSnapshot) GetDataset(spaceID, datasetID string) (*pb.Dataset, bool) {
	attrs := map[string]string{}
	switch datasetID {
	case "collector-data":
		attrs = map[string]string{"owner_module": "collector", "dataset_role": "raw_collection"}
	case "factor-data":
		attrs = map[string]string{"owner_module": "factor", "dataset_role": "factor_result", "write_owner": "factor"}
	}
	return &pb.Dataset{SpaceId: spaceID, DatasetId: datasetID, DataNodeId: "node-owner", Attributes: attrs}, true
}

func (markerRoutingSnapshot) GetDataNode(string) (*pb.DataNode, bool) { return nil, false }

func (markerRoutingSnapshot) ListDatasetColumns(string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (factorResultSnapshot) GetDataset(spaceID, datasetID string) (*pb.Dataset, bool) {
	return &pb.Dataset{
		SpaceId: spaceID, DatasetId: datasetID, DataNodeId: "node-owner",
		Attributes: map[string]string{"owner_module": "factor", "dataset_role": "factor_result", "write_owner": "factor"},
	}, true
}

func (factorResultSnapshot) GetDataNode(string) (*pb.DataNode, bool) { return nil, false }

func (factorResultSnapshot) ListDatasetColumns(string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func TestPrimaryRangeReadsRequireCallerAuthorization(t *testing.T) {
	svc, err := New(Options{
		Node: &recordingNode{
			write: func(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
				return &pb.UpsertFieldsRsp{}, nil
			},
			read: func(context.Context, *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
				return &pb.ReadFieldsRsp{}, nil
			},
		},
		Authorizer: func(*pb.AuthInfo) error { return errors.New("denied") },
	})
	if err != nil {
		t.Fatal(err)
	}
	records, err := svc.ReadRecordRows(context.Background(), &pb.ReadRecordRowsReq{
		Keys:         []*pb.RecordKey{{SpaceId: "space", DatasetId: "dataset", RecordId: "record"}},
		VersionRange: &pb.VersionRange{},
	})
	if err != nil || records.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
		t.Fatalf("record range read rsp=%v err=%v", records, err)
	}
	timeSeries, err := svc.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{
		SpaceId:   "space",
		DatasetId: "dataset",
		Selectors: []*pb.TimeSeriesSelector{{SpaceId: "space", DatasetId: "dataset", SubjectId: "subject", Freq: "1m"}},
		TimeRange: &pb.TimeRange{},
	})
	if err != nil || timeSeries.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
		t.Fatalf("time-series range read rsp=%v err=%v", timeSeries, err)
	}
}

func TestReadTimeSeriesRowsUsesSelectorsAndCopiesViewCompleteness(t *testing.T) {
	var captured *pb.QueryTimeSeriesRowsReq
	view := &recordingView{query: func(_ context.Context, req *pb.QueryTimeSeriesRowsReq) (*pb.QueryTimeSeriesRowsRsp, error) {
		captured = req
		return &pb.QueryTimeSeriesRowsRsp{
			RetInfo:           successRetInfo(),
			Rows:              []*pb.TimeSeriesRow{{Key: &pb.TimeSeriesKey{SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-29T00:00:00Z", SeriesTag: "venue:okx"}}},
			ServedIndexedFrom: "2026-07-29T00:00:00Z",
			ServedIndexedTo:   "2026-07-29T00:01:00Z",
			Complete:          true,
		}, nil
	}}
	node := &recordingNode{
		write: func(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
			return nil, errors.New("unexpected write")
		},
		read: func(context.Context, *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
			return nil, errors.New("ReadTimeSeriesRows must not use point ReadFields")
		},
	}
	svc, err := New(Options{
		Node: node,
		View: func(_ context.Context, spaceID, datasetID string) (pb.DataViewService, string, error) {
			if spaceID != "space" || datasetID != "market" {
				t.Fatalf("resolved scope=%s/%s", spaceID, datasetID)
			}
			return view, "prices", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	empty := ""
	okx := "venue:okx"
	selectors := []*pb.TimeSeriesSelector{
		{SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m"},
		{SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m", SeriesTag: &empty},
		{SpaceId: "space", DatasetId: "market", SubjectId: "BTC-USDT", Freq: "1m", SeriesTag: &okx},
	}
	rsp, err := svc.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{
		AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"},
		SpaceId:  "space", DatasetId: "market", Selectors: selectors,
		Order: pb.SortOrder_SORT_ORDER_DESC,
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("rsp=%v err=%v", rsp, err)
	}
	if rsp.GetServedIndexedFrom() != "2026-07-29T00:00:00Z" ||
		rsp.GetServedIndexedTo() != "2026-07-29T00:01:00Z" || !rsp.GetComplete() {
		t.Fatalf("view completeness was not copied: %v", rsp)
	}
	if captured == nil || len(captured.GetSelectors()) != 3 {
		t.Fatalf("selectors not forwarded: %v", captured)
	}
	for i := range selectors {
		if (captured.GetSelectors()[i].SeriesTag == nil) != (selectors[i].SeriesTag == nil) ||
			captured.GetSelectors()[i].GetSeriesTag() != selectors[i].GetSeriesTag() {
			t.Fatalf("selector %d presence changed: got=%v want=%v", i, captured.GetSelectors()[i], selectors[i])
		}
	}
	wantSorts := []string{"subject_id", "freq", "data_time", "series_tag"}
	if len(captured.GetSorts()) != len(wantSorts) {
		t.Fatalf("sorts=%v", captured.GetSorts())
	}
	for i, field := range wantSorts {
		if captured.GetSorts()[i].GetFieldName() != field || !captured.GetSorts()[i].GetDesc() {
			t.Fatalf("sort %d=%v", i, captured.GetSorts()[i])
		}
	}
	if len(rsp.GetRows()) != 1 || rsp.GetRows()[0].GetKey().GetSeriesTag() != "venue:okx" {
		t.Fatalf("exact result tag lost: %v", rsp.GetRows())
	}
}

func TestStockReadForwardsExactDefaultSeriesToView(t *testing.T) {
	var captured *pb.QueryTimeSeriesRowsReq
	view := &recordingView{query: func(_ context.Context, req *pb.QueryTimeSeriesRowsReq) (*pb.QueryTimeSeriesRowsRsp, error) {
		captured = req
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: successRetInfo()}, nil
	}}
	svc, err := New(Options{
		Node: &recordingNode{},
		View: func(_ context.Context, spaceID, datasetID string) (pb.DataViewService, string, error) {
			if spaceID != "stockcn" || datasetID != "dataset_stockcn_equity_kline_1m" {
				t.Fatalf("resolved scope=%s/%s", spaceID, datasetID)
			}
			return view, "view_stockcn_equity_kline_1m", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defaultTag := "default"
	rsp, err := svc.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{
		AuthInfo: &pb.AuthInfo{AppId: "moox-skill"},
		SpaceId:  "stockcn", DatasetId: "dataset_stockcn_equity_kline_1m", Order: pb.SortOrder_SORT_ORDER_DESC,
		Selectors: []*pb.TimeSeriesSelector{{
			SpaceId: "stockcn", DatasetId: "dataset_stockcn_equity_kline_1m", SubjectId: "600000.SH", Freq: "1m", SeriesTag: &defaultTag,
		}},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("rsp=%v err=%v", rsp, err)
	}
	if captured == nil || len(captured.GetSelectors()) != 1 || captured.GetSelectors()[0].SeriesTag == nil || captured.GetSelectors()[0].GetSeriesTag() != defaultTag {
		t.Fatalf("exact default series_tag was not forwarded: %v", captured)
	}
}

func TestPrimaryExactTimeSeriesReadOmitsMissingRows(t *testing.T) {
	var existing *pb.RowKey
	node := &recordingNode{
		write: func(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
			return &pb.UpsertFieldsRsp{}, nil
		},
		read: func(_ context.Context, req *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
			rows := make([]*pb.RowFieldValues, 0, len(req.GetKeys()))
			for _, key := range req.GetKeys() {
				rows = append(rows, &pb.RowFieldValues{Key: key})
			}
			existing = req.GetKeys()[1]
			rows[1].Fields = []*pb.FieldValue{{
				FieldId: "close",
				Value:   &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}},
			}}
			return &pb.ReadFieldsRsp{
				RetInfo:      successRetInfo(),
				Rows:         rows,
				ExistingKeys: []*pb.RowKey{existing},
			}, nil
		},
	}
	svc, err := New(Options{Node: node})
	if err != nil {
		t.Fatal(err)
	}
	keys := []*pb.TimeSeriesKey{
		{SpaceId: "space", DatasetId: "dataset", SubjectId: "subject", Freq: "1h", DataTime: "2026-07-29T16:00:00Z", SeriesTag: "venue:okx"},
		{SpaceId: "space", DatasetId: "dataset", SubjectId: "subject", Freq: "1h", DataTime: "2026-07-29T15:00:00Z", SeriesTag: "venue:binance"},
	}
	rsp, err := svc.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{
		AuthInfo:    &pb.AuthInfo{AppId: "caller", AppKey: "key"},
		SpaceId:     "space",
		DatasetId:   "dataset",
		Keys:        keys,
		ColumnNames: []string{"close"},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("read rsp=%v err=%v", rsp, err)
	}
	if len(rsp.GetRows()) != 1 || rsp.GetRows()[0].GetKey().GetDataTime() != keys[1].GetDataTime() ||
		rsp.GetRows()[0].GetKey().GetSeriesTag() != "venue:binance" {
		t.Fatalf("rows=%v want only existing key %v", rsp.GetRows(), existing)
	}
}

func TestPrimaryTimeSeriesReadRejectsAmbiguousOrMismatchedScope(t *testing.T) {
	svc, err := New(Options{Node: &recordingNode{
		write: func(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
			return nil, errors.New("unexpected write")
		},
		read: func(context.Context, *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
			return nil, errors.New("invalid request reached DataNode")
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	key := &pb.TimeSeriesKey{
		SpaceId: "space", DatasetId: "dataset", SubjectId: "subject", Freq: "1m",
		DataTime: "2026-07-29T15:00:00Z", SeriesTag: "venue:binance",
	}
	selector := &pb.TimeSeriesSelector{
		SpaceId: "space", DatasetId: "dataset", SubjectId: "subject", Freq: "1m",
	}
	for _, tc := range []struct {
		name string
		req  *pb.ReadTimeSeriesRowsReq
	}{
		{
			name: "keys and selectors",
			req: &pb.ReadTimeSeriesRowsReq{
				SpaceId: "space", DatasetId: "dataset", Keys: []*pb.TimeSeriesKey{key},
				Selectors: []*pb.TimeSeriesSelector{selector}, ColumnNames: []string{"close"},
			},
		},
		{
			name: "key outside top-level scope",
			req: &pb.ReadTimeSeriesRowsReq{
				SpaceId: "other", DatasetId: "dataset", Keys: []*pb.TimeSeriesKey{key},
				ColumnNames: []string{"close"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.req.AuthInfo = &pb.AuthInfo{AppId: "caller", AppKey: "key"}
			rsp, readErr := svc.ReadTimeSeriesRows(context.Background(), tc.req)
			if readErr != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM {
				t.Fatalf("rsp=%v err=%v", rsp, readErr)
			}
		})
	}
}

func TestPrimaryExactRecordReadOmitsMissingRows(t *testing.T) {
	node := &recordingNode{
		write: func(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
			return &pb.UpsertFieldsRsp{}, nil
		},
		read: func(_ context.Context, req *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
			rows := make([]*pb.RowFieldValues, 0, len(req.GetKeys()))
			for _, key := range req.GetKeys() {
				rows = append(rows, &pb.RowFieldValues{Key: key})
			}
			rows[1].Fields = []*pb.FieldValue{{
				FieldId: "value",
				Value:   &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "present"}},
			}}
			return &pb.ReadFieldsRsp{
				RetInfo:      successRetInfo(),
				Rows:         rows,
				ExistingKeys: []*pb.RowKey{req.GetKeys()[1]},
			}, nil
		},
	}
	svc, err := New(Options{Node: node})
	if err != nil {
		t.Fatal(err)
	}
	keys := []*pb.RecordKey{
		{SpaceId: "space", DatasetId: "dataset", RecordId: "missing", Version: "1"},
		{SpaceId: "space", DatasetId: "dataset", RecordId: "present", Version: "1"},
	}
	rsp, err := svc.ReadRecordRows(context.Background(), &pb.ReadRecordRowsReq{
		AuthInfo:    &pb.AuthInfo{AppId: "caller", AppKey: "key"},
		Keys:        keys,
		ColumnNames: []string{"value"},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("read rsp=%v err=%v", rsp, err)
	}
	if len(rsp.GetRows()) != 1 || rsp.GetRows()[0].GetKey().GetRecordId() != keys[1].GetRecordId() {
		t.Fatalf("rows=%v want only existing key %v", rsp.GetRows(), keys[1])
	}
}

func TestPrimaryRejectsWritesFromSCFMarketCanaryCredential(t *testing.T) {
	svc, err := New(Options{
		Node: &recordingNode{
			write: func(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
				t.Fatal("read-only SCF credential reached DataNode")
				return nil, nil
			},
			read: func(context.Context, *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
				return &pb.ReadFieldsRsp{}, nil
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rsp, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "scf-market-canary", AppKey: "valid"},
		Rows: []*pb.RowFieldUpsert{{
			Key: &pb.RowKey{SpaceId: "space", DatasetId: "dataset", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "record", Version: "1"}}},
		}},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
		t.Fatalf("rsp=%v err=%v", rsp, err)
	}
}

func TestPrimaryRecordDatasetPeriodFailuresRejectsReadOnlyCredentials(t *testing.T) {
	svc, err := New(Options{Node: &recordingNode{
		write: func(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
			return &pb.UpsertFieldsRsp{}, nil
		},
		read: func(context.Context, *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) { return &pb.ReadFieldsRsp{}, nil },
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, appID := range []string{"scf-market-canary"} {
		t.Run(appID, func(t *testing.T) {
			rsp, err := svc.RecordDatasetPeriodFailures(context.Background(), &pb.PrimaryRecordDatasetPeriodFailuresReq{
				AuthInfo:      &pb.AuthInfo{AppId: appID, AppKey: "valid"},
				Expectation:   &pb.DatasetPeriodExpectation{SpaceId: "space", DatasetId: "dataset", Frequency: "1m", PeriodTime: time.Now().Unix(), SeriesHash: "hash", ExpectedCount: 1},
				SeriesIndexes: []uint32{0},
			})
			if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
				t.Fatalf("rsp=%v err=%v", rsp, err)
			}
		})
	}
}

type periodAuthorizationNode struct {
	ensure int
	status int
	commit int
	record int
}

func (*periodAuthorizationNode) UpsertFields(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
	return nil, errors.New("unexpected ordinary upsert")
}

func (*periodAuthorizationNode) ReadFields(context.Context, *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
	return nil, errors.New("unexpected ordinary read")
}

func (n *periodAuthorizationNode) EnsureDatasetPeriod(context.Context, *pb.EnsureDatasetPeriodReq) (*pb.EnsureDatasetPeriodRsp, error) {
	n.ensure++
	return &pb.EnsureDatasetPeriodRsp{RetInfo: successRetInfo(), Status: "waiting"}, nil
}

func (n *periodAuthorizationNode) GetDatasetPeriodStatus(context.Context, *pb.GetDatasetPeriodStatusReq) (*pb.GetDatasetPeriodStatusRsp, error) {
	n.status++
	return &pb.GetDatasetPeriodStatusRsp{RetInfo: successRetInfo(), Status: "waiting"}, nil
}

func (n *periodAuthorizationNode) CommitTimeSeriesBatch(context.Context, *pb.CommitTimeSeriesBatchReq) (*pb.CommitTimeSeriesBatchRsp, error) {
	n.commit++
	return &pb.CommitTimeSeriesBatchRsp{RetInfo: successRetInfo()}, nil
}

func (n *periodAuthorizationNode) RecordDatasetPeriodFailures(context.Context, *pb.RecordDatasetPeriodFailuresReq) (*pb.RecordDatasetPeriodFailuresRsp, error) {
	n.record++
	return &pb.RecordDatasetPeriodFailuresRsp{RetInfo: successRetInfo()}, nil
}

type periodAuthorizationSnapshot struct {
	datasets map[routeKey]*pb.Dataset
}

func collectorPeriodMetadataSnapshot(spaceID, datasetID, frequency string) metadata.RequestSnapshot {
	return periodAuthorizationSnapshot{datasets: map[routeKey]*pb.Dataset{
		{spaceID: spaceID, datasetID: datasetID}: {
			SpaceId: spaceID, DatasetId: datasetID, DataKind: pb.DataKind_DATA_KIND_TIME_SERIES,
			Freq: frequency, Attributes: map[string]string{"owner_module": "collector", "dataset_role": "raw_collection"},
		},
	}}
}

func (s periodAuthorizationSnapshot) GetDataset(spaceID, datasetID string) (*pb.Dataset, bool) {
	dataset, ok := s.datasets[routeKey{spaceID: spaceID, datasetID: datasetID}]
	return dataset, ok
}

func (periodAuthorizationSnapshot) GetDataNode(string) (*pb.DataNode, bool) { return nil, false }

func (periodAuthorizationSnapshot) ListDatasetColumns(string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func TestEnsureDatasetPeriodWriteAuthorization(t *testing.T) {
	runPeriodAuthorizationCases(t, "ensure", func(svc *Service, auth *pb.AuthInfo, exp *pb.DatasetPeriodExpectation) *pb.RetInfo {
		rsp, err := svc.EnsureDatasetPeriod(context.Background(), &pb.PrimaryEnsureDatasetPeriodReq{AuthInfo: auth, Expectation: exp})
		if err != nil {
			t.Fatalf("EnsureDatasetPeriod error: %v", err)
		}
		return rsp.GetRetInfo()
	})
}

func TestCommitTimeSeriesBatchWriteAuthorization(t *testing.T) {
	runPeriodAuthorizationCases(t, "commit", func(svc *Service, auth *pb.AuthInfo, exp *pb.DatasetPeriodExpectation) *pb.RetInfo {
		row := &pb.RowFieldUpsert{Key: &pb.RowKey{
			SpaceId: exp.GetSpaceId(), DatasetId: exp.GetDatasetId(),
			Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: "BTC-USDT", Freq: exp.GetFrequency(), DataTime: "2026-09-30T00:00:00Z"}},
		}}
		rsp, err := svc.CommitTimeSeriesBatch(context.Background(), &pb.PrimaryCommitTimeSeriesBatchReq{
			AuthInfo: auth, Expectation: exp, Items: []*pb.TimeSeriesBatchRow{{SeriesIndex: 0, Row: row}}, WriteSource: "collector",
		})
		if err != nil {
			t.Fatalf("CommitTimeSeriesBatch error: %v", err)
		}
		return rsp.GetRetInfo()
	})
}

func TestRecordDatasetPeriodFailuresWriteAuthorization(t *testing.T) {
	runPeriodAuthorizationCases(t, "record", func(svc *Service, auth *pb.AuthInfo, exp *pb.DatasetPeriodExpectation) *pb.RetInfo {
		rsp, err := svc.RecordDatasetPeriodFailures(context.Background(), &pb.PrimaryRecordDatasetPeriodFailuresReq{AuthInfo: auth, Expectation: exp, SeriesIndexes: []uint32{0}})
		if err != nil {
			t.Fatalf("RecordDatasetPeriodFailures error: %v", err)
		}
		return rsp.GetRetInfo()
	})
}

func TestGetDatasetPeriodStatusAuthorization(t *testing.T) {
	runPeriodAuthorizationCases(t, "status", func(svc *Service, auth *pb.AuthInfo, exp *pb.DatasetPeriodExpectation) *pb.RetInfo {
		rsp, err := svc.GetDatasetPeriodStatus(context.Background(), &pb.PrimaryGetDatasetPeriodStatusReq{AuthInfo: auth, Expectation: exp})
		if err != nil {
			t.Fatalf("GetDatasetPeriodStatus error: %v", err)
		}
		return rsp.GetRetInfo()
	})
}

func runPeriodAuthorizationCases(t *testing.T, method string, invoke func(*Service, *pb.AuthInfo, *pb.DatasetPeriodExpectation) *pb.RetInfo) {
	t.Helper()
	validDataset := func() *pb.Dataset {
		return &pb.Dataset{
			SpaceId: "space", DatasetId: "dataset", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1h",
			Attributes: map[string]string{"owner_module": "collector", "dataset_role": "raw_collection"},
		}
	}
	tests := []struct {
		name            string
		appID           string
		dataset         *pb.Dataset
		includeDataset  bool
		includeSnapshot bool
		frequency       string
		authorizeErr    error
		wantCode        pb.ErrorCode
	}{
		{name: "collector owner", appID: "collector", dataset: validDataset(), includeDataset: true, includeSnapshot: true, frequency: "1h", wantCode: pb.ErrorCode_SUCCESS},
		{name: "SCF canary read-only caller", appID: "scf-market-canary", dataset: validDataset(), includeDataset: true, includeSnapshot: true, frequency: "1h", wantCode: pb.ErrorCode_NO_PERMISSION},
		{name: "Factor caller", appID: "moox-factor", dataset: validDataset(), includeDataset: true, includeSnapshot: true, frequency: "1h", wantCode: pb.ErrorCode_NO_PERMISSION},
		{name: "other module caller", appID: "admin", dataset: validDataset(), includeDataset: true, includeSnapshot: true, frequency: "1h", wantCode: pb.ErrorCode_NO_PERMISSION},
		{name: "Factor-owned Dataset", appID: "collector", dataset: &pb.Dataset{SpaceId: "space", DatasetId: "dataset", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1h", Attributes: map[string]string{"owner_module": "factor", "dataset_role": "factor_result"}}, includeDataset: true, includeSnapshot: true, frequency: "1h", wantCode: pb.ErrorCode_NO_PERMISSION},
		{name: "missing Dataset owner", appID: "collector", dataset: &pb.Dataset{SpaceId: "space", DatasetId: "dataset", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1h", Attributes: map[string]string{"dataset_role": "raw_collection"}}, includeDataset: true, includeSnapshot: true, frequency: "1h", wantCode: pb.ErrorCode_NO_PERMISSION},
		{name: "wrong Dataset role", appID: "collector", dataset: &pb.Dataset{SpaceId: "space", DatasetId: "dataset", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1h", Attributes: map[string]string{"owner_module": "collector", "dataset_role": "factor_result"}}, includeDataset: true, includeSnapshot: true, frequency: "1h", wantCode: pb.ErrorCode_NO_PERMISSION},
		{name: "snapshot Dataset identity mismatch", appID: "collector", dataset: &pb.Dataset{SpaceId: "other-space", DatasetId: "dataset", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1h", Attributes: map[string]string{"owner_module": "collector", "dataset_role": "raw_collection"}}, includeDataset: true, includeSnapshot: true, frequency: "1h", wantCode: pb.ErrorCode_NO_PERMISSION},
		{name: "record Dataset kind", appID: "collector", dataset: &pb.Dataset{SpaceId: "space", DatasetId: "dataset", DataKind: pb.DataKind_DATA_KIND_RECORD, Freq: "1h", Attributes: map[string]string{"owner_module": "collector", "dataset_role": "raw_collection"}}, includeDataset: true, includeSnapshot: true, frequency: "1h", wantCode: pb.ErrorCode_NO_PERMISSION},
		{name: "invalid declared frequency", appID: "collector", dataset: &pb.Dataset{SpaceId: "space", DatasetId: "dataset", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "forever", Attributes: map[string]string{"owner_module": "collector", "dataset_role": "raw_collection"}}, includeDataset: true, includeSnapshot: true, frequency: "forever", wantCode: pb.ErrorCode_NO_PERMISSION},
		{name: "frequency alias is not canonical", appID: "collector", dataset: validDataset(), includeDataset: true, includeSnapshot: true, frequency: "1H", wantCode: pb.ErrorCode_NO_PERMISSION},
		{name: "unknown Dataset", appID: "collector", dataset: validDataset(), includeDataset: false, includeSnapshot: true, frequency: "1h", wantCode: pb.ErrorCode_NO_PERMISSION},
		{name: "missing metadata snapshot", appID: "collector", dataset: validDataset(), includeDataset: true, includeSnapshot: false, frequency: "1h", wantCode: pb.ErrorCode_NO_PERMISSION},
		{name: "HMAC rejects before metadata", appID: "collector", dataset: validDataset(), includeDataset: true, includeSnapshot: true, frequency: "1h", authorizeErr: errors.New("invalid HMAC"), wantCode: pb.ErrorCode_NO_PERMISSION},
	}
	for _, tc := range tests {
		t.Run(method+"/"+tc.name, func(t *testing.T) {
			node := &periodAuthorizationNode{}
			var resolveCalls, signerCalls, authorizerCalls, snapshotCalls int
			svc, err := New(Options{
				Resolver: func(context.Context, string, string) (DataNodeClient, error) {
					resolveCalls++
					return node, nil
				},
				Snapshot: func() metadata.RequestSnapshot {
					snapshotCalls++
					if !tc.includeSnapshot {
						return nil
					}
					datasets := map[routeKey]*pb.Dataset{}
					if tc.includeDataset {
						datasets[routeKey{spaceID: "space", datasetID: "dataset"}] = tc.dataset
					}
					return periodAuthorizationSnapshot{datasets: datasets}
				},
				AuthSigner: func(*pb.AuthInfo) (*pb.AuthInfo, error) {
					signerCalls++
					return &pb.AuthInfo{AppId: "storage-primary", AppKey: "node-key"}, nil
				},
				Authorizer: func(auth *pb.AuthInfo) error {
					authorizerCalls++
					if auth == nil || auth.GetAppId() == "" || auth.GetAppKey() == "" {
						return errors.New("auth_info is invalid")
					}
					return tc.authorizeErr
				},
			})
			require.NoError(t, err)
			expectation := &pb.DatasetPeriodExpectation{SpaceId: "space", DatasetId: "dataset", Frequency: tc.frequency, PeriodTime: 123, SeriesHash: "hash", ExpectedCount: 1, DeadlineAt: time.Now().Add(time.Hour).Unix()}
			rsp := invoke(svc, &pb.AuthInfo{AppId: tc.appID, AppKey: "caller-key"}, expectation)
			require.Equal(t, tc.wantCode, rsp.GetCode(), "ret_info=%v", rsp)
			require.Equal(t, 1, authorizerCalls, "ordinary HMAC authorization must run first")
			wantSnapshotCalls := 1
			if tc.authorizeErr != nil {
				wantSnapshotCalls = 0
			} else if method != "status" && tc.appID == "scf-market-canary" {
				wantSnapshotCalls = 0
			}
			require.Equal(t, wantSnapshotCalls, snapshotCalls, "unexpected request metadata snapshot access")
			if method == "status" && tc.appID == "scf-market-canary" {
				require.NotContains(t, rsp.GetMsg(), "read-only primary credential", "status reads must rely on Collector identity, not the write-only read-only credential block")
			}
			if tc.wantCode == pb.ErrorCode_SUCCESS {
				require.Equal(t, 1, resolveCalls)
				require.Equal(t, 1, signerCalls)
				require.Equal(t, 1, node.ensure+node.status+node.commit+node.record)
			} else {
				require.Zero(t, resolveCalls, "unauthorized period RPC resolved a DataNode")
				require.Zero(t, signerCalls, "unauthorized period RPC signed a DataNode request")
				require.Zero(t, node.ensure+node.status+node.commit+node.record, "unauthorized period RPC reached the Store")
			}
		})
	}
}

func TestPrimaryAndDataNodeRecordPeriodFailuresThroughDeadlineMarker(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	const secret = "period-rpc-secret"
	node, err := datanode.NewService(datanode.Options{NodeID: "node-a", AuthSecret: secret, Pebble: pebble.Options{NodeID: "node-a", Path: root}})
	require.NoError(t, err)
	nodeClosed := false
	t.Cleanup(func() {
		if !nodeClosed {
			_ = node.Close()
		}
	})
	service, err := New(Options{
		Node:     primaryPeriodNodeIDAdapter{Service: node},
		Snapshot: func() metadata.RequestSnapshot { return collectorPeriodMetadataSnapshot("crypto", "bars", "1m") },
		AuthSigner: func(*pb.AuthInfo) (*pb.AuthInfo, error) {
			return &pb.AuthInfo{AppId: "primary", AppKey: datanode.ServiceAuthKey(secret, "primary")}, nil
		},
	})
	require.NoError(t, err)
	deadline := time.Now().UTC().Add(time.Minute).Unix()
	expectation := &pb.DatasetPeriodExpectation{
		SpaceId: "crypto", DatasetId: "bars", Frequency: "1m", PeriodTime: time.Now().UTC().Truncate(time.Minute).Unix(),
		SeriesHash: "period-rpc-hash", ExpectedCount: 1, DeadlineAt: deadline,
		SeriesSnapshot: []*pb.DatasetPeriodSeries{{SeriesIndex: 0, SubjectId: "ETH-USDT"}},
	}
	auth := &pb.AuthInfo{AppId: "collector", AppKey: "caller-key"}
	ensured, err := service.EnsureDatasetPeriod(context.Background(), &pb.PrimaryEnsureDatasetPeriodReq{AuthInfo: auth, Expectation: expectation})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, ensured.GetRetInfo().GetCode())
	require.Equal(t, "waiting", ensured.GetStatus())
	recorded, err := service.RecordDatasetPeriodFailures(context.Background(), &pb.PrimaryRecordDatasetPeriodFailuresReq{
		AuthInfo: auth, Expectation: expectation, SeriesIndexes: []uint32{0},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, recorded.GetRetInfo().GetCode())
	require.Equal(t, "waiting", recorded.GetPeriodStatus())

	require.NoError(t, node.Close())
	nodeClosed = true

	periodStore, err := pebble.Open(pebble.Options{Path: root, NodeID: "node-a"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = periodStore.Close() })
	_, err = periodStore.FinalizeWaitingDatasetPeriods(context.Background(), time.Unix(deadline+1, 0).UTC(), 10)
	require.NoError(t, err)
	periodExpectation := pebble.DatasetPeriodExpectation{
		SpaceID: expectation.GetSpaceId(), DatasetID: expectation.GetDatasetId(), Frequency: expectation.GetFrequency(),
		PeriodTime: expectation.GetPeriodTime(), SeriesHash: expectation.GetSeriesHash(), ExpectedCount: expectation.GetExpectedCount(),
		DeadlineAt: expectation.GetDeadlineAt(), SeriesSnapshot: []pebble.DatasetPeriodSeries{{SeriesIndex: 0, SubjectID: "ETH-USDT"}},
	}
	progress, err := periodStore.GetDatasetPeriodProgress(context.Background(), periodExpectation)
	require.NoError(t, err)
	require.Equal(t, "degraded", progress.Status)
	require.Equal(t, []uint32{0}, progress.FailedSeriesIndexes)
	entries, err := periodStore.ListOutbox(context.Background(), 0, 100)
	require.NoError(t, err)
	markers := 0
	for _, entry := range entries {
		message := &eventpb.EventMessage{}
		if err := proto.Unmarshal(entry.Data, message); err != nil || message.GetEventName() != events.CollectorPeriodCompleted.Name() {
			continue
		}
		marker := &storageeventpb.CollectorPeriodCompleted{}
		require.NoError(t, proto.Unmarshal(message.GetPayload(), marker))
		require.Equal(t, "degraded", marker.GetStatus())
		require.Equal(t, []string{"ETH-USDT"}, marker.GetFailedSubjects())
		require.Equal(t, []string{"ETH-USDT"}, marker.GetUniverseSubjectIds())
		markers++
	}
	require.Equal(t, 1, markers)
}

func TestPrimaryRoutesSameDatasetInDifferentSpacesSeparately(t *testing.T) {
	var resolved []string
	node := &recordingNode{
		write: func(_ context.Context, req *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
			keys := make([]*pb.RowKey, 0, len(req.GetRows()))
			for _, row := range req.GetRows() {
				keys = append(keys, row.GetKey())
			}
			return &pb.UpsertFieldsRsp{RetInfo: successRetInfo(), Keys: keys}, nil
		},
		read: func(_ context.Context, req *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
			rows := make([]*pb.RowFieldValues, 0, len(req.GetKeys()))
			for _, key := range req.GetKeys() {
				rows = append(rows, &pb.RowFieldValues{Key: key})
			}
			return &pb.ReadFieldsRsp{RetInfo: successRetInfo(), Rows: rows}, nil
		},
	}
	svc, err := New(Options{
		Resolver: func(_ context.Context, spaceID, datasetID string) (DataNodeClient, error) {
			resolved = append(resolved, spaceID+"/"+datasetID)
			return node, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	row := func(space, record string) *pb.RowFieldUpsert {
		return &pb.RowFieldUpsert{
			Key:    &pb.RowKey{SpaceId: space, DatasetId: "shared", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: record, Version: "1"}}},
			Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: record}}}},
		}
	}
	rsp, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"},
		Rows:     []*pb.RowFieldUpsert{row("space-a", "a"), row("space-b", "b")},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("write rsp=%v err=%v", rsp, err)
	}
	if !reflect.DeepEqual(resolved, []string{"space-a/shared", "space-b/shared"}) {
		t.Fatalf("resolved=%v", resolved)
	}
}

type recordingDatasetMetrics struct {
	observations []report.DatasetObservation
}

func (m *recordingDatasetMetrics) ObserveRun(observation report.DatasetObservation) error {
	m.observations = append(m.observations, observation)
	return nil
}

func TestPrimaryObservesCommittedTimeSeriesWatermarkByFrequency(t *testing.T) {
	node := &recordingNode{
		write: func(_ context.Context, req *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
			keys := make([]*pb.RowKey, 0, len(req.GetRows()))
			for _, row := range req.GetRows() {
				keys = append(keys, row.GetKey())
			}
			return &pb.UpsertFieldsRsp{RetInfo: successRetInfo(), Keys: keys}, nil
		},
	}
	metrics := &recordingDatasetMetrics{}
	svc, err := New(Options{Node: node, DatasetMetrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	row := func(freq string, at time.Time) *pb.RowFieldUpsert {
		return &pb.RowFieldUpsert{
			Key: &pb.RowKey{
				SpaceId: "crypto", DatasetId: "market_kline",
				Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
					SubjectId: "BTC-USDT", Freq: freq, DataTime: at.UTC().Format(time.RFC3339Nano),
				}},
			},
			Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{
				Value: &pb.TypedValue_DoubleValue{DoubleValue: 1},
			}}},
		}
	}
	t1003 := time.Date(2026, 7, 28, 10, 3, 0, 0, time.UTC)
	t1005 := t1003.Add(2 * time.Minute)
	rsp, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "test", AppKey: "test"},
		Rows:     []*pb.RowFieldUpsert{row("1m", t1003), row("1m", t1005), row("5m", t1003)},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("write rsp=%v err=%v", rsp, err)
	}
	if len(metrics.observations) != 2 {
		t.Fatalf("observations=%v", metrics.observations)
	}
	got := map[string]report.DatasetObservation{}
	for _, observation := range metrics.observations {
		got[observation.Key.Freq] = observation
	}
	if got["1m"].Rows != 2 || !got["1m"].OutputWatermark.Equal(t1005) || got["1m"].Result != "success" {
		t.Fatalf("1m observation=%+v", got["1m"])
	}
	if got["5m"].Rows != 1 || !got["5m"].OutputWatermark.Equal(t1003) || got["5m"].Result != "success" {
		t.Fatalf("5m observation=%+v", got["5m"])
	}
}

func TestPrimaryWriteFailureReportsErrorWithoutWatermark(t *testing.T) {
	node := &recordingNode{write: func(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
		return nil, errors.New("write failed")
	}}
	metrics := &recordingDatasetMetrics{}
	svc, err := New(Options{Node: node, DatasetMetrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 7, 28, 10, 5, 0, 0, time.UTC)
	rsp, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "test", AppKey: "test"},
		Rows: []*pb.RowFieldUpsert{{
			Key: &pb.RowKey{
				SpaceId: "crypto", DatasetId: "market_kline",
				Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
					SubjectId: "BTC-USDT", Freq: "1m", DataTime: at.Format(time.RFC3339Nano),
				}},
			},
			Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{
				Value: &pb.TypedValue_DoubleValue{DoubleValue: 1},
			}}},
		}}})
	if err != nil || rsp.GetRetInfo().GetCode() == pb.ErrorCode_SUCCESS {
		t.Fatalf("write rsp=%v err=%v", rsp, err)
	}
	if len(metrics.observations) != 1 {
		t.Fatalf("observations=%v", metrics.observations)
	}
	observation := metrics.observations[0]
	if observation.Result != "error" || !observation.OutputWatermark.IsZero() {
		t.Fatalf("observation=%+v", observation)
	}
}

func TestPrimaryDoesNotObserveRecordDataset(t *testing.T) {
	node := &recordingNode{write: func(_ context.Context, req *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
		return &pb.UpsertFieldsRsp{RetInfo: successRetInfo(), Keys: []*pb.RowKey{req.GetRows()[0].GetKey()}}, nil
	}}
	metrics := &recordingDatasetMetrics{}
	svc, err := New(Options{Node: node, DatasetMetrics: metrics})
	if err != nil {
		t.Fatal(err)
	}
	rsp, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "test", AppKey: "test"},
		Rows: []*pb.RowFieldUpsert{{
			Key: &pb.RowKey{
				SpaceId: "crypto", DatasetId: "symbols",
				Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "BTC-USDT", Version: "1"}},
			},
			Fields: []*pb.FieldValue{{FieldId: "symbol", Value: &pb.TypedValue{
				Value: &pb.TypedValue_StringValue{StringValue: "BTC-USDT"},
			}}},
		}},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("write rsp=%v err=%v", rsp, err)
	}
	if len(metrics.observations) != 0 {
		t.Fatalf("record dataset observations=%v", metrics.observations)
	}
}

func TestPrimaryReadPreservesRequestOrderAcrossDatasets(t *testing.T) {
	node := &recordingNode{
		write: func(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
			return &pb.UpsertFieldsRsp{RetInfo: successRetInfo()}, nil
		},
		read: func(_ context.Context, req *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
			rows := make([]*pb.RowFieldValues, 0, len(req.GetKeys()))
			for _, key := range req.GetKeys() {
				rows = append(rows, &pb.RowFieldValues{Key: key})
			}
			return &pb.ReadFieldsRsp{RetInfo: successRetInfo(), Rows: rows}, nil
		},
	}
	svc, err := New(Options{Node: node})
	if err != nil {
		t.Fatal(err)
	}
	key := func(dataset, record string) *pb.RowKey {
		return &pb.RowKey{SpaceId: "s", DatasetId: dataset, Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: record, Version: "1"}}}
	}
	keys := []*pb.RowKey{key("b", "first"), key("a", "second"), key("b", "third")}
	rsp, err := svc.ReadFields(context.Background(), &pb.PrimaryReadFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"},
		Keys:     keys,
		FieldIds: []string{"value"},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("read rsp=%v err=%v", rsp, err)
	}
	for i, row := range rsp.GetRows() {
		if row.GetKey().GetRecord().GetRecordId() != keys[i].GetRecord().GetRecordId() {
			t.Fatalf("row %d=%v want=%v", i, row.GetKey(), keys[i])
		}
	}
}

func TestPrimaryReadFieldsUsesOneSnapshotAcrossDatasetGroups(t *testing.T) {
	provider := &mutableSnapshotProvider{current: &testRequestSnapshot{generation: "generation-a"}}
	var generations []string
	node := &recordingNode{
		write: func(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
			return &pb.UpsertFieldsRsp{RetInfo: successRetInfo()}, nil
		},
		read: func(ctx context.Context, req *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
			snapshot, ok := metadata.RequestSnapshotFromContext(ctx).(*testRequestSnapshot)
			if !ok || snapshot == nil {
				t.Fatalf("request snapshot missing for %s", req.GetDatasetId())
			}
			generations = append(generations, snapshot.generation)
			return &pb.ReadFieldsRsp{RetInfo: successRetInfo(), Rows: []*pb.RowFieldValues{{Key: req.GetKeys()[0]}}}, nil
		},
	}
	svc, err := New(Options{
		Node:     node,
		Snapshot: provider.RequestSnapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	key := func(dataset, record string) *pb.RowKey {
		return &pb.RowKey{SpaceId: "space", DatasetId: dataset, Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: record, Version: "1"}}}
	}
	rsp, err := svc.ReadFields(context.Background(), &pb.PrimaryReadFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"},
		Keys:     []*pb.RowKey{key("dataset-a", "a"), key("dataset-b", "b")},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("read rsp=%v err=%v", rsp, err)
	}
	if provider.calls != 1 {
		t.Fatalf("snapshot provider calls=%d want 1", provider.calls)
	}
	if !reflect.DeepEqual(generations, []string{"generation-a", "generation-a"}) {
		t.Fatalf("generations=%v", generations)
	}
}

func TestPrimaryRejectsRecordWriteWithoutVersion(t *testing.T) {
	node := &recordingNode{
		write: func(context.Context, *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
			t.Fatal("write should not reach DataNode")
			return nil, nil
		},
		read: func(context.Context, *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
			return nil, nil
		},
	}
	svc, err := New(Options{Node: node})
	if err != nil {
		t.Fatal(err)
	}
	rsp, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"},
		Rows: []*pb.RowFieldUpsert{{
			Key:    &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r"}}},
			Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "x"}}}},
		}},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("rsp=%v err=%v", rsp, err)
	}
}

func TestPrimaryReadFieldsReturnsResolvedLatestRecordVersion(t *testing.T) {
	node, err := datanode.NewService(datanode.Options{
		NodeID: "node-a", AuthSecret: "node-secret",
		Pebble: pebble.Options{NodeID: "node-a", Path: filepath.Join(t.TempDir(), "node")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	svc, err := New(Options{
		Node: node,
		AuthSigner: func(*pb.AuthInfo) (*pb.AuthInfo, error) {
			return &pb.AuthInfo{AppId: "primary", AppKey: datanode.ServiceAuthKey("node-secret", "primary")}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"1", "2"} {
		key := &pb.RowKey{SpaceId: "space", DatasetId: "records", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: version}}}
		rsp, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{
			AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"},
			Rows: []*pb.RowFieldUpsert{{
				Key: key,
				Fields: []*pb.FieldValue{{
					FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: version}},
				}},
			}},
		})
		if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			t.Fatalf("write version=%s rsp=%v err=%v", version, rsp, err)
		}
	}
	rsp, err := svc.ReadFields(context.Background(), &pb.PrimaryReadFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"},
		Keys: []*pb.RowKey{{
			SpaceId: "space", DatasetId: "records",
			Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r"}},
		}},
		FieldIds: []string{"value"},
	})
	if err != nil || len(rsp.GetRows()) != 1 || rsp.GetRows()[0].GetKey().GetRecord().GetVersion() != "2" {
		t.Fatalf("rsp=%v err=%v", rsp, err)
	}
}

func TestPrimaryExactTimeSeriesReadCrossesDataNodePebble(t *testing.T) {
	node, err := datanode.NewService(datanode.Options{
		NodeID: "node-a", AuthSecret: "node-secret",
		Pebble: pebble.Options{NodeID: "node-a", Path: filepath.Join(t.TempDir(), "node")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	svc, err := New(Options{
		Node: node,
		AuthSigner: func(*pb.AuthInfo) (*pb.AuthInfo, error) {
			return &pb.AuthInfo{AppId: "primary", AppKey: datanode.ServiceAuthKey("node-secret", "primary")}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	key := &pb.TimeSeriesKey{
		SpaceId: "crypto", DatasetId: "dataset_spot_kline_1h", SubjectId: "BTC-USDT", Freq: "1h",
		DataTime: "2026-07-29T15:00:00Z", SeriesTag: "venue:binance",
	}
	rowKey := timeSeriesRowKey(key)
	writeRsp, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"},
		Rows: []*pb.RowFieldUpsert{{
			Key: rowKey,
			Fields: []*pb.FieldValue{{
				FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 101.25}},
			}},
		}},
	})
	if err != nil || writeRsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("write rsp=%v err=%v", writeRsp, err)
	}
	readRsp, err := svc.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{
		AuthInfo: &pb.AuthInfo{AppId: "caller", AppKey: "key"},
		SpaceId:  "crypto", DatasetId: "dataset_spot_kline_1h",
		Keys: []*pb.TimeSeriesKey{key}, ColumnNames: []string{"close"},
	})
	if err != nil || readRsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS ||
		len(readRsp.GetRows()) != 1 || readRsp.GetRows()[0].GetKey().GetSeriesTag() != "venue:binance" ||
		len(readRsp.GetRows()[0].GetFields()) != 1 ||
		readRsp.GetRows()[0].GetFields()[0].GetValue().GetDoubleValue() != 101.25 {
		t.Fatalf("read rsp=%v err=%v", readRsp, err)
	}
}

func successRetInfo() *pb.RetInfo {
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}
}

type mutableSnapshotProvider struct {
	current metadata.RequestSnapshot
	calls   int
}

func (p *mutableSnapshotProvider) RequestSnapshot() metadata.RequestSnapshot {
	p.calls++
	snapshot := p.current
	p.current = &testRequestSnapshot{generation: "generation-b"}
	return snapshot
}

type testRequestSnapshot struct {
	generation string
}

func (*testRequestSnapshot) GetDataset(string, string) (*pb.Dataset, bool) {
	return nil, false
}

func (*testRequestSnapshot) GetDataNode(string) (*pb.DataNode, bool) {
	return nil, false
}

func (*testRequestSnapshot) ListDatasetColumns(string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}
