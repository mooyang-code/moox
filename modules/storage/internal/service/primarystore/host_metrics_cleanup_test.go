package primarystore

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCleanupExpiredHostMetricsUsesStrictCutoffAndBoundedBatches(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	var requests []*pb.DeleteTimeSeriesRowsReq
	svc := &Service{cleanupDeleteRows: func(_ context.Context, req *pb.DeleteTimeSeriesRowsReq) (*pb.DeleteTimeSeriesRowsRsp, error) {
		requests = append(requests, req)
		return cleanupSuccess(1000), nil
	}}

	result, err := svc.CleanupExpiredHostMetrics(context.Background(), HostMetricsCleanupOptions{
		SpaceID: "moox_system", DatasetIDs: []string{"host_resource_v1"}, MaxAge: 48 * time.Hour,
		BatchSize: 1000, MaxBatchesPerRun: 10, Now: now,
	})
	require.NoError(t, err)
	assert.Equal(t, HostMetricsCleanupResult{Deleted: 10000, Batches: 10}, result)
	require.Len(t, requests, 10)
	wantCutoff := now.UTC().Add(-48 * time.Hour).Add(-time.Nanosecond).Format(time.RFC3339Nano)
	for _, req := range requests {
		assert.Equal(t, wantCutoff, req.GetTimeRange().GetEndTime())
		assert.Equal(t, uint32(1), req.GetPage().GetPage())
		assert.Equal(t, uint32(1000), req.GetPage().GetSize())
	}
}

func TestCleanupExpiredHostMetricsStopsDatasetAfterEmptyBatch(t *testing.T) {
	var calls int
	svc := &Service{cleanupDeleteRows: func(context.Context, *pb.DeleteTimeSeriesRowsReq) (*pb.DeleteTimeSeriesRowsRsp, error) {
		calls++
		if calls == 2 {
			return cleanupSuccess(0), nil
		}
		return cleanupSuccess(4), nil
	}}
	result, err := svc.CleanupExpiredHostMetrics(context.Background(), validCleanupOptions("host_resource_v1"))
	require.NoError(t, err)
	assert.Equal(t, HostMetricsCleanupResult{Deleted: 4, Batches: 2}, result)
}

func TestCleanupExpiredHostMetricsContinuesAfterDatasetFailure(t *testing.T) {
	var datasets []string
	svc := &Service{cleanupDeleteRows: func(_ context.Context, req *pb.DeleteTimeSeriesRowsReq) (*pb.DeleteTimeSeriesRowsRsp, error) {
		datasets = append(datasets, req.GetDatasetId())
		if req.GetDatasetId() == "host_resource_v1" {
			return nil, errors.New("temporary delete failure")
		}
		return cleanupSuccess(3), nil
	}}
	opts := validCleanupOptions("host_resource_v1", "host_fs_v1")
	opts.MaxBatchesPerRun = 1
	result, err := svc.CleanupExpiredHostMetrics(context.Background(), opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "host_resource_v1")
	assert.Equal(t, []string{"host_resource_v1", "host_fs_v1"}, datasets)
	assert.Equal(t, HostMetricsCleanupResult{Deleted: 3, Batches: 1}, result)
}

func TestCleanupExpiredHostMetricsTreatsErrorResponseAsDatasetFailure(t *testing.T) {
	svc := &Service{cleanupDeleteRows: func(_ context.Context, req *pb.DeleteTimeSeriesRowsReq) (*pb.DeleteTimeSeriesRowsRsp, error) {
		return &pb.DeleteTimeSeriesRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_INNER_ERR, Msg: "broken"}}, nil
	}}
	_, err := svc.CleanupExpiredHostMetrics(context.Background(), validCleanupOptions("host_net_v1"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "host_net_v1")
}

func TestCleanupExpiredHostMetricsStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls int
	svc := &Service{cleanupDeleteRows: func(context.Context, *pb.DeleteTimeSeriesRowsReq) (*pb.DeleteTimeSeriesRowsRsp, error) {
		calls++
		cancel()
		return cleanupSuccess(1), nil
	}}
	result, err := svc.CleanupExpiredHostMetrics(ctx, validCleanupOptions("host_resource_v1", "host_fs_v1"))
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, calls)
	assert.Equal(t, HostMetricsCleanupResult{Deleted: 1, Batches: 1}, result)
}

func TestCleanupExpiredHostMetricsValidatesOptions(t *testing.T) {
	tests := []HostMetricsCleanupOptions{
		{},
		{SpaceID: "moox_system", DatasetIDs: []string{"host_resource_v1"}, MaxAge: 0, BatchSize: 1000, MaxBatchesPerRun: 10},
		{SpaceID: "moox_system", DatasetIDs: []string{"host_resource_v1"}, MaxAge: time.Hour, BatchSize: 1001, MaxBatchesPerRun: 10},
	}
	for i, opts := range tests {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			_, err := (&Service{}).CleanupExpiredHostMetrics(context.Background(), opts)
			require.Error(t, err)
		})
	}
}

func validCleanupOptions(datasets ...string) HostMetricsCleanupOptions {
	return HostMetricsCleanupOptions{
		SpaceID: "moox_system", DatasetIDs: datasets, MaxAge: 48 * time.Hour,
		BatchSize: 1000, MaxBatchesPerRun: 10, Now: time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC),
	}
}

func cleanupSuccess(deleted uint32) *pb.DeleteTimeSeriesRowsRsp {
	return &pb.DeleteTimeSeriesRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Deleted: deleted}
}
