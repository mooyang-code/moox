package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

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
		Members: []string{factor.FactorID}, Subjects: []string{"BTC"},
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
		{JobID: "job-a", RequestID: "request-race", SetID: "set", Members: []string{"a"}, Subjects: []string{"BTC"}, MembersOmitted: true, SubjectsOmitted: true, StartTime: 1, EndTime: 2},
		{JobID: "job-b", RequestID: "request-race", SetID: "set", Members: []string{"a", "b"}, Subjects: []string{"BTC", "ETH"}, MembersOmitted: true, SubjectsOmitted: true, StartTime: 1, EndTime: 2},
	}
	results := concurrentCreate(t, firstStore, secondStore, jobs)
	require.NoError(t, results[0].err)
	require.NoError(t, results[1].err)
	require.Equal(t, results[0].job.JobID, results[1].job.JobID)
	require.Equal(t, results[0].job.Members, results[1].job.Members)
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
		{JobID: "job-a", RequestID: "request-conflict", SetID: "set", Members: []string{"a"}, StartTime: 1, EndTime: 2},
		{JobID: "job-b", RequestID: "request-conflict", SetID: "set", Members: []string{"b"}, StartTime: 1, EndTime: 2},
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

// 连续启用同一因子集的多个成员，只保留一个还没开始跑的回填任务：每个成员各建一个任务，
// 同一个窗口就会被重复读 N 遍（2026-10-14 两个 1h 因子集 11 个成员各 11 个任务，把 storage-node 打满）。
func TestEnableMemberMergesIntoTheQueuedBackfillOfTheSameSet(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	set := testSet("set_prices", domain.SetStatusEnabled)
	require.NoError(t, s.CreateSet(ctx, set))
	factors := []string{"momentum", "bias", "cci"}
	for _, id := range factors {
		addTestMember(t, s, set.SetID, id, domain.MemberStatusDisabled)
	}
	enable := func(factorID string, start, end int64) RecalcJob {
		job, err := s.EnableMemberWithRecalcJob(ctx, set.SetID, factorID, domain.MemberStatusDisabled, RecalcJob{
			RequestID: "factor-enable-" + factorID, SetID: set.SetID, Members: []string{factorID}, Subjects: []string{},
			StartTime: start, EndTime: end, Status: RecalcStatusAccepted,
		})
		require.NoError(t, err)
		return job
	}

	first := enable("momentum", 100, 200)
	second := enable("bias", 90, 210)
	third := enable("cci", 100, 200)
	require.Equal(t, first.JobID, second.JobID)
	require.Equal(t, first.JobID, third.JobID)
	require.Equal(t, []string{"bias", "cci", "momentum"}, third.Members)
	require.Equal(t, int64(90), third.StartTime, "窗口取并集")
	require.Equal(t, int64(210), third.EndTime)

	jobs, err := s.ListRecalcJobs(ctx, RecalcJobFilter{SetID: set.SetID})
	require.NoError(t, err)
	require.Len(t, jobs, 1, "三个成员只有一个回填任务")
	for _, id := range factors {
		member, err := s.GetMember(ctx, set.SetID, id)
		require.NoError(t, err)
		require.Equal(t, domain.MemberStatusEnabled, member.Status)
	}
}

func TestEnableMemberDoesNotMergeIntoRunningManualOrOtherSetJobs(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for _, id := range []string{"set_a", "set_b"} {
		set := testSet(id, domain.SetStatusEnabled)
		set.SourceDatasetID, set.ResultDatasetID = "dataset_"+id, "dataset_factor_"+id
		require.NoError(t, s.CreateSet(ctx, set))
	}
	for _, member := range [][2]string{{"set_a", "f1"}, {"set_a", "f2"}, {"set_a", "f3"}, {"set_b", "f1"}} {
		addTestMember(t, s, member[0], member[1], domain.MemberStatusDisabled)
	}
	enable := func(setID, factorID, request string) RecalcJob {
		job, err := s.EnableMemberWithRecalcJob(ctx, setID, factorID, domain.MemberStatusDisabled, RecalcJob{
			RequestID: request, SetID: setID, Members: []string{factorID}, Subjects: []string{},
			StartTime: 100, EndTime: 200, Status: RecalcStatusAccepted,
		})
		require.NoError(t, err)
		return job
	}

	// 手工补算（请求号不是 factor-enable- 开头）不会被并入。
	manual, err := s.CreateRecalcJob(ctx, RecalcJob{JobID: "cli-1", RequestID: "cli-1", SetID: "set_a", StartTime: 100, EndTime: 200})
	require.NoError(t, err)
	first := enable("set_a", "f1", "factor-enable-f1")
	require.NotEqual(t, manual.JobID, first.JobID)

	// 别的因子集的任务不会被并入。
	other := enable("set_b", "f1", "factor-enable-b1")
	require.NotEqual(t, first.JobID, other.JobID)

	// 已经被引擎领取的任务不能再改，新成员另建任务。
	pulled, ok, err := s.PullRecalcJob(ctx, "engine-a", pullNow, time.Minute, map[string]bool{"set_b": true}, 0)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, manual.JobID, pulled.JobID, "先领取到最早的手工任务")
	pulledEnable, ok, err := s.PullRecalcJob(ctx, "engine-a", pullNow, time.Minute, map[string]bool{"set_b": true}, 0)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, first.JobID, pulledEnable.JobID)
	second := enable("set_a", "f2", "factor-enable-f2")
	require.NotEqual(t, first.JobID, second.JobID, "运行中的任务不并入")
	third := enable("set_a", "f3", "factor-enable-f3")
	require.Equal(t, second.JobID, third.JobID, "新的排队任务可以再并入后来的成员")
	require.Equal(t, []string{"f2", "f3"}, third.Members)
}
