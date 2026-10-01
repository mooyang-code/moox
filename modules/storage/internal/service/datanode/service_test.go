package datanode

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestDataNodePeriodReceiptsDeadlineAndReadonlyStatus(t *testing.T) {
	ctx := context.Background()
	deadline := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	var now atomic.Int64
	now.Store(deadline.Add(-time.Nanosecond).UnixNano())
	node, err := NewService(Options{NodeID: "node-a", AuthSecret: "period-secret", Pebble: pebble.Options{
		NodeID: "node-a", Path: filepath.Join(t.TempDir(), "node"), PeriodNow: func() time.Time { return time.Unix(0, now.Load()).UTC() },
	}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, node.Close()) })
	auth := &pb.AuthInfo{AppId: "primary", AppKey: ServiceAuthKey("period-secret", "primary")}
	exp := &pb.DatasetPeriodExpectation{SpaceId: "crypto", DatasetId: "bars", Frequency: "1m", PeriodTime: deadline.Add(-time.Minute).Unix(), SeriesHash: "hash", ExpectedCount: 2, DeadlineAt: deadline.Unix(), SeriesSnapshot: []*pb.DatasetPeriodSeries{
		{SeriesIndex: 0, SubjectId: "BTC-USDT", SeriesTag: "okx"}, {SeriesIndex: 1, SubjectId: "BTC-USDT", SeriesTag: "binance"},
	}}
	query := proto.Clone(exp).(*pb.DatasetPeriodExpectation)
	query.SeriesSnapshot = nil
	status, err := node.GetDatasetPeriodStatus(ctx, &pb.GetDatasetPeriodStatusReq{NodeId: "node-a", AuthInfo: auth, Expectation: query})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NOT_FOUND, status.GetRetInfo().GetCode())
	require.Empty(t, status.GetStatus())
	ensured, err := node.EnsureDatasetPeriod(ctx, &pb.EnsureDatasetPeriodReq{NodeId: "node-a", AuthInfo: auth, Expectation: exp})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, ensured.GetRetInfo().GetCode())
	require.Equal(t, deadline.Unix(), ensured.GetDeadlineAt())
	retry := proto.Clone(exp).(*pb.DatasetPeriodExpectation)
	retry.DeadlineAt += 3600
	ensured, err = node.EnsureDatasetPeriod(ctx, &pb.EnsureDatasetPeriodReq{NodeId: "node-a", AuthInfo: auth, Expectation: retry})
	require.NoError(t, err)
	require.Equal(t, deadline.Unix(), ensured.GetDeadlineAt())
	assertStatus := func(expectation *pb.DatasetPeriodExpectation, want string) {
		t.Helper()
		response, err := node.GetDatasetPeriodStatus(ctx, &pb.GetDatasetPeriodStatusReq{NodeId: "node-a", AuthInfo: auth, Expectation: expectation})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_SUCCESS, response.GetRetInfo().GetCode())
		require.Equal(t, want, response.GetStatus())
		require.Equal(t, exp.GetSeriesHash(), response.GetSeriesHash())
		require.Equal(t, exp.GetExpectedCount(), response.GetExpectedCount())
		require.Equal(t, deadline.Unix(), response.GetDeadlineAt())
	}
	assertStatus(query, "waiting")
	for _, change := range []func(*pb.DatasetPeriodExpectation){func(x *pb.DatasetPeriodExpectation) { x.SeriesHash = "wrong" }, func(x *pb.DatasetPeriodExpectation) { x.ExpectedCount++ }} {
		bad := proto.Clone(query).(*pb.DatasetPeriodExpectation)
		change(bad)
		status, err = node.GetDatasetPeriodStatus(ctx, &pb.GetDatasetPeriodStatusReq{NodeId: "node-a", AuthInfo: auth, Expectation: bad})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_CONFLICT, status.GetRetInfo().GetCode())
	}
	failed, err := node.RecordDatasetPeriodFailures(ctx, &pb.RecordDatasetPeriodFailuresReq{NodeId: "node-a", AuthInfo: auth, Expectation: query, SeriesIndexes: []uint32{1, 0, 1}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, failed.GetRetInfo().GetCode())
	require.Len(t, failed.GetResults(), 2)
	for index, result := range failed.GetResults() {
		require.Equal(t, uint32(index), result.GetSeriesIndex())
		require.Equal(t, pb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_RECORDED, result.GetDisposition())
	}
	items := []*pb.TimeSeriesBatchRow{}
	for index, tag := range []string{"okx", "binance"} {
		items = append(items, &pb.TimeSeriesBatchRow{SeriesIndex: uint32(index), Row: dataNodePeriodRow(exp, "BTC-USDT", tag)})
	}
	committed, err := node.CommitTimeSeriesBatch(ctx, &pb.CommitTimeSeriesBatchReq{NodeId: "node-a", AuthInfo: auth, Expectation: query, Items: items, WriteSource: "collector"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, committed.GetRetInfo().GetCode())
	require.Equal(t, []uint32{0, 1}, committed.GetAcceptedSeriesIndexes())
	assertStatus(query, "complete")
	failed, err = node.RecordDatasetPeriodFailures(ctx, &pb.RecordDatasetPeriodFailuresReq{NodeId: "node-a", AuthInfo: auth, Expectation: query, SeriesIndexes: []uint32{1, 0}})
	require.NoError(t, err)
	for _, result := range failed.GetResults() {
		require.Equal(t, pb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_ALREADY_SUCCEEDED, result.GetDisposition())
	}
	degraded := proto.Clone(exp).(*pb.DatasetPeriodExpectation)
	degraded.PeriodTime -= 60
	_, err = node.EnsureDatasetPeriod(ctx, &pb.EnsureDatasetPeriodReq{NodeId: "node-a", AuthInfo: auth, Expectation: degraded})
	require.NoError(t, err)
	now.Store(deadline.UnixNano())
	assertStatus(degraded, "waiting")
	committed, err = node.CommitTimeSeriesBatch(ctx, &pb.CommitTimeSeriesBatchReq{NodeId: "node-a", AuthInfo: auth, Expectation: degraded, Items: []*pb.TimeSeriesBatchRow{{SeriesIndex: 0, Row: dataNodePeriodRow(degraded, "BTC-USDT", "okx")}}, WriteSource: "collector"})
	require.NoError(t, err)
	require.Equal(t, "degraded", committed.GetPeriodStatus())
	require.Empty(t, committed.GetAcceptedSeriesIndexes(), "request keys cannot stand in for persisted accepted indexes")
	failed, err = node.RecordDatasetPeriodFailures(ctx, &pb.RecordDatasetPeriodFailuresReq{NodeId: "node-a", AuthInfo: auth, Expectation: degraded, SeriesIndexes: []uint32{0, 1}})
	require.NoError(t, err)
	for _, result := range failed.GetResults() {
		require.Equal(t, pb.PeriodFailureDisposition_PERIOD_FAILURE_DISPOSITION_MISSED_DEADLINE, result.GetDisposition())
	}
	assertStatus(degraded, "degraded")
}

func TestDataNodePeriodRPCsRequireMatchingNodeID(t *testing.T) {
	ctx := context.Background()
	node, err := NewService(Options{NodeID: "node-a", AuthSecret: "node-identity-secret", Pebble: pebble.Options{
		NodeID: "node-a", Path: filepath.Join(t.TempDir(), "node"),
	}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, node.Close()) })
	auth := &pb.AuthInfo{AppId: "primary", AppKey: ServiceAuthKey("node-identity-secret", "primary")}
	period := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	exp := &pb.DatasetPeriodExpectation{SpaceId: "crypto", DatasetId: "bars", Frequency: "1m", PeriodTime: period.Unix(), SeriesHash: "hash", ExpectedCount: 1, DeadlineAt: period.Add(time.Hour).Unix(), SeriesSnapshot: []*pb.DatasetPeriodSeries{{SeriesIndex: 0, SubjectId: "BTC-USDT", SeriesTag: "okx"}}}
	ensured, err := node.EnsureDatasetPeriod(ctx, &pb.EnsureDatasetPeriodReq{NodeId: "node-a", AuthInfo: auth, Expectation: exp})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, ensured.GetRetInfo().GetCode())
	query := proto.Clone(exp).(*pb.DatasetPeriodExpectation)
	query.SeriesSnapshot = nil
	row := dataNodePeriodRow(exp, "BTC-USDT", "okx")

	for _, nodeID := range []string{"", "node-b"} {
		t.Run(fmt.Sprintf("node_id_%q", nodeID), func(t *testing.T) {
			ensure, err := node.EnsureDatasetPeriod(ctx, &pb.EnsureDatasetPeriodReq{NodeId: nodeID, AuthInfo: auth, Expectation: exp})
			require.NoError(t, err)
			require.Equal(t, pb.ErrorCode_INVALID_PARAM, ensure.GetRetInfo().GetCode())
			status, err := node.GetDatasetPeriodStatus(ctx, &pb.GetDatasetPeriodStatusReq{NodeId: nodeID, AuthInfo: auth, Expectation: query})
			require.NoError(t, err)
			require.Equal(t, pb.ErrorCode_INVALID_PARAM, status.GetRetInfo().GetCode())
			commit, err := node.CommitTimeSeriesBatch(ctx, &pb.CommitTimeSeriesBatchReq{NodeId: nodeID, AuthInfo: auth, Expectation: query, Items: []*pb.TimeSeriesBatchRow{{SeriesIndex: 0, Row: row}}, WriteSource: "collector"})
			require.NoError(t, err)
			require.Equal(t, pb.ErrorCode_INVALID_PARAM, commit.GetRetInfo().GetCode())
			record, err := node.RecordDatasetPeriodFailures(ctx, &pb.RecordDatasetPeriodFailuresReq{NodeId: nodeID, AuthInfo: auth, Expectation: query, SeriesIndexes: []uint32{0}})
			require.NoError(t, err)
			require.Equal(t, pb.ErrorCode_INVALID_PARAM, record.GetRetInfo().GetCode())
		})
	}

	progress, err := node.Store().GetDatasetPeriodProgress(ctx, periodExpectation(exp))
	require.NoError(t, err)
	require.Equal(t, "waiting", progress.Status)
	require.Equal(t, []byte{0}, progress.Bitmap)
	require.Empty(t, progress.FailedSeriesIndexes)
	entries, err := node.Store().ListOutbox(ctx, 0, 100)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestDataNodeEnsureRejectsNonPositiveDeadlineWithoutSideEffects(t *testing.T) {
	for _, deadlineAt := range []int64{0, -1} {
		t.Run(fmt.Sprint(deadlineAt), func(t *testing.T) {
			ctx := context.Background()
			node, err := NewService(Options{NodeID: "node-a", AuthSecret: "deadline-secret", Pebble: pebble.Options{
				NodeID: "node-a", Path: filepath.Join(t.TempDir(), "node"),
			}})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, node.Close()) })
			auth := &pb.AuthInfo{AppId: "primary", AppKey: ServiceAuthKey("deadline-secret", "primary")}
			period := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
			exp := &pb.DatasetPeriodExpectation{SpaceId: "crypto", DatasetId: "bars", Frequency: "1m", PeriodTime: period.Unix(), SeriesHash: "hash", ExpectedCount: 1, DeadlineAt: deadlineAt, SeriesSnapshot: []*pb.DatasetPeriodSeries{{SeriesIndex: 0, SubjectId: "BTC-USDT", SeriesTag: "okx"}}}

			ensured, err := node.EnsureDatasetPeriod(ctx, &pb.EnsureDatasetPeriodReq{NodeId: "node-a", AuthInfo: auth, Expectation: exp})
			require.NoError(t, err)
			require.Equal(t, pb.ErrorCode_INVALID_PARAM, ensured.GetRetInfo().GetCode())
			query := proto.Clone(exp).(*pb.DatasetPeriodExpectation)
			query.SeriesSnapshot = nil
			query.DeadlineAt = 0
			status, err := node.GetDatasetPeriodStatus(ctx, &pb.GetDatasetPeriodStatusReq{NodeId: "node-a", AuthInfo: auth, Expectation: query})
			require.NoError(t, err)
			require.Equal(t, pb.ErrorCode_NOT_FOUND, status.GetRetInfo().GetCode())
			entries, err := node.Store().ListOutbox(ctx, 0, 100)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}

func dataNodePeriodRow(exp *pb.DatasetPeriodExpectation, subject, tag string) *pb.RowFieldUpsert {
	return &pb.RowFieldUpsert{Key: &pb.RowKey{SpaceId: exp.GetSpaceId(), DatasetId: exp.GetDatasetId(), Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: subject, SeriesTag: tag, Freq: exp.GetFrequency(), DataTime: time.Unix(exp.GetPeriodTime(), 0).UTC().Format(time.RFC3339Nano)}}}, Fields: []*pb.FieldValue{{FieldId: "close", Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}}}}}
}

func TestDataNodePeriodRejectsNilSnapshotSlots(t *testing.T) {
	for _, count := range []uint32{2, 3} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			ctx := context.Background()
			period := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
			node, err := NewService(Options{NodeID: "node-a", AuthSecret: "nil-snapshot-secret", Pebble: pebble.Options{NodeID: "node-a", Path: filepath.Join(t.TempDir(), "node"), PeriodNow: func() time.Time { return period }}})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, node.Close()) })
			auth := &pb.AuthInfo{AppId: "primary", AppKey: ServiceAuthKey("nil-snapshot-secret", "primary")}
			exp := &pb.DatasetPeriodExpectation{SpaceId: "crypto", DatasetId: "bars", Frequency: "1m", PeriodTime: period.Unix(), SeriesHash: "hash", ExpectedCount: count, DeadlineAt: period.Add(time.Hour).Unix(), SeriesSnapshot: []*pb.DatasetPeriodSeries{{SeriesIndex: 0, SubjectId: "BTC-USDT", SeriesTag: "okx"}, nil, {SeriesIndex: 1, SubjectId: "ETH-USDT", SeriesTag: "okx"}}}
			rsp, err := node.EnsureDatasetPeriod(ctx, &pb.EnsureDatasetPeriodReq{NodeId: "node-a", AuthInfo: auth, Expectation: exp})
			require.NoError(t, err)
			require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode(), "a nil slot cannot be silently dropped to repair the snapshot")
			exp.SeriesSnapshot = nil
			status, err := node.GetDatasetPeriodStatus(ctx, &pb.GetDatasetPeriodStatusReq{NodeId: "node-a", AuthInfo: auth, Expectation: exp})
			require.NoError(t, err)
			require.Equal(t, pb.ErrorCode_NOT_FOUND, status.GetRetInfo().GetCode())
			entries, err := node.Store().ListOutbox(ctx, 0, 100)
			require.NoError(t, err)
			require.Empty(t, entries, "invalid Ensure must have no period or marker side effects")
		})
	}
}

func TestNewServiceRequiresAuthSecret(t *testing.T) {
	_, err := NewService(Options{
		NodeID: "node-a",
		Pebble: pebble.Options{
			NodeID: "node-a",
			Path:   filepath.Join(t.TempDir(), "node"),
		},
	})
	if err == nil {
		t.Fatal("expected missing auth secret to be rejected")
	}
}

func TestErrorCodeUsesTypedValidationErrors(t *testing.T) {
	if got := errorCode(errors.New("required backend is unavailable")); got != pb.ErrorCode_INNER_ERR {
		t.Fatalf("plain error classified as %s", got)
	}
	_, validationErr := pebble.NormalizeRowKey(nil)
	if got := errorCode(validationErr); got != pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("validation error classified as %s", got)
	}
}

func TestGetNodeStateRequiresSignedIdentityAndReturnsConfiguredNode(t *testing.T) {
	node, err := NewService(Options{
		NodeID:     "node-a",
		AuthSecret: "secret",
		Pebble: pebble.Options{
			NodeID: "node-a",
			Path:   filepath.Join(t.TempDir(), "node"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()

	ctx := context.Background()
	wrongSignature, err := node.GetNodeState(ctx, &pb.GetNodeStateReq{
		NodeId:   "node-a",
		AuthInfo: &pb.AuthInfo{AppId: "storage-metadata", AppKey: "bad"},
	})
	if err != nil || wrongSignature.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
		t.Fatalf("wrong signature: rsp=%v err=%v", wrongSignature, err)
	}

	mismatchedNode, err := node.GetNodeState(ctx, &pb.GetNodeStateReq{
		NodeId:   "node-b",
		AuthInfo: &pb.AuthInfo{AppId: "storage-metadata", AppKey: ServiceAuthKey("secret", "storage-metadata")},
	})
	if err != nil || mismatchedNode.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("mismatched node: rsp=%v err=%v", mismatchedNode, err)
	}

	ready, err := node.GetNodeState(ctx, &pb.GetNodeStateReq{
		NodeId:   "node-a",
		AuthInfo: &pb.AuthInfo{AppId: "storage-metadata", AppKey: ServiceAuthKey("secret", "storage-metadata")},
	})
	if err != nil || ready.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("valid identity: rsp=%v err=%v", ready, err)
	}
	if ready.GetNodeId() != "node-a" || ready.GetStatus() != "READY" {
		t.Fatalf("node state = (%q, %q), want (node-a, READY)", ready.GetNodeId(), ready.GetStatus())
	}
}
