package marketfetch

import (
	"context"
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
	periodStorageReconcileCandidateLimit = 1000
	periodStorageRetention               = 30 * 24 * time.Hour
)

type PeriodStorageStatusClient interface {
	GetDatasetPeriodStatus(context.Context, *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error)
}

// PeriodStorageReconciler probes expired snapshots against Storage before
// recording authority or reclaiming local state. It is created per Space so
// its in-memory keyset cursor survives timer rounds without another table.
type PeriodStorageReconciler struct {
	Snapshots *store.PeriodSeriesSnapshotRepository
	States    *store.PeriodStorageStateRepository
	Storage   PeriodStorageStatusClient
	SpaceID   string

	mu     sync.Mutex
	cursor *store.PeriodSeriesSnapshotCursor
}

func NewPeriodStorageReconciler(
	snapshots *store.PeriodSeriesSnapshotRepository,
	states *store.PeriodStorageStateRepository,
	storage PeriodStorageStatusClient,
	spaceID string,
) *PeriodStorageReconciler {
	return &PeriodStorageReconciler{Snapshots: snapshots, States: states, Storage: storage, SpaceID: strings.TrimSpace(spaceID)}
}

// Reconcile probes at most 1000 expired snapshot periods in stable keyset
// order, then deletes only matching terminal periods. Failed probes advance
// the cursor and are retried after the scan wraps on a later round.
func (r *PeriodStorageReconciler) Reconcile(ctx context.Context, now time.Time) (int64, error) {
	if r == nil || r.Snapshots == nil || r.States == nil || r.Storage == nil {
		return 0, fmt.Errorf("period Storage reconciler is not initialized")
	}
	spaceID := strings.TrimSpace(r.SpaceID)
	if spaceID == "" || now.IsZero() {
		return 0, fmt.Errorf("period Storage reconciler space_id and current time are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := now.UTC().Add(-periodStorageRetention)
	page, err := r.Snapshots.ListCleanupCandidates(ctx, spaceID, cutoff, r.cursor, periodStorageReconcileCandidateLimit)
	if err != nil {
		return 0, fmt.Errorf("list expired Collector period snapshots: %w", err)
	}
	var firstErr error
	if len(page.Snapshots) == 0 {
		r.cursor = nil
	} else {
		for _, snapshot := range page.Snapshots {
			key := snapshot.Key
			// Advance before the network call so a failed Storage endpoint cannot
			// pin the scan on the same oldest candidate forever.
			r.cursor = &store.PeriodSeriesSnapshotCursor{PeriodTime: key.PeriodTime, DatasetID: key.DatasetID, Frequency: key.Frequency}
			expectation := &storagepb.DatasetPeriodExpectation{
				SpaceId: key.SpaceID, DatasetId: key.DatasetID, Frequency: key.Frequency, PeriodTime: key.PeriodTime.Unix(),
				SeriesHash: snapshot.SeriesHash, ExpectedCount: snapshot.ExpectedCount,
			}
			state, err := r.Storage.GetDatasetPeriodStatus(ctx, expectation)
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("probe Storage period %s/%s/%s/%s: %w", key.SpaceID, key.DatasetID, key.Frequency, key.PeriodTime.Format(time.RFC3339), err)
				}
				continue
			}
			state.Normalize()
			if err := validatePeriodStorageObservation(snapshot, state); err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("Storage returned invalid period state for %s/%s/%s/%s: %w", key.SpaceID, key.DatasetID, key.Frequency, key.PeriodTime.Format(time.RFC3339), err)
				}
				continue
			}
			if err := r.States.ObservePeriodStorageState(ctx, state); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("persist Storage period state for %s/%s/%s/%s: %w", key.SpaceID, key.DatasetID, key.Frequency, key.PeriodTime.Format(time.RFC3339), err)
			}
		}
		if firstErr != nil {
			log.WarnContextf(ctx, "period Storage reconciliation deferred space=%s after scanning %d candidates: %v", spaceID, len(page.Snapshots), firstErr)
		}
	}
	deleted, err := r.Snapshots.CleanupTerminalBefore(ctx, spaceID, cutoff, periodStorageReconcileCandidateLimit)
	if err != nil {
		return deleted, fmt.Errorf("cleanup terminal Collector period snapshots: %w", err)
	}
	return deleted, firstErr
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
