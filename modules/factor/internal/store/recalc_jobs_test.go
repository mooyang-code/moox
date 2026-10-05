package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestEnableMemberWithRecalcJobIsAtomicAndIdempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	set := testSet("set_prices", domain.SetStatusEnabled)
	require.NoError(t, s.CreateSet(ctx, set))
	factor := testFactorDef("momentum")
	addTestMember(t, s, set.SetID, factor.FactorID, domain.MemberStatusDisabled)
	job := RecalcJob{
		JobID: "enable-request", RequestID: "enable-request", SetID: set.SetID,
		FactorIDs: []string{factor.FactorID}, Subjects: []string{"BTC"},
		StartTime: 100, EndTime: 200, Status: RecalcStatusAccepted,
	}

	accepted, err := s.EnableMemberWithRecalcJob(ctx, set.SetID, factor.FactorID, domain.MemberStatusDisabled, job)
	require.NoError(t, err)
	require.Equal(t, job.JobID, accepted.JobID)
	member, err := s.GetMember(ctx, set.SetID, factor.FactorID)
	require.NoError(t, err)
	require.Equal(t, domain.MemberStatusEnabled, member.Status)

	acceptedAgain, err := s.EnableMemberWithRecalcJob(ctx, set.SetID, factor.FactorID, domain.MemberStatusDisabled, job)
	require.NoError(t, err)
	require.Equal(t, accepted, acceptedAgain)

	conflict := job
	conflict.EndTime++
	_, err = s.EnableMemberWithRecalcJob(ctx, set.SetID, factor.FactorID, domain.MemberStatusDisabled, conflict)
	require.ErrorIs(t, err, ErrConflict)
}

func TestRecalcJobRequestIDIsIdempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	first, err := s.CreateRecalcJob(ctx, RecalcJob{
		JobID: "job-first", RequestID: "request-1", SetID: "set_prices",
		StartTime: 100, EndTime: 200, Status: RecalcStatusAccepted,
	})
	require.NoError(t, err)
	second, err := s.CreateRecalcJob(ctx, RecalcJob{
		JobID: "job-retry", RequestID: "request-1", SetID: "set_prices",
		StartTime: 100, EndTime: 200, Status: RecalcStatusAccepted,
	})
	require.NoError(t, err)
	require.Equal(t, first.JobID, second.JobID)
	require.Equal(t, first, second)
}

func TestConcurrentRecalcJobRequestIDIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factor.db")
	firstStore, err := Open(&Options{Path: path, MaxOpenConns: 2})
	require.NoError(t, err)
	secondStore, err := Open(&Options{Path: path, MaxOpenConns: 2})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, firstStore.Close()); require.NoError(t, secondStore.Close()) })

	jobs := []RecalcJob{
		{JobID: "job-a", RequestID: "request-race", SetID: "set", FactorIDs: []string{"a"}, Subjects: []string{"BTC"}, FactorsOmitted: true, SubjectsOmitted: true, StartTime: 1, EndTime: 2},
		{JobID: "job-b", RequestID: "request-race", SetID: "set", FactorIDs: []string{"a", "b"}, Subjects: []string{"BTC", "ETH"}, FactorsOmitted: true, SubjectsOmitted: true, StartTime: 1, EndTime: 2},
	}
	results := concurrentCreate(t, firstStore, secondStore, jobs)
	require.NoError(t, results[0].err)
	require.NoError(t, results[1].err)
	require.Equal(t, results[0].job.JobID, results[1].job.JobID)
	require.Equal(t, results[0].job.FactorIDs, results[1].job.FactorIDs)
	require.Equal(t, results[0].job.Subjects, results[1].job.Subjects)
}

func TestConcurrentDifferentRecalcPayloadConflicts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factor.db")
	firstStore, err := Open(&Options{Path: path, MaxOpenConns: 2})
	require.NoError(t, err)
	secondStore, err := Open(&Options{Path: path, MaxOpenConns: 2})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, firstStore.Close()); require.NoError(t, secondStore.Close()) })
	jobs := []RecalcJob{
		{JobID: "job-a", RequestID: "request-conflict", SetID: "set", FactorIDs: []string{"a"}, StartTime: 1, EndTime: 2},
		{JobID: "job-b", RequestID: "request-conflict", SetID: "set", FactorIDs: []string{"b"}, StartTime: 1, EndTime: 2},
	}
	results := concurrentCreate(t, firstStore, secondStore, jobs)
	require.NotEqual(t, results[0].err == nil, results[1].err == nil)
	conflict := results[0].err
	if conflict == nil {
		conflict = results[1].err
	}
	require.ErrorIs(t, conflict, ErrConflict)
}

type createResult struct {
	job RecalcJob
	err error
}

func concurrentCreate(t *testing.T, firstStore, secondStore *Store, jobs []RecalcJob) []createResult {
	t.Helper()
	stores := []*Store{firstStore, secondStore}
	results := make([]createResult, len(jobs))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range jobs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i].job, results[i].err = stores[i].CreateRecalcJob(context.Background(), jobs[i])
		}(i)
	}
	close(start)
	wg.Wait()
	return results
}

func TestUpdateRecalcProgressAndCancel(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	job, err := s.CreateRecalcJob(ctx, RecalcJob{
		JobID: "job-1", RequestID: "request-1", SetID: "set_prices",
		StartTime: 100, EndTime: 500, Status: RecalcStatusAccepted,
	})
	require.NoError(t, err)

	job, err = s.UpdateRecalcJob(ctx, job.JobID, func(current *RecalcJob) error {
		current.Status = RecalcStatusRunning
		current.ProgressTime = 300
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, RecalcStatusRunning, job.Status)
	require.EqualValues(t, 300, job.ProgressTime)

	job, err = s.UpdateRecalcJob(ctx, job.JobID, func(current *RecalcJob) error {
		current.ProgressTime = 200
		return nil
	})
	require.NoError(t, err)
	require.EqualValues(t, 300, job.ProgressTime)

	job, err = s.UpdateRecalcJob(ctx, job.JobID, func(current *RecalcJob) error {
		current.Status = RecalcStatusCancelled
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, RecalcStatusCancelled, job.Status)

	job, err = s.UpdateRecalcJob(ctx, job.JobID, func(current *RecalcJob) error {
		current.Status = RecalcStatusRunning
		current.ProgressTime = 400
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, RecalcStatusCancelled, job.Status)
	require.EqualValues(t, 300, job.ProgressTime)
}

func TestListRecalcJobsFiltersSetAndStatuses(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for _, job := range []RecalcJob{
		{JobID: "job-a", RequestID: "req-a", SetID: "set", StartTime: 1, EndTime: 2, Status: RecalcStatusAccepted},
		{JobID: "job-b", RequestID: "req-b", SetID: "set", StartTime: 1, EndTime: 2, Status: RecalcStatusRunning},
		{JobID: "job-c", RequestID: "req-c", SetID: "other", StartTime: 1, EndTime: 2, Status: RecalcStatusRunning},
	} {
		_, err := s.CreateRecalcJob(ctx, job)
		require.NoError(t, err)
	}
	running, err := s.ListRecalcJobs(ctx, RecalcJobFilter{Statuses: []string{RecalcStatusRunning}})
	require.NoError(t, err)
	require.Len(t, running, 2)

	jobs, err := s.ListRecalcJobs(ctx, RecalcJobFilter{SetID: "set", Statuses: []string{RecalcStatusRunning}})
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Equal(t, "job-b", jobs[0].JobID)

	all, err := s.ListRecalcJobs(ctx, RecalcJobFilter{SetID: "set"})
	require.NoError(t, err)
	require.Len(t, all, 2)
}

func TestRecalcJobPersistsAcrossStoreReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factor.db")
	s, err := Open(&Options{Path: path})
	require.NoError(t, err)
	job, err := s.CreateRecalcJob(context.Background(), RecalcJob{
		JobID: "job-1", RequestID: "request-1", SetID: "set_prices",
		StartTime: 100, EndTime: 200, Status: RecalcStatusAccepted,
	})
	require.NoError(t, err)
	require.NoError(t, s.Close())

	s, err = Open(&Options{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	got, err := s.GetRecalcJob(context.Background(), job.JobID)
	require.NoError(t, err)
	require.Equal(t, job, got)
}
