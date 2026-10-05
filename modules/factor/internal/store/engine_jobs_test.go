package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

func seedPullJob(t *testing.T, s *Store, jobID, setID string) RecalcJob {
	t.Helper()
	ctx := context.Background()
	if _, err := s.GetSet(ctx, setID); err != nil {
		require.NoError(t, s.CreateSet(ctx, testSet(setID, domain.SetStatusEnabled)))
	}
	job, err := s.CreateRecalcJob(ctx, RecalcJob{
		JobID: jobID, RequestID: jobID, SetID: setID, StartTime: 100, EndTime: 400,
	})
	require.NoError(t, err)
	return job
}

var pullNow = time.Unix(10_000, 0).UTC()

func TestPullRecalcJobIsAtomic(t *testing.T) {
	s := openTestStore(t)
	seedPullJob(t, s, "job-1", "set_a")

	var wg sync.WaitGroup
	var mu sync.Mutex
	found := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, ok, err := s.PullRecalcJob(context.Background(), fmt.Sprintf("engine-%d", i), pullNow, time.Minute, nil)
			require.NoError(t, err)
			if ok {
				mu.Lock()
				found++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	require.Equal(t, 1, found)
}

func TestPullMarksJobRunningWithLease(t *testing.T) {
	s := openTestStore(t)
	seedPullJob(t, s, "job-1", "set_a")

	job, ok, err := s.PullRecalcJob(context.Background(), "engine-a", pullNow, time.Minute, nil)

	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, RecalcStatusRunning, job.Status)
	require.Equal(t, "engine-a", job.EngineID)
	require.NotEmpty(t, job.LeaseToken)
	require.Equal(t, pullNow.Add(time.Minute).Unix(), job.LeaseExpiresAt)
	require.Equal(t, int64(100), job.ProgressTime, "progress starts at the window start")
}

func TestPullPrefersOldestAccepted(t *testing.T) {
	s := openTestStore(t)
	seedPullJob(t, s, "job-old", "set_a")
	time.Sleep(5 * time.Millisecond)
	seedPullJob(t, s, "job-new", "set_a")

	job, ok, err := s.PullRecalcJob(context.Background(), "engine-a", pullNow, time.Minute, nil)

	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "job-old", job.JobID)
}

func TestPullSkipsLiveLease(t *testing.T) {
	s := openTestStore(t)
	seedPullJob(t, s, "job-1", "set_a")
	_, ok, err := s.PullRecalcJob(context.Background(), "engine-a", pullNow, time.Minute, nil)
	require.NoError(t, err)
	require.True(t, ok)

	_, ok, err = s.PullRecalcJob(context.Background(), "engine-b", pullNow.Add(30*time.Second), time.Minute, nil)

	require.NoError(t, err)
	require.False(t, ok)
}

func TestPullReclaimsExpiredRunningLease(t *testing.T) {
	s := openTestStore(t)
	seedPullJob(t, s, "job-1", "set_a")
	first, ok, err := s.PullRecalcJob(context.Background(), "engine-a", pullNow, time.Minute, nil)
	require.NoError(t, err)
	require.True(t, ok)
	_, err = s.ReportRecalcProgress(context.Background(), first.JobID, first.LeaseToken, 200, RecalcStatusRunning, "", pullNow, time.Minute)
	require.NoError(t, err)

	second, ok, err := s.PullRecalcJob(context.Background(), "engine-b", pullNow.Add(2*time.Minute), time.Minute, nil)

	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, first.JobID, second.JobID)
	require.Equal(t, "engine-b", second.EngineID)
	require.NotEqual(t, first.LeaseToken, second.LeaseToken)
	require.Equal(t, int64(200), second.ProgressTime, "a reclaimed job resumes from its progress")
}

func TestPullSkipsExcludedSets(t *testing.T) {
	s := openTestStore(t)
	seedPullJob(t, s, "job-1", "set_a")

	_, ok, err := s.PullRecalcJob(context.Background(), "engine-a", pullNow, time.Minute, map[string]bool{"set_a": true})

	require.NoError(t, err)
	require.False(t, ok)
}

func TestReportProgressRejectsWrongToken(t *testing.T) {
	s := openTestStore(t)
	seedPullJob(t, s, "job-1", "set_a")
	job, _, err := s.PullRecalcJob(context.Background(), "engine-a", pullNow, time.Minute, nil)
	require.NoError(t, err)

	_, err = s.ReportRecalcProgress(context.Background(), job.JobID, "stale-token", 200, RecalcStatusRunning, "", pullNow, time.Minute)

	require.ErrorIs(t, err, ErrConflict)
}

func TestReportProgressExtendsLease(t *testing.T) {
	s := openTestStore(t)
	seedPullJob(t, s, "job-1", "set_a")
	job, _, err := s.PullRecalcJob(context.Background(), "engine-a", pullNow, time.Minute, nil)
	require.NoError(t, err)
	later := pullNow.Add(50 * time.Second)

	updated, err := s.ReportRecalcProgress(context.Background(), job.JobID, job.LeaseToken, 200, RecalcStatusRunning, "", later, time.Minute)

	require.NoError(t, err)
	require.Equal(t, int64(200), updated.ProgressTime)
	require.Equal(t, later.Add(time.Minute).Unix(), updated.LeaseExpiresAt)
}

func TestReportProgressCompletesJob(t *testing.T) {
	s := openTestStore(t)
	seedPullJob(t, s, "job-1", "set_a")
	job, _, err := s.PullRecalcJob(context.Background(), "engine-a", pullNow, time.Minute, nil)
	require.NoError(t, err)

	done, err := s.ReportRecalcProgress(context.Background(), job.JobID, job.LeaseToken, 400, RecalcStatusSucceeded, "degraded: failed_subjects=1", pullNow, time.Minute)

	require.NoError(t, err)
	require.Equal(t, RecalcStatusSucceeded, done.Status)
	require.Equal(t, "degraded: failed_subjects=1", done.Error)
	_, ok, err := s.PullRecalcJob(context.Background(), "engine-b", pullNow.Add(time.Hour), time.Minute, nil)
	require.NoError(t, err)
	require.False(t, ok, "a finished job is never pulled again")
}

func TestReportProgressReturnsCancelled(t *testing.T) {
	s := openTestStore(t)
	seedPullJob(t, s, "job-1", "set_a")
	job, _, err := s.PullRecalcJob(context.Background(), "engine-a", pullNow, time.Minute, nil)
	require.NoError(t, err)
	_, err = s.UpdateRecalcJob(context.Background(), job.JobID, func(current *RecalcJob) error {
		current.Status = RecalcStatusCancelled
		return nil
	})
	require.NoError(t, err)

	current, err := s.ReportRecalcProgress(context.Background(), job.JobID, job.LeaseToken, 200, RecalcStatusRunning, "", pullNow, time.Minute)

	require.NoError(t, err)
	require.Equal(t, RecalcStatusCancelled, current.Status)
	require.Equal(t, int64(100), current.ProgressTime)
}

func TestReportProgressRejectsRegression(t *testing.T) {
	s := openTestStore(t)
	seedPullJob(t, s, "job-1", "set_a")
	job, _, err := s.PullRecalcJob(context.Background(), "engine-a", pullNow, time.Minute, nil)
	require.NoError(t, err)
	_, err = s.ReportRecalcProgress(context.Background(), job.JobID, job.LeaseToken, 300, RecalcStatusRunning, "", pullNow, time.Minute)
	require.NoError(t, err)

	_, err = s.ReportRecalcProgress(context.Background(), job.JobID, job.LeaseToken, 200, RecalcStatusRunning, "", pullNow, time.Minute)

	require.ErrorIs(t, err, ErrConflict)
}

func TestReportProgressRejectsUnknownStatus(t *testing.T) {
	s := openTestStore(t)
	seedPullJob(t, s, "job-1", "set_a")
	job, _, err := s.PullRecalcJob(context.Background(), "engine-a", pullNow, time.Minute, nil)
	require.NoError(t, err)

	_, err = s.ReportRecalcProgress(context.Background(), job.JobID, job.LeaseToken, 200, RecalcStatusCancelled, "", pullNow, time.Minute)

	require.Error(t, err)
}
