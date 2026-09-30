package marketstorage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

func TestEnsureDatasetPeriodRecoversLostAckWithOriginalStorageDeadline(t *testing.T) {
	period := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	expectation := validStorageExpectation(period)
	expectation.DeadlineAt = period.Add(10 * time.Minute).Unix()
	access := &periodAccessStub{
		ensureErr: errors.New("ack lost"),
		status: &storagepb.PrimaryGetDatasetPeriodStatusRsp{
			RetInfo: storageSuccess(), Status: domain.PeriodStatusWaiting, SeriesHash: expectation.SeriesHash,
			ExpectedCount: expectation.ExpectedCount, DeadlineAt: period.Add(time.Minute).Unix(),
		},
	}
	writer := &storageWriter{period: access}

	state, err := writer.EnsureDatasetPeriod(context.Background(), expectation)
	require.NoError(t, err)
	require.Equal(t, 1, access.ensureCalls, "a lost acknowledgement is recovered by a read-only status query before retrying Ensure")
	require.Equal(t, 1, access.statusCalls)
	require.Equal(t, period.Add(time.Minute), state.DeadlineAt, "the first Storage deadline remains authoritative")
	require.Equal(t, domain.PeriodStatusWaiting, state.Status)
	require.NotZero(t, state.ConfirmedAt)
}

func TestEnsureDatasetPeriodRejectsInvalidStorageResponse(t *testing.T) {
	period := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		status string
		until  int64
	}{
		{name: "unknown status", status: "unknown", until: period.Add(time.Minute).Unix()},
		{name: "missing deadline", status: domain.PeriodStatusWaiting},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writer := &storageWriter{period: &periodAccessStub{ensure: &storagepb.PrimaryEnsureDatasetPeriodRsp{RetInfo: storageSuccess(), Status: tc.status, DeadlineAt: tc.until}}}
			_, err := writer.EnsureDatasetPeriod(context.Background(), validStorageExpectation(period))
			require.Error(t, err)
		})
	}
}

func TestGetDatasetPeriodStatusValidatesIdentityAndDoesNotCreate(t *testing.T) {
	period := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	expectation := validStorageExpectation(period)
	access := &periodAccessStub{status: &storagepb.PrimaryGetDatasetPeriodStatusRsp{
		RetInfo: storageSuccess(), Status: domain.PeriodStatusComplete, SeriesHash: "wrong",
		ExpectedCount: expectation.ExpectedCount, DeadlineAt: period.Add(time.Minute).Unix(),
	}}
	writer := &storageWriter{period: access}
	_, err := writer.GetDatasetPeriodStatus(context.Background(), expectation)
	require.Error(t, err)
	require.Zero(t, access.ensureCalls)
	require.Equal(t, 1, access.statusCalls)
}

func TestGetDatasetPeriodStatusNotFoundIsNotFabricatedAsWaiting(t *testing.T) {
	period := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	access := &periodAccessStub{status: &storagepb.PrimaryGetDatasetPeriodStatusRsp{
		RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_NOT_FOUND},
	}}
	state, err := (&storageWriter{period: access}).GetDatasetPeriodStatus(context.Background(), validStorageExpectation(period))
	require.ErrorIs(t, err, ErrDatasetPeriodNotFound)
	require.Zero(t, state)
	require.Zero(t, access.ensureCalls)
}

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

func (s *periodAccessStub) RecordDatasetPeriodFailures(context.Context, *storagepb.PrimaryRecordDatasetPeriodFailuresReq, ...client.Option) (*storagepb.PrimaryRecordDatasetPeriodFailuresRsp, error) {
	return nil, nil
}
