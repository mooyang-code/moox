package marketfetch

import (
	"context"
	"testing"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

type deadlineRecordingStorage struct {
	remaining time.Duration
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

func TestStorageAndPublishReservesColdEventBusConnection(t *testing.T) {
	commit, publish := storageAndPublishReserves(5*time.Second, 0, true)
	require.Equal(t, 18750*time.Millisecond, commit)
	require.Equal(t, 13*time.Second, publish)
	require.GreaterOrEqual(t, publish, 2*(completionConnectTimeout+3*time.Second)+300*time.Millisecond)
}

func TestTimerBudgetLeavesBoundedStorageEventBusAndCLSWindows(t *testing.T) {
	t.Setenv("MOOX_FETCH_TIMEOUT_SECONDS", "")
	t.Setenv("MOOX_MARKET_FETCH_TIMEOUT_SECONDS", "")
	claim := timerClaimTimeout
	commit, publish := storageAndPublishReserves(5*time.Second, 0, true)
	ctx, cancel := executionContext(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	budget := time.Until(deadline)
	require.Equal(t, 13*time.Second, publish)
	require.Equal(t, metricsResponseReserve, commit-5*time.Second-publish)
	provider := 4 * 4 * time.Second // 40 Timer subjects / 10 inflight, one bound source.
	require.Greater(t, budget-claim-commit-3*time.Second, provider, "Timer must preserve Claim, provider attempts, Storage, Completion and CLS")
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
