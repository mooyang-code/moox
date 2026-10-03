package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

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

func TestListRecalcJobsFiltersStatuses(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for _, job := range []RecalcJob{
		{JobID: "job-a", RequestID: "req-a", SetID: "set", StartTime: 1, EndTime: 2, Status: RecalcStatusAccepted},
		{JobID: "job-b", RequestID: "req-b", SetID: "set", StartTime: 1, EndTime: 2, Status: RecalcStatusRunning},
	} {
		_, err := s.CreateRecalcJob(ctx, job)
		require.NoError(t, err)
	}
	jobs, err := s.ListRecalcJobs(ctx, RecalcStatusRunning)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Equal(t, "job-b", jobs[0].JobID)
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
