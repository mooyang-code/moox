package store

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/stretchr/testify/require"
)

func seedPullJob(t *testing.T, s *Store, jobID, taskID string) RecalcJob {
	t.Helper()
	ctx := context.Background()
	if _, err := s.GetTask(ctx, taskID); err != nil {
		// 每个因子任务的数据源和结果数据集必须各不相同（数据库对 空间+数据源+频率 有唯一约束）。
		set := testTask(taskID, domain.TaskStatusEnabled)
		set.SourceDatasetID, set.ResultDatasetID = "dataset_"+taskID, "dataset_factor_"+taskID
		require.NoError(t, s.CreateTask(ctx, set))
	}
	job, err := s.CreateRecalcJob(ctx, RecalcJob{
		JobID: jobID, RequestID: jobID, TaskID: taskID, StartTime: 100, EndTime: 400,
	})
	require.NoError(t, err)
	return job
}

var pullNow = time.Unix(10_000, 0).UTC()

// 并发领取同一条待领取作业：都拿到同一条（第一个置为运行中，之后的都拿回这条运行中的），不会出现两条运行中。
func TestPullRecalcJobKeepsOneRunning(t *testing.T) {
	s := openTestStore(t)
	seedPullJob(t, s, "job-1", "task_a")
	seedPullJob(t, s, "job-2", "task_b")

	var wg sync.WaitGroup
	var mu sync.Mutex
	got := map[string]int{}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job, ok, err := s.PullRecalcJob(context.Background(), pullNow, nil)
			require.NoError(t, err)
			require.True(t, ok)
			mu.Lock()
			got[job.JobID]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	require.Equal(t, map[string]int{"job-1": 8}, got)
	running, err := s.ListRecalcJobs(context.Background(), RecalcJobFilter{Statuses: []string{RecalcStatusRunning}})
	require.NoError(t, err)
	require.Len(t, running, 1)
}

func TestPullMarksJobRunning(t *testing.T) {
	s := openTestStore(t)
	seedPullJob(t, s, "job-1", "task_a")

	job, ok, err := s.PullRecalcJob(context.Background(), pullNow, nil)

	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, RecalcStatusRunning, job.Status)
	require.Equal(t, int64(100), job.ProgressTime, "progress starts at the window start")
}

func TestPullPrefersOldestAccepted(t *testing.T) {
	s := openTestStore(t)
	seedPullJob(t, s, "job-old", "task_a")
	time.Sleep(5 * time.Millisecond)
	seedPullJob(t, s, "job-new", "task_a")

	job, ok, err := s.PullRecalcJob(context.Background(), pullNow, nil)

	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "job-old", job.JobID)
}

// 运行中的作业先于待领取的返回，并从已上报的进度续跑（引擎重启、管理端重启、上报失败放弃后都这样拿回）。
func TestPullReturnsTheRunningJobFirst(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	seedPullJob(t, s, "job-1", "task_a")
	first, ok, err := s.PullRecalcJob(ctx, pullNow, nil)
	require.NoError(t, err)
	require.True(t, ok)
	_, err = s.ReportRecalcProgress(ctx, first.JobID, 200, RecalcStatusRunning, "", nil, pullNow)
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond)
	seedPullJob(t, s, "job-2", "task_a")

	again, ok, err := s.PullRecalcJob(ctx, pullNow.Add(time.Hour), nil)

	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, first.JobID, again.JobID)
	require.Equal(t, int64(200), again.ProgressTime, "a running job resumes from its progress")
	other, err := s.GetRecalcJob(ctx, "job-2")
	require.NoError(t, err)
	require.Equal(t, RecalcStatusAccepted, other.Status, "有运行中的作业时不发新的")
}

func TestPullSkipsExcludedTasks(t *testing.T) {
	s := openTestStore(t)
	seedPullJob(t, s, "job-1", "task_a")

	_, ok, err := s.PullRecalcJob(context.Background(), pullNow, map[string]bool{"task_a": true})

	require.NoError(t, err)
	require.False(t, ok)
}

// 运行中的作业所在任务暂时不可领取（结果数据集还没就绪）：退回待领取、进度保留，改发别的任务的作业，仍然只有一条运行中。
func TestPullReturnsAnExcludedRunningJobToTheQueue(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	seedPullJob(t, s, "job-a", "task_a")
	first, _, err := s.PullRecalcJob(ctx, pullNow, nil)
	require.NoError(t, err)
	_, err = s.ReportRecalcProgress(ctx, first.JobID, 200, RecalcStatusRunning, "", nil, pullNow)
	require.NoError(t, err)
	seedPullJob(t, s, "job-b", "task_b")

	next, ok, err := s.PullRecalcJob(ctx, pullNow, map[string]bool{"task_a": true})

	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "job-b", next.JobID)
	returned, err := s.GetRecalcJob(ctx, "job-a")
	require.NoError(t, err)
	require.Equal(t, RecalcStatusAccepted, returned.Status)
	require.Equal(t, int64(200), returned.ProgressTime)
	running, err := s.ListRecalcJobs(ctx, RecalcJobFilter{Statuses: []string{RecalcStatusRunning}})
	require.NoError(t, err)
	require.Len(t, running, 1)
}

func TestReportProgressRejectsAJobThatIsNotRunning(t *testing.T) {
	s := openTestStore(t)
	seedPullJob(t, s, "job-1", "task_a")

	_, err := s.ReportRecalcProgress(context.Background(), "job-1", 200, RecalcStatusRunning, "", nil, pullNow)

	require.ErrorIs(t, err, ErrConflict)
}

func TestReportProgressCompletesJob(t *testing.T) {
	s := openTestStore(t)
	seedPullJob(t, s, "job-1", "task_a")
	job, _, err := s.PullRecalcJob(context.Background(), pullNow, nil)
	require.NoError(t, err)

	done, err := s.ReportRecalcProgress(context.Background(), job.JobID, 400, RecalcStatusSucceeded, "degraded: failed_subjects=1", nil, pullNow)

	require.NoError(t, err)
	require.Equal(t, RecalcStatusSucceeded, done.Status)
	require.Equal(t, "degraded: failed_subjects=1", done.Error)
	_, ok, err := s.PullRecalcJob(context.Background(), pullNow.Add(time.Hour), nil)
	require.NoError(t, err)
	require.False(t, ok, "a finished job is never pulled again")
}

func TestReportProgressReturnsCancelled(t *testing.T) {
	s := openTestStore(t)
	seedPullJob(t, s, "job-1", "task_a")
	job, _, err := s.PullRecalcJob(context.Background(), pullNow, nil)
	require.NoError(t, err)
	_, err = s.UpdateRecalcJob(context.Background(), job.JobID, func(current *RecalcJob) error {
		current.Status = RecalcStatusCancelled
		return nil
	})
	require.NoError(t, err)

	current, err := s.ReportRecalcProgress(context.Background(), job.JobID, 200, RecalcStatusRunning, "", nil, pullNow)

	require.NoError(t, err)
	require.Equal(t, RecalcStatusCancelled, current.Status)
	require.Equal(t, int64(100), current.ProgressTime)
}

func TestReportProgressRejectsRegression(t *testing.T) {
	s := openTestStore(t)
	seedPullJob(t, s, "job-1", "task_a")
	job, _, err := s.PullRecalcJob(context.Background(), pullNow, nil)
	require.NoError(t, err)
	_, err = s.ReportRecalcProgress(context.Background(), job.JobID, 300, RecalcStatusRunning, "", nil, pullNow)
	require.NoError(t, err)

	_, err = s.ReportRecalcProgress(context.Background(), job.JobID, 200, RecalcStatusRunning, "", nil, pullNow)

	require.ErrorIs(t, err, ErrConflict)
}

func TestReportProgressRejectsUnknownStatus(t *testing.T) {
	s := openTestStore(t)
	seedPullJob(t, s, "job-1", "task_a")
	job, _, err := s.PullRecalcJob(context.Background(), pullNow, nil)
	require.NoError(t, err)

	_, err = s.ReportRecalcProgress(context.Background(), job.JobID, 200, RecalcStatusCancelled, "", nil, pullNow)

	require.Error(t, err)
}
