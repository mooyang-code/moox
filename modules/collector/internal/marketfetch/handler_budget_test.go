package marketfetch

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

type deadlineRecordingStorage struct {
	remaining time.Duration
}

type stalledInstrumentNamesStorage struct{ deadlineRecordingStorage }

func (*stalledInstrumentNamesStorage) ListInstrumentNames(ctx context.Context, _ string, _ []string) (map[string]string, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestInstrumentNamesReadHasShortIndependentBudget(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	storage := &reservedDeadlineStorage{Storage: &stalledInstrumentNamesStorage{}, parent: parent, timeout: 5 * time.Second}
	start := time.Now()
	_, err := storage.ListInstrumentNames(parent, StockCNSpaceID, []string{"600000.XSHG"})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 400*time.Millisecond)
	require.NoError(t, parent.Err())
	require.True(t, storage.writeDeadline.IsZero(), "best-effort presentation lookup must not spend the write window")
}

type slowDeadlineCommitStorage struct {
	recordingPeriodFailureStorage
}

func (s *slowDeadlineCommitStorage) ListInstrumentNames(ctx context.Context, _ string, _ []string) (map[string]string, error) {
	return map[string]string{"600000.XSHG": "stock"}, ctx.Err()
}

func (s *slowDeadlineCommitStorage) CommitTimeSeriesBatch(ctx context.Context, _ *storagepb.DatasetPeriodExpectation, _ []*storagepb.TimeSeriesBatchRow, _ string) error {
	select {
	case <-time.After(60 * time.Millisecond):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *deadlineRecordingStorage) UpsertFields(ctx context.Context, _ []*storagepb.RowFieldUpsert) error {
	deadline, ok := ctx.Deadline()
	if ok {
		s.remaining = time.Until(deadline)
	}
	return ctx.Err()
}

func TestContextWithReserveEndsFetchBeforeStorageAndCLSWindow(t *testing.T) {
	parent, parentCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer parentCancel()
	work, workCancel := contextWithReserve(parent, 180*time.Millisecond)
	defer workCancel()
	deadline, ok := work.Deadline()
	require.True(t, ok)
	remaining := time.Until(deadline)
	require.Positive(t, remaining)
	require.LessOrEqual(t, remaining, 140*time.Millisecond)
}

func TestReservedDeadlineStorageSharesWriteDeadlineAndLeavesPublishReserve(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	storage := &reservedDeadlineStorage{Storage: &slowDeadlineCommitStorage{}, parent: parent, timeout: 100 * time.Millisecond}
	_, err := storage.ListInstrumentNames(context.Background(), "stockcn", nil)
	require.NoError(t, err)
	// Reading names must not start the write budget.
	time.Sleep(110 * time.Millisecond)
	start := time.Now()
	require.NoError(t, storage.CommitTimeSeriesBatch(context.Background(), nil, nil, "first"))
	require.ErrorIs(t, storage.CommitTimeSeriesBatch(context.Background(), nil, nil, "second"), context.DeadlineExceeded)
	require.Less(t, time.Since(start), 150*time.Millisecond, "two destinations share one 100ms write window")
	require.NoError(t, parent.Err(), "the publisher still owns the reserved parent window")
	publishCtx, publishCancel := context.WithTimeout(parent, 50*time.Millisecond)
	defer publishCancel()
	deadline, ok := publishCtx.Deadline()
	require.True(t, ok)
	require.Greater(t, time.Until(deadline), 40*time.Millisecond, "Completion retains its full publication reserve after two slow commits")
}

func TestReservedDeadlineStorageUsesReservedParentBudget(t *testing.T) {
	parent, parentCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer parentCancel()
	expiredWork, workCancel := context.WithCancel(parent)
	workCancel()
	underlying := &deadlineRecordingStorage{}
	storage := &reservedDeadlineStorage{Storage: underlying, parent: parent, timeout: 120 * time.Millisecond}

	require.NoError(t, storage.UpsertFields(expiredWork, []*storagepb.RowFieldUpsert{{}}))
	require.Positive(t, underlying.remaining)
	require.LessOrEqual(t, underlying.remaining, 130*time.Millisecond)
}

func TestReservedDeadlineStorageForwardsInstrumentNames(t *testing.T) {
	underlying := &pipelineStorageWithInstrumentNames{names: map[string]string{"600000.XSHG": "浦发银行"}}
	storage := &reservedDeadlineStorage{Storage: underlying, parent: context.Background(), timeout: time.Second}

	names, err := storage.ListInstrumentNames(context.Background(), StockCNSpaceID, []string{"600000.XSHG"})
	require.NoError(t, err)
	require.Equal(t, "浦发银行", names["600000.XSHG"])
}

// recordingPeriodFailureStorage 是不做任何事的 Storage 测试替身，记录对周期状态的调用。
type recordingPeriodFailureStorage struct {
	ensured []*storagepb.DatasetPeriodExpectation
}

func (s *recordingPeriodFailureStorage) UpsertFields(context.Context, []*storagepb.RowFieldUpsert) error {
	return nil
}

func (s *recordingPeriodFailureStorage) EnsureDatasetPeriod(_ context.Context, exp *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error) {
	s.ensured = append(s.ensured, exp)
	return domain.PeriodStorageState{}, nil
}

func (s *recordingPeriodFailureStorage) GetDatasetPeriodStatus(context.Context, *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error) {
	return domain.PeriodStorageState{}, nil
}

func (s *recordingPeriodFailureStorage) CommitTimeSeriesBatch(context.Context, *storagepb.DatasetPeriodExpectation, []*storagepb.TimeSeriesBatchRow, string) error {
	return nil
}

func (s *recordingPeriodFailureStorage) RecordDatasetPeriodFailures(context.Context, *storagepb.DatasetPeriodExpectation, []uint32) ([]*storagepb.DatasetPeriodFailureResult, error) {
	return nil, nil
}
