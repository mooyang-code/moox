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

func seedRecalcParentTask(t *testing.T, s *Store, taskID string) {
	t.Helper()
	task := testTask(taskID, domain.TaskStatusEnabled)
	task.Name = taskID
	task.ResultDatasetID = "dataset_factor_" + taskID
	require.NoError(t, s.CreateTask(context.Background(), task))
}

func TestRecalcJobIDIsIdempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	seedRecalcParentTask(t, s, "task_prices")
	first, err := s.CreateRecalcJob(ctx, RecalcJob{
		JobID: "job-1", TaskID: "task_prices", Members: []string{"a"},
		StartTime: 100, EndTime: 200, Status: RecalcStatusQueued,
	})
	require.NoError(t, err)
	second, err := s.CreateRecalcJob(ctx, RecalcJob{
		JobID: "job-1", TaskID: "task_prices", Members: []string{"b"},
		StartTime: 100, EndTime: 200, Status: RecalcStatusQueued,
	})
	require.NoError(t, err)
	require.Equal(t, first.JobID, second.JobID)
	require.Equal(t, first, second)
}

func TestConcurrentRecalcJobIDIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factor.db")
	firstStore, err := Open(&Options{Path: path, MaxOpenConns: 2})
	require.NoError(t, err)
	secondStore, err := Open(&Options{Path: path, MaxOpenConns: 2})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, firstStore.Close()); require.NoError(t, secondStore.Close()) })
	seedRecalcParentTask(t, firstStore, "set")

	jobs := []RecalcJob{
		{JobID: "job-race", TaskID: "set", Members: []string{"a"}, Subjects: []string{"BTC"}, StartTime: 1, EndTime: 2},
		{JobID: "job-race", TaskID: "set", Members: []string{"a", "b"}, Subjects: []string{"BTC", "ETH"}, StartTime: 1, EndTime: 2},
	}
	results := concurrentCreate(t, firstStore, secondStore, jobs)
	require.NoError(t, results[0].err)
	require.NoError(t, results[1].err)
	require.Equal(t, results[0].job.JobID, results[1].job.JobID)
	require.Equal(t, results[0].job.Members, results[1].job.Members)
	require.Equal(t, results[0].job.Subjects, results[1].job.Subjects)
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
	seedRecalcParentTask(t, s, "task_prices")
	job, err := s.CreateRecalcJob(ctx, RecalcJob{
		JobID: "job-1", TaskID: "task_prices",
		StartTime: 100, EndTime: 500, Status: RecalcStatusQueued,
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

func TestListRecalcJobsFiltersTaskAndStatuses(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	seedRecalcParentTask(t, s, "set")
	seedRecalcParentTask(t, s, "other")
	for _, job := range []RecalcJob{
		{JobID: "job-a", TaskID: "set", StartTime: 1, EndTime: 2, Status: RecalcStatusQueued},
		{JobID: "job-b", TaskID: "set", StartTime: 1, EndTime: 2, Status: RecalcStatusRunning},
		{JobID: "job-c", TaskID: "other", StartTime: 1, EndTime: 2, Status: RecalcStatusRunning},
	} {
		_, err := s.CreateRecalcJob(ctx, job)
		require.NoError(t, err)
	}
	running, err := s.ListRecalcJobs(ctx, RecalcJobFilter{Statuses: []string{RecalcStatusRunning}})
	require.NoError(t, err)
	require.Len(t, running, 2)

	jobs, err := s.ListRecalcJobs(ctx, RecalcJobFilter{TaskID: "set", Statuses: []string{RecalcStatusRunning}})
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	require.Equal(t, "job-b", jobs[0].JobID)

	all, err := s.ListRecalcJobs(ctx, RecalcJobFilter{TaskID: "set"})
	require.NoError(t, err)
	require.Len(t, all, 2)
}

func TestRecalcJobPersistsAcrossStoreReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factor.db")
	s, err := Open(&Options{Path: path})
	require.NoError(t, err)
	seedRecalcParentTask(t, s, "task_prices")
	job, err := s.CreateRecalcJob(context.Background(), RecalcJob{
		JobID: "job-1", TaskID: "task_prices",
		StartTime: 100, EndTime: 200, Status: RecalcStatusQueued,
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

func TestRecalcJobSchemaForeignKeyCascadeAndIndex(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// 外键约束拒绝引用不存在的 c_task_id。
	_, err := s.CreateRecalcJob(ctx, RecalcJob{
		JobID: "job-orphan", TaskID: "task_missing",
		StartTime: 100, EndTime: 200, Status: RecalcStatusQueued,
	})
	require.Error(t, err)

	// 删除因子任务时级联清理其补算作业记录。
	task := testTask("task_cascade", domain.TaskStatusDisabled)
	task.Name, task.ResultDatasetID = "task_cascade", "dataset_factor_cascade"
	require.NoError(t, s.CreateTask(ctx, task))
	_, err = s.CreateRecalcJob(ctx, RecalcJob{
		JobID: "job-cascade", TaskID: "task_cascade",
		StartTime: 100, EndTime: 200, Status: RecalcStatusSucceeded,
	})
	require.NoError(t, err)
	require.NoError(t, s.DeleteTask(ctx, "task_cascade"))
	jobs, err := s.ListRecalcJobs(ctx, RecalcJobFilter{TaskID: "task_cascade"})
	require.NoError(t, err)
	require.Empty(t, jobs)

	// 验证 (c_task_id, c_ctime) 复合索引存在。
	var indexCols []string
	require.NoError(t, s.db.Raw(`SELECT name FROM pragma_index_info('idx_t_factor_recalc_jobs_task_ctime') ORDER BY seqno`).Scan(&indexCols).Error)
	require.Equal(t, []string{"c_task_id", "c_ctime"}, indexCols)
}

func TestPruneRecalcJobsRespectsRetentionPerTaskCapAndActiveJobs(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	seedRecalcParentTask(t, s, "task_a")
	seedRecalcParentTask(t, s, "task_b")

	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	insertJob := func(jobID, taskID, status string, ctime, mtime string) {
		t.Helper()
		_, err := s.CreateRecalcJob(ctx, RecalcJob{
			JobID: jobID, TaskID: taskID,
			StartTime: 100, EndTime: 200, Status: status,
		})
		require.NoError(t, err)
		require.NoError(t, s.db.Exec(
			`UPDATE t_factor_recalc_jobs SET c_ctime = ?, c_mtime = ? WHERE c_job_id = ?`,
			ctime, mtime, jobID,
		).Error)
	}

	// task_a: 2 条过期已完结（覆盖 RFC3339Nano 与 SQLite DATETIME 两种格式）、3 条窗口内已完结、2 条活跃作业（即使过期也不能删）。
	insertJob("a_expired_rfc", "task_a", RecalcStatusSucceeded, now.Add(-40*time.Hour).Format(time.RFC3339Nano), now.Add(-35*time.Hour).Format(time.RFC3339Nano))
	insertJob("a_expired_sql", "task_a", RecalcStatusFailed, now.Add(-38*time.Hour).Format("2006-01-02 15:04:05"), now.Add(-30*time.Hour).Format("2006-01-02 15:04:05"))
	insertJob("a_recent_1", "task_a", RecalcStatusCancelled, now.Add(-10*time.Hour).Format(time.RFC3339Nano), now.Add(-10*time.Hour).Format(time.RFC3339Nano))
	insertJob("a_recent_2", "task_a", RecalcStatusSucceeded, now.Add(-5*time.Hour).Format(time.RFC3339Nano), now.Add(-5*time.Hour).Format(time.RFC3339Nano))
	insertJob("a_recent_3", "task_a", RecalcStatusFailed, now.Add(-1*time.Hour).Format(time.RFC3339Nano), now.Add(-1*time.Hour).Format(time.RFC3339Nano))
	insertJob("a_queued_old", "task_a", RecalcStatusQueued, now.Add(-50*time.Hour).Format(time.RFC3339Nano), now.Add(-50*time.Hour).Format(time.RFC3339Nano))
	insertJob("a_running_old", "task_a", RecalcStatusRunning, now.Add(-45*time.Hour).Format(time.RFC3339Nano), now.Add(-45*time.Hour).Format(time.RFC3339Nano))

	// task_b: 2 条窗口内已完结，应按任务独立保留。
	insertJob("b_recent_1", "task_b", RecalcStatusSucceeded, now.Add(-6*time.Hour).Format(time.RFC3339Nano), now.Add(-6*time.Hour).Format(time.RFC3339Nano))
	insertJob("b_recent_2", "task_b", RecalcStatusCancelled, now.Add(-2*time.Hour).Format(time.RFC3339Nano), now.Add(-2*time.Hour).Format(time.RFC3339Nano))

	deleted, err := s.PruneRecalcJobs(ctx, now, 24*time.Hour, 2)
	require.NoError(t, err)
	require.Equal(t, int64(3), deleted, "应删除 task_a 的 2 条过期已完结和 1 条超出每任务上限的旧已完结作业")

	taskAJobs, err := s.ListRecalcJobs(ctx, RecalcJobFilter{TaskID: "task_a"})
	require.NoError(t, err)
	var taskAIDs []string
	for _, job := range taskAJobs {
		taskAIDs = append(taskAIDs, job.JobID)
	}
	require.ElementsMatch(t, []string{"a_recent_3", "a_recent_2", "a_queued_old", "a_running_old"}, taskAIDs)

	taskBJobs, err := s.ListRecalcJobs(ctx, RecalcJobFilter{TaskID: "task_b"})
	require.NoError(t, err)
	require.Len(t, taskBJobs, 2)

	// 再次执行幂等（不再删除任何记录）。
	deleted, err = s.PruneRecalcJobs(ctx, now, 24*time.Hour, 2)
	require.NoError(t, err)
	require.Zero(t, deleted)
}
