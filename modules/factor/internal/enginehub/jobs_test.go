package enginehub

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/stretchr/testify/require"
)

func seedJob(t *testing.T, db *store.Store, jobID, taskID string, factorIDs ...string) {
	t.Helper()
	_, err := db.CreateRecalcJob(context.Background(), store.RecalcJob{
		JobID: jobID, RequestID: jobID, TaskID: taskID, Members: factorIDs, StartTime: 0, EndTime: 600,
	})
	require.NoError(t, err)
}

func leaseEngineA(t *testing.T, hub *Hub) {
	t.Helper()
	_, err := hub.Heartbeat(context.Background(), engineA, domain.EngineStatus{})
	require.NoError(t, err)
}

func TestPullRequiresEngineLease(t *testing.T) {
	hub, db, _ := newTestHub(t)
	seedTask(t, db, "task_a", map[string]string{"bias": domain.MemberStatusEnabled})
	hub.SetResultReady("task_a", true)
	seedJob(t, db, "job-1", "task_a")

	_, _, _, err := hub.Pull(context.Background(), engineA)

	require.ErrorIs(t, err, ErrLeaseConflict)
}

func TestPullReturnsJobWithEnabledSelection(t *testing.T) {
	hub, db, _ := newTestHub(t)
	seedTask(t, db, "task_a", map[string]string{"bias": domain.MemberStatusEnabled, "cci": domain.MemberStatusEnabled, "zf": domain.MemberStatusDisabled})
	hub.SetResultReady("task_a", true)
	seedJob(t, db, "job-1", "task_a", "cci")
	leaseEngineA(t, hub)

	job, set, found, err := hub.Pull(context.Background(), engineA)

	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "job-1", job.JobID)
	require.Equal(t, engineA.EngineID, job.EngineID)
	require.NotEmpty(t, job.LeaseToken)
	require.Equal(t, "task_a", set.Set.TaskID)
	require.True(t, set.ResultReady)
	require.Len(t, set.Members, 1)
	require.Equal(t, "cci", set.Members[0].Factor.FactorID)
	require.NotEmpty(t, set.Members[0].Factor.Spec)
}

func TestPullDefaultsToAllEnabledMembers(t *testing.T) {
	hub, db, _ := newTestHub(t)
	seedTask(t, db, "task_a", map[string]string{"bias": domain.MemberStatusEnabled, "cci": domain.MemberStatusEnabled, "zf": domain.MemberStatusDisabled})
	hub.SetResultReady("task_a", true)
	seedJob(t, db, "job-1", "task_a")
	leaseEngineA(t, hub)

	_, set, found, err := hub.Pull(context.Background(), engineA)

	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, set.Members, 2)
}

func TestPullFailsJobWhenSelectedFactorDisabled(t *testing.T) {
	hub, db, _ := newTestHub(t)
	seedTask(t, db, "task_a", map[string]string{"bias": domain.MemberStatusEnabled, "zf": domain.MemberStatusDisabled})
	hub.SetResultReady("task_a", true)
	seedJob(t, db, "job-1", "task_a", "zf")
	leaseEngineA(t, hub)

	_, _, found, err := hub.Pull(context.Background(), engineA)

	require.NoError(t, err)
	require.False(t, found)
	job, err := db.GetRecalcJob(context.Background(), "job-1")
	require.NoError(t, err)
	require.Equal(t, store.RecalcStatusFailed, job.Status)
	require.Contains(t, job.Error, "not enabled in set")
}

func TestPullFailsJobOfDisabledTaskAndContinues(t *testing.T) {
	hub, db, _ := newTestHub(t)
	seedTask(t, db, "task_off", map[string]string{"bias": domain.MemberStatusEnabled})
	require.NoError(t, db.SetTaskStatus(context.Background(), "task_off", domain.TaskStatusEnabled, domain.TaskStatusDisabled))
	seedTask(t, db, "task_on", map[string]string{"bias": domain.MemberStatusEnabled})
	hub.SetResultReady("task_on", true)
	seedJob(t, db, "job-off", "task_off")
	time.Sleep(5 * time.Millisecond)
	seedJob(t, db, "job-on", "task_on")
	leaseEngineA(t, hub)

	job, _, found, err := hub.Pull(context.Background(), engineA)

	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "job-on", job.JobID)
	off, err := db.GetRecalcJob(context.Background(), "job-off")
	require.NoError(t, err)
	require.Equal(t, store.RecalcStatusFailed, off.Status)
}

func TestPullSkipsTasksNotResultReady(t *testing.T) {
	hub, db, _ := newTestHub(t)
	seedTask(t, db, "task_a", map[string]string{"bias": domain.MemberStatusEnabled})
	seedJob(t, db, "job-1", "task_a")
	leaseEngineA(t, hub)

	_, _, found, err := hub.Pull(context.Background(), engineA)

	require.NoError(t, err)
	require.False(t, found)
	job, err := db.GetRecalcJob(context.Background(), "job-1")
	require.NoError(t, err)
	require.Equal(t, store.RecalcStatusAccepted, job.Status, "the job waits until the set is ready")
}

func TestReportRenewsJobLease(t *testing.T) {
	hub, db, clock := newTestHub(t)
	seedTask(t, db, "task_a", map[string]string{"bias": domain.MemberStatusEnabled})
	hub.SetResultReady("task_a", true)
	seedJob(t, db, "job-1", "task_a")
	leaseEngineA(t, hub)
	job, _, _, err := hub.Pull(context.Background(), engineA)
	require.NoError(t, err)
	clock.Advance(50 * time.Second)
	leaseEngineA(t, hub)

	updated, err := hub.Report(context.Background(), engineA, job.JobID, job.LeaseToken, time.Unix(300, 0), store.RecalcStatusRunning, "")

	require.NoError(t, err)
	require.Equal(t, int64(300), updated.ProgressTime)
	require.Equal(t, clock.now.Add(time.Minute).Unix(), updated.LeaseExpiresAt)
}

func TestReportRejectsEngineWithoutLease(t *testing.T) {
	hub, db, clock := newTestHub(t)
	seedTask(t, db, "task_a", map[string]string{"bias": domain.MemberStatusEnabled})
	hub.SetResultReady("task_a", true)
	seedJob(t, db, "job-1", "task_a")
	leaseEngineA(t, hub)
	job, _, _, err := hub.Pull(context.Background(), engineA)
	require.NoError(t, err)
	clock.Advance(time.Minute)
	_, err = hub.Heartbeat(context.Background(), engineB, domain.EngineStatus{})
	require.NoError(t, err)

	_, err = hub.Report(context.Background(), engineA, job.JobID, job.LeaseToken, time.Unix(300, 0), store.RecalcStatusRunning, "")

	require.ErrorIs(t, err, ErrLeaseConflict)
}

// 同一 engine_id 的新进程（不同 boot_id）接管后，旧进程的 Pull/Report 不能再续租或完成任务。
func TestOldBootOfSameEngineCannotPullOrReportAfterTakeover(t *testing.T) {
	hub, db, clock := newTestHub(t)
	seedTask(t, db, "task_a", map[string]string{"bias": domain.MemberStatusEnabled})
	hub.SetResultReady("task_a", true)
	seedJob(t, db, "job-1", "task_a")
	leaseEngineA(t, hub)
	job, _, _, err := hub.Pull(context.Background(), engineA)
	require.NoError(t, err)
	clock.Advance(46 * time.Second)
	restarted := engineA
	restarted.BootID = engineA.BootID + "-restarted"
	_, err = hub.Heartbeat(context.Background(), restarted, domain.EngineStatus{})
	require.NoError(t, err)

	_, err = hub.Report(context.Background(), engineA, job.JobID, job.LeaseToken, time.Unix(300, 0), store.RecalcStatusRunning, "")
	require.ErrorIs(t, err, ErrLeaseConflict, "旧进程的 Report 必须被拒绝")
	_, _, _, err = hub.Pull(context.Background(), engineA)
	require.ErrorIs(t, err, ErrLeaseConflict, "旧进程的 Pull 必须被拒绝")
	_, err = hub.Report(context.Background(), restarted, job.JobID, job.LeaseToken, time.Unix(300, 0), store.RecalcStatusRunning, "")
	require.NotErrorIs(t, err, ErrLeaseConflict, "新进程持有租约")
}

func TestReportStopsJobWhenMemberOrTaskDisabledBetweenChunks(t *testing.T) {
	for name, disable := range map[string]func(*store.Store){
		"member": func(db *store.Store) {
			require.NoError(t, db.SetMemberStatus(context.Background(), "task_a", "bias", domain.MemberStatusEnabled, domain.MemberStatusDisabled))
		},
		"set": func(db *store.Store) {
			require.NoError(t, db.SetTaskStatus(context.Background(), "task_a", domain.TaskStatusEnabled, domain.TaskStatusDisabled))
		},
	} {
		t.Run(name, func(t *testing.T) {
			hub, db, _ := newTestHub(t)
			seedTask(t, db, "task_a", map[string]string{"bias": domain.MemberStatusEnabled})
			hub.SetResultReady("task_a", true)
			seedJob(t, db, "job-1", "task_a")
			leaseEngineA(t, hub)
			job, _, _, err := hub.Pull(context.Background(), engineA)
			require.NoError(t, err)

			disable(db) // 第一块运行期间停用

			updated, err := hub.Report(context.Background(), engineA, job.JobID, job.LeaseToken, time.Unix(300, 0), store.RecalcStatusRunning, "")
			require.NoError(t, err)
			require.Equal(t, store.RecalcStatusFailed, updated.Status, "后续分块必须停止")
			require.NotEmpty(t, updated.Error)
		})
	}
}

// 同时只跑一个补算作业：补算整窗口读取源数据，并发的回填会让 DataNode 读不动在线计算。
func TestPullRunsOneRecalcJobAtATimeByDefault(t *testing.T) {
	hub, db, clock := newTestHub(t)
	for _, taskID := range []string{"task_a", "task_b"} {
		seedTask(t, db, taskID, map[string]string{"bias": domain.MemberStatusEnabled})
		hub.SetResultReady(taskID, true)
		seedJob(t, db, "job-"+taskID, taskID)
	}
	leaseEngineA(t, hub)
	require.Equal(t, DefaultMaxRunningJobs, hub.maxRunningJobs)

	first, _, found, err := hub.Pull(context.Background(), engineA)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "job-task_a", first.JobID)

	_, _, found, err = hub.Pull(context.Background(), engineA)
	require.NoError(t, err)
	require.False(t, found, "第一个任务还在运行，不再发出第二个")

	// 第一个任务结束后才轮到下一个。
	_, err = hub.Report(context.Background(), engineA, first.JobID, first.LeaseToken, time.Unix(600, 0), store.RecalcStatusSucceeded, "")
	require.NoError(t, err)
	second, _, found, err := hub.Pull(context.Background(), engineA)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "job-task_b", second.JobID)

	// 显式放开上限后可以并行。
	hub.maxRunningJobs = 0
	seedTask(t, db, "task_c", map[string]string{"bias": domain.MemberStatusEnabled})
	hub.SetResultReady("task_c", true)
	seedJob(t, db, "job-task_c", "task_c")
	clock.Advance(time.Second)
	_, _, found, err = hub.Pull(context.Background(), engineA)
	require.NoError(t, err)
	require.True(t, found)
}
