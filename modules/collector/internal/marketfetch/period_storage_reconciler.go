package marketfetch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	periodStorageReconcileCandidateLimit = 500
	periodStorageWaitingProbeLimit       = 500
	periodStorageCleanupRowLimit         = 1000
	periodStorageCleanupManifestLimit    = 1000
	periodStorageStatusProbeConcurrency  = 10
	periodStorageRetention               = 30 * 24 * time.Hour
)

type PeriodStorageStatusClient interface {
	GetDatasetPeriodStatus(context.Context, *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error)
}

// PeriodStorageReconciler probes expired snapshots against Storage before
// recording authority or reclaiming local state. It is created per Space so
// its in-memory keyset cursor survives timer rounds without another table.
type PeriodStorageReconciler struct {
	Metrics   *Metrics
	Snapshots *store.PeriodSeriesSnapshotRepository
	States    *store.PeriodStorageStateRepository
	Batches   *store.TimerPeriodBatchRepository
	Storage   PeriodStorageStatusClient
	SpaceID   string

	mu            sync.Mutex
	cursor        *store.PeriodSeriesSnapshotCursor
	waitingCursor *store.PeriodStorageStateCursor
}

func NewPeriodStorageReconciler(
	snapshots *store.PeriodSeriesSnapshotRepository,
	states *store.PeriodStorageStateRepository,
	storage PeriodStorageStatusClient,
	spaceID string,
	timerBatches ...*store.TimerPeriodBatchRepository,
) *PeriodStorageReconciler {
	reconciler := &PeriodStorageReconciler{Snapshots: snapshots, States: states, Storage: storage, SpaceID: strings.TrimSpace(spaceID)}
	if len(timerBatches) > 0 {
		reconciler.Batches = timerBatches[0]
	}
	return reconciler
}

// ReconcileWithCleanupCounts preserves committed counts from earlier cleanup
// transactions even when later probing or cleanup fails.
func (r *PeriodStorageReconciler) ReconcileWithCleanupCounts(ctx context.Context, now time.Time, retention time.Duration, cleanupRowBudget, manifestLimit int) (counts store.PeriodCleanupCounts, retErr error) {
	if r == nil || r.Snapshots == nil || r.States == nil || r.Storage == nil {
		return counts, fmt.Errorf("period Storage reconciler is not initialized")
	}
	spaceID := strings.TrimSpace(r.SpaceID)
	if spaceID == "" || now.IsZero() || retention <= 0 || cleanupRowBudget < 0 || manifestLimit < 0 {
		return counts, fmt.Errorf("period Storage reconciler space_id, current time, positive retention, and nonnegative cleanup limits are required")
	}
	started := time.Now()
	defer func() {
		if r.Metrics != nil {
			r.Metrics.ObservePeriodStorageReconcile(spaceID, time.Since(started), retErr)
		}
	}()
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := now.UTC().Add(-retention)
	firstErr := r.reconcileWaitingStates(ctx, spaceID)
	if r.Batches != nil && manifestLimit > 0 {
		deleted, err := r.Batches.CleanupStorageTerminal(ctx, spaceID, manifestLimit)
		if err != nil {
			return counts, errors.Join(firstErr, fmt.Errorf("cleanup terminal Timer period manifests: %w", err))
		}
		counts.ManifestRows = deleted
	}
	page, err := r.Snapshots.ListCleanupCandidates(ctx, spaceID, cutoff, r.cursor, periodStorageReconcileCandidateLimit)
	if err != nil {
		return counts, errors.Join(firstErr, fmt.Errorf("list expired Collector period snapshots: %w", err))
	}
	if len(page.Snapshots) == 0 {
		r.cursor = nil
	} else {
		lastKey := page.Snapshots[len(page.Snapshots)-1].Key
		// Advance before concurrent network calls so one failed Storage probe
		// cannot pin this pass on the oldest candidate.
		r.cursor = &store.PeriodSeriesSnapshotCursor{PeriodTime: lastKey.PeriodTime, DatasetID: lastKey.DatasetID, Frequency: lastKey.Frequency}
		expectations := make([]*storagepb.DatasetPeriodExpectation, len(page.Snapshots))
		for index, snapshot := range page.Snapshots {
			key := snapshot.Key
			expectations[index] = &storagepb.DatasetPeriodExpectation{
				SpaceId: key.SpaceID, DatasetId: key.DatasetID, Frequency: key.Frequency, PeriodTime: key.PeriodTime.Unix(),
				SeriesHash: snapshot.SeriesHash, ExpectedCount: snapshot.ExpectedCount,
			}
		}
		results := r.probeStoragePeriods(ctx, expectations)
		for index, result := range results {
			snapshot := page.Snapshots[index]
			key := snapshot.Key
			if result.err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("probe Storage period %s/%s/%s/%s: %w", key.SpaceID, key.DatasetID, key.Frequency, key.PeriodTime.Format(time.RFC3339), result.err)
				}
				continue
			}
			result.state.Normalize()
			if err := validatePeriodStorageObservation(snapshot, result.state); err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("Storage returned invalid period state for %s/%s/%s/%s: %w", key.SpaceID, key.DatasetID, key.Frequency, key.PeriodTime.Format(time.RFC3339), err)
				}
				continue
			}
			if err := r.States.ObservePeriodStorageState(ctx, result.state); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("persist Storage period state for %s/%s/%s/%s: %w", key.SpaceID, key.DatasetID, key.Frequency, key.PeriodTime.Format(time.RFC3339), err)
			}
		}
		if firstErr != nil {
			log.WarnContextf(ctx, "period Storage reconciliation deferred space=%s after scanning %d candidates: %v", spaceID, len(page.Snapshots), firstErr)
		}
	}
	if cleanupRowBudget > 0 {
		deleted, err := r.Snapshots.CleanupTerminalBeforeWithCounts(ctx, spaceID, cutoff, cleanupRowBudget)
		if err != nil {
			return counts, fmt.Errorf("cleanup terminal Collector period snapshots: %w", err)
		}
		counts.SnapshotRows, counts.StateRows = deleted.SnapshotRows, deleted.StateRows
	}
	return counts, firstErr
}

func (r *PeriodStorageReconciler) reconcileWaitingStates(ctx context.Context, spaceID string) error {
	states, err := r.States.ListWaiting(ctx, spaceID, r.waitingCursor, periodStorageWaitingProbeLimit)
	if err != nil {
		return fmt.Errorf("list waiting Collector period Storage states: %w", err)
	}
	if len(states) == 0 {
		r.waitingCursor = nil
		return nil
	}
	last := states[len(states)-1]
	r.waitingCursor = &store.PeriodStorageStateCursor{
		DatasetID: last.Key.DatasetID, Frequency: last.Key.Frequency, PeriodTime: last.Key.PeriodTime,
	}
	if len(states) < periodStorageWaitingProbeLimit {
		r.waitingCursor = nil
	}
	expectations := make([]*storagepb.DatasetPeriodExpectation, len(states))
	for index, expected := range states {
		expectations[index] = &storagepb.DatasetPeriodExpectation{
			SpaceId: expected.Key.SpaceID, DatasetId: expected.Key.DatasetID, Frequency: expected.Key.Frequency,
			PeriodTime: expected.Key.PeriodTime.Unix(), SeriesHash: expected.SeriesHash, ExpectedCount: expected.ExpectedCount,
		}
	}
	results := r.probeStoragePeriods(ctx, expectations)
	var firstErr error
	for index, result := range results {
		expected := states[index]
		if result.err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("probe waiting Storage period %s/%s/%s/%s: %w", expected.Key.SpaceID, expected.Key.DatasetID, expected.Key.Frequency, expected.Key.PeriodTime.Format(time.RFC3339), result.err)
			}
			continue
		}
		result.state.Normalize()
		if err := validatePeriodStorageStateObservation(expected, result.state); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("Storage returned invalid waiting period state for %s/%s/%s/%s: %w", expected.Key.SpaceID, expected.Key.DatasetID, expected.Key.Frequency, expected.Key.PeriodTime.Format(time.RFC3339), err)
			}
			continue
		}
		if err := r.States.ObservePeriodStorageState(ctx, result.state); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("persist Storage period state for %s/%s/%s/%s: %w", expected.Key.SpaceID, expected.Key.DatasetID, expected.Key.Frequency, expected.Key.PeriodTime.Format(time.RFC3339), err)
		}
	}
	return firstErr
}

type periodStorageProbeResult struct {
	state domain.PeriodStorageState
	err   error
}

func (r *PeriodStorageReconciler) probeStoragePeriods(ctx context.Context, expectations []*storagepb.DatasetPeriodExpectation) []periodStorageProbeResult {
	results := make([]periodStorageProbeResult, len(expectations))
	semaphore := make(chan struct{}, periodStorageStatusProbeConcurrency)
	var workers sync.WaitGroup
	for index, expectation := range expectations {
		workers.Add(1)
		go func(index int, expectation *storagepb.DatasetPeriodExpectation) {
			defer workers.Done()
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				results[index].err = ctx.Err()
				return
			}
			defer func() { <-semaphore }()
			results[index].state, results[index].err = r.Storage.GetDatasetPeriodStatus(ctx, expectation)
			if r.Metrics != nil {
				result := "success"
				if results[index].err != nil {
					result = "error"
				}
				r.Metrics.ObservePeriodStorageProbe(strings.TrimSpace(expectation.GetSpaceId()), result)
			}
		}(index, expectation)
	}
	workers.Wait()
	return results
}

func validatePeriodStorageObservation(snapshot domain.PeriodSeriesSnapshot, state domain.PeriodStorageState) error {
	if state.Key.SpaceID != snapshot.Key.SpaceID || state.Key.DatasetID != snapshot.Key.DatasetID || state.Key.Frequency != snapshot.Key.Frequency || !state.Key.PeriodTime.Equal(snapshot.Key.PeriodTime) {
		return fmt.Errorf("period key does not match the probed snapshot")
	}
	if state.SeriesHash != snapshot.SeriesHash || state.ExpectedCount != snapshot.ExpectedCount {
		return fmt.Errorf("series hash/count does not match the probed snapshot")
	}
	if state.Status != domain.PeriodStatusWaiting && state.Status != domain.PeriodStatusComplete && state.Status != domain.PeriodStatusDegraded {
		return fmt.Errorf("unsupported status %q", state.Status)
	}
	if state.DeadlineAt.IsZero() || state.ConfirmedAt.IsZero() {
		return fmt.Errorf("Storage deadline and confirmation time are required")
	}
	return nil
}

func validatePeriodStorageStateObservation(expected, observed domain.PeriodStorageState) error {
	if observed.Key.SpaceID != expected.Key.SpaceID || observed.Key.DatasetID != expected.Key.DatasetID || observed.Key.Frequency != expected.Key.Frequency || !observed.Key.PeriodTime.Equal(expected.Key.PeriodTime) {
		return fmt.Errorf("period key does not match the probed state")
	}
	if observed.SeriesHash != expected.SeriesHash || observed.ExpectedCount != expected.ExpectedCount || !observed.DeadlineAt.Equal(expected.DeadlineAt) {
		return fmt.Errorf("period snapshot identity or deadline does not match the probed state")
	}
	if observed.Status != domain.PeriodStatusWaiting && observed.Status != domain.PeriodStatusComplete && observed.Status != domain.PeriodStatusDegraded {
		return fmt.Errorf("unsupported status %q", observed.Status)
	}
	if observed.ConfirmedAt.IsZero() {
		return fmt.Errorf("Storage confirmation time is required")
	}
	return nil
}
