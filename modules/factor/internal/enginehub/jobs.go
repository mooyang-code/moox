package enginehub

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
)

// maxPullAttempts bounds how many unrunnable jobs one pull may fail before it
// gives up for this round.
const maxPullAttempts = 16

// Pull leases the next runnable recalc job to the engine holding the lease.
// Jobs of enabled sets whose result dataset is not ready are left queued; jobs
// whose set is no longer enabled or whose selected factors are no longer all
// enabled fail immediately, as they could never run.
func (h *Hub) Pull(ctx context.Context, id domain.EngineIdentity) (store.RecalcJob, domain.EngineSet, bool, error) {
	now := h.now().UTC()
	if !h.holdsLease(id.EngineID, now) {
		return store.RecalcJob{}, domain.EngineSet{}, false, ErrLeaseConflict
	}
	skip, err := h.unreadySets(ctx)
	if err != nil {
		return store.RecalcJob{}, domain.EngineSet{}, false, err
	}
	for attempt := 0; attempt < maxPullAttempts; attempt++ {
		job, found, err := h.store.PullRecalcJob(ctx, id.EngineID, now, h.jobTTL, skip)
		if err != nil || !found {
			return store.RecalcJob{}, domain.EngineSet{}, false, err
		}
		set, err := h.runnableSet(ctx, job)
		if err != nil {
			if _, failErr := h.store.ReportRecalcProgress(ctx, job.JobID, job.LeaseToken, job.ProgressTime,
				store.RecalcStatusFailed, err.Error(), now, h.jobTTL); failErr != nil {
				return store.RecalcJob{}, domain.EngineSet{}, false, failErr
			}
			continue
		}
		return job, set, true, nil
	}
	return store.RecalcJob{}, domain.EngineSet{}, false, nil
}

// Report records a chunk of a leased job and returns the job's current state.
func (h *Hub) Report(ctx context.Context, jobID, leaseToken string, progress time.Time, status, errText string) (store.RecalcJob, error) {
	return h.store.ReportRecalcProgress(ctx, jobID, leaseToken, progress.Unix(), status, errText, h.now().UTC(), h.jobTTL)
}

func (h *Hub) runnableSet(ctx context.Context, job store.RecalcJob) (domain.EngineSet, error) {
	set, err := h.store.GetSet(ctx, job.SetID)
	if err != nil {
		return domain.EngineSet{}, err
	}
	if set.Status != domain.SetStatusEnabled {
		return domain.EngineSet{}, fmt.Errorf("factor set %q is not enabled", set.SetID)
	}
	members, err := h.store.ListMembers(ctx, set.SetID, "")
	if err != nil {
		return domain.EngineSet{}, err
	}
	factors, err := domain.SelectEnabledFactors(members, job.FactorIDs)
	if err != nil {
		return domain.EngineSet{}, err
	}
	return domain.EngineSet{Set: set, Factors: factors, ResultReady: h.isReady(set.SetID)}, nil
}

func (h *Hub) unreadySets(ctx context.Context) (map[string]bool, error) {
	sets, err := h.store.ListSets(ctx)
	if err != nil {
		return nil, err
	}
	skip := make(map[string]bool)
	for _, set := range sets {
		if set.Status == domain.SetStatusEnabled && !h.isReady(set.SetID) {
			skip[set.SetID] = true
		}
	}
	return skip, nil
}
