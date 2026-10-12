package marketstorage

import (
	"context"
	"testing"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

func TestCommitTimeSeriesBatchRequiresExactAcceptedIndexSet(t *testing.T) {
	expectation := validStorageExpectation(time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC))
	rows := []*storagepb.TimeSeriesBatchRow{{SeriesIndex: 2}, {SeriesIndex: 1}, {SeriesIndex: 2}}
	t.Run("deduped exact indexes", func(t *testing.T) {
		writer := &storageWriter{period: &periodAccessStub{commit: &storagepb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: storageSuccess(), AcceptedSeriesIndexes: []uint32{1, 2}}}}
		require.NoError(t, writer.CommitTimeSeriesBatch(context.Background(), expectation, rows, "event"))
	})
	for _, accepted := range [][]uint32{{1}, {1, 2, 3}, {1, 2, 2}} {
		t.Run("invalid accepted set", func(t *testing.T) {
			writer := &storageWriter{period: &periodAccessStub{commit: &storagepb.PrimaryCommitTimeSeriesBatchRsp{RetInfo: storageSuccess(), AcceptedSeriesIndexes: accepted}}}
			require.Error(t, writer.CommitTimeSeriesBatch(context.Background(), expectation, rows, "event"))
		})
	}
}

func validStorageExpectation(period time.Time) *storagepb.DatasetPeriodExpectation {
	return &storagepb.DatasetPeriodExpectation{
		SpaceId: "crypto", DatasetId: "bars", Frequency: "1m", PeriodTime: period.Unix(),
		SeriesHash: "abc", ExpectedCount: 2, DeadlineAt: period.Add(time.Minute).Unix(),
	}
}

func storageSuccess() *storagepb.RetInfo {
	return &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}
}

type periodAccessStub struct {
	ensure      *storagepb.PrimaryEnsureDatasetPeriodRsp
	ensureErr   error
	status      *storagepb.PrimaryGetDatasetPeriodStatusRsp
	commit      *storagepb.PrimaryCommitTimeSeriesBatchRsp
	record      *storagepb.PrimaryRecordDatasetPeriodFailuresRsp
	recordReq   *storagepb.PrimaryRecordDatasetPeriodFailuresReq
	ensureCalls int
	statusCalls int
}

func (s *periodAccessStub) EnsureDatasetPeriod(context.Context, *storagepb.PrimaryEnsureDatasetPeriodReq, ...client.Option) (*storagepb.PrimaryEnsureDatasetPeriodRsp, error) {
	s.ensureCalls++
	return s.ensure, s.ensureErr
}

func (s *periodAccessStub) GetDatasetPeriodStatus(context.Context, *storagepb.PrimaryGetDatasetPeriodStatusReq, ...client.Option) (*storagepb.PrimaryGetDatasetPeriodStatusRsp, error) {
	s.statusCalls++
	return s.status, nil
}

func (s *periodAccessStub) CommitTimeSeriesBatch(context.Context, *storagepb.PrimaryCommitTimeSeriesBatchReq, ...client.Option) (*storagepb.PrimaryCommitTimeSeriesBatchRsp, error) {
	return s.commit, nil
}

func (s *periodAccessStub) RecordDatasetPeriodFailures(_ context.Context, req *storagepb.PrimaryRecordDatasetPeriodFailuresReq, _ ...client.Option) (*storagepb.PrimaryRecordDatasetPeriodFailuresRsp, error) {
	s.recordReq = req
	return s.record, nil
}
