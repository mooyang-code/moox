package marketfetch

import (
	"context"
	"errors"
	"testing"
	"time"

	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/stretchr/testify/require"
)

// backfillHarness 搭好一个只有补采函数可用的对账器：代码包按名称解析，云端作业可以停在运行中。
func backfillHarness(t *testing.T) (*planHarness, *PlanReconciler, string) {
	t.Helper()
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	r.Deploy.PackageID, r.Deploy.PackageName = "", "moox-collector-crypto-plan"
	r.JobWait, r.PollInterval = time.Millisecond, time.Millisecond
	h.cloud.packages = []string{"pkg-v1"}
	return h, r, BackfillFunctionName("crypto", "spot", "ap-nanjing")
}

func TestBackfillCreateStillRunningKeepsTheLeaseAndIsNotResubmitted(t *testing.T) {
	h, r, name := backfillHarness(t)
	h.cloud.runningAll = true

	_, err := r.EnsureBackfillFunction(context.Background(), "spot")
	require.ErrorIs(t, err, errBackfillJobRunning)
	require.Zero(t, h.cloud.releases, "作业还没结束，租约一释放就立即过期，排队中的作业开始时会因租约过期而失败")
	require.Equal(t, 1, h.cloud.leaseCalls)
	require.Equal(t, 1, h.cloud.submits)
	require.Contains(t, r.backfillJobs, name)
	require.Equal(t, "create", r.backfillJobs[name].kind)

	// 作业仍在运行时再来一次：不重复提交，不重新申请租约，只续租。
	_, err = r.EnsureBackfillFunction(context.Background(), "spot")
	require.ErrorIs(t, err, errBackfillJobRunning)
	require.Equal(t, 1, h.cloud.submits)
	require.Equal(t, 1, h.cloud.leaseCalls)
	require.Zero(t, h.cloud.releases)
	require.Positive(t, h.cloud.renews)

	// 作业结束：下一次调用结算在途作业、释放租约，并按云端实际状态确认函数已就绪，不再创建第二次。
	h.cloud.runningAll = false
	got, err := r.EnsureBackfillFunction(context.Background(), "spot")
	require.NoError(t, err)
	require.Equal(t, name, got)
	require.Equal(t, 1, h.cloud.releases)
	require.Empty(t, r.backfillJobs)
	require.Equal(t, 1, h.cloud.submits)
	require.Equal(t, []string{name}, h.cloud.created)
}

func TestBackfillJobThatFinishesInTimeReleasesTheLease(t *testing.T) {
	h, r, name := backfillHarness(t)
	got, err := r.EnsureBackfillFunction(context.Background(), "spot")
	require.NoError(t, err)
	require.Equal(t, name, got)
	require.Equal(t, 1, h.cloud.releases)
	require.Empty(t, r.backfillJobs)
	require.Nil(t, r.backfillLease)
}

func TestBackfillSubmitFailureReleasesTheLease(t *testing.T) {
	h, r, _ := backfillHarness(t)
	h.cloud.submitErr = nil
	h.cloud.failNodes = map[string]bool{}
	// 提交本身失败（没有作业产生）：租约立即释放，不留在途记录。
	r.Cloud = &submitFailingCloud{fakePlanCloud: h.cloud}
	_, err := r.EnsureBackfillFunction(context.Background(), "spot")
	require.Error(t, err)
	require.NotErrorIs(t, err, errBackfillJobRunning)
	require.Equal(t, 1, h.cloud.releases)
	require.Empty(t, r.backfillJobs)
}

type submitFailingCloud struct{ *fakePlanCloud }

func (c *submitFailingCloud) SubmitCreateNodes(context.Context, string, []*cloudnodepb.NodeCreateItem) (string, error) {
	return "", errors.New("submit create nodes: gateway unavailable")
}

func TestBackfillUpgradeStillRunningKeepsTheLease(t *testing.T) {
	h, r, name := backfillHarness(t)
	h.cloud.backfill = map[string]scfinvoker.Node{name: {NodeID: name, PackageID: "pkg-v1", Metadata: testBackfillMetadata()}}
	_, err := r.EnsureBackfillFunction(context.Background(), "spot")
	require.NoError(t, err)

	h.cloud.packages = append(h.cloud.packages, "pkg-v2")
	h.cloud.runningAll = true
	h.now = h.now.Add(time.Minute)
	_, err = r.EnsureBackfillFunction(context.Background(), "spot")
	require.ErrorIs(t, err, errBackfillJobRunning)
	require.Equal(t, "upgrade", r.backfillJobs[name].kind)
	require.Equal(t, []string{name}, h.cloud.upgraded)
	releasesWhileRunning := h.cloud.releases

	_, err = r.EnsureBackfillFunction(context.Background(), "spot")
	require.ErrorIs(t, err, errBackfillJobRunning)
	require.Len(t, h.cloud.upgraded, 1, "在途期间不重复升级")
	require.Equal(t, releasesWhileRunning, h.cloud.releases)

	h.cloud.runningAll = false
	_, err = r.EnsureBackfillFunction(context.Background(), "spot")
	require.NoError(t, err)
	require.Len(t, h.cloud.upgraded, 1)
	require.Empty(t, r.backfillJobs)
	require.Equal(t, releasesWhileRunning+1, h.cloud.releases)
}

func TestBackfillStaleReservationCleanupStillRunningKeepsTheLease(t *testing.T) {
	h, r, name := backfillHarness(t)
	// 创建失败后遗留的预留，已超过等待时长：补采路径要先走受租约保护的删除作业。
	h.cloud.unready[name] = scfinvoker.Node{NodeID: name, FunctionName: name, TriggerType: "invoke", ModifiedAt: h.now.Add(-time.Hour)}
	h.cloud.runningAll = true

	_, err := r.EnsureBackfillFunction(context.Background(), "spot")
	require.Error(t, err)
	require.Equal(t, []string{name}, h.cloud.deleted)
	require.Equal(t, "cleanup", r.backfillJobs[name].kind)
	require.Zero(t, h.cloud.releases, "清理作业还在运行：保留租约，不重复提交")

	_, err = r.EnsureBackfillFunction(context.Background(), "spot")
	require.ErrorIs(t, err, errBackfillJobRunning)
	require.Len(t, h.cloud.deleted, 1)

	// 清理结束后继续创建；两个作业的租约在各自结束后释放。
	h.cloud.runningAll = false
	delete(h.cloud.unready, name)
	got, err := r.EnsureBackfillFunction(context.Background(), "spot")
	require.NoError(t, err)
	require.Equal(t, name, got)
	require.Empty(t, r.backfillJobs)
	require.Nil(t, r.backfillLease)
}

func TestBackfillInFlightQueryErrorKeepsTheJobTracked(t *testing.T) {
	h, r, name := backfillHarness(t)
	h.cloud.runningAll = true
	_, err := r.EnsureBackfillFunction(context.Background(), "spot")
	require.ErrorIs(t, err, errBackfillJobRunning)

	// 读取作业结果出错：不能当成作业已结束，也不能丢掉在途记录。
	h.cloud.runningAll = false
	h.cloud.batchErr = errors.New("get batch results: gateway unavailable")
	_, err = r.EnsureBackfillFunction(context.Background(), "spot")
	require.ErrorContains(t, err, "gateway unavailable")
	require.Contains(t, r.backfillJobs, name)
	require.Zero(t, h.cloud.releases)
	require.Equal(t, 1, h.cloud.submits)
}

func TestBackfillRenewFailureWhileSettlingDoesNotAcquireANewLease(t *testing.T) {
	h, r, _ := backfillHarness(t)
	h.cloud.runningAll = true
	_, err := r.EnsureBackfillFunction(context.Background(), "spot")
	require.ErrorIs(t, err, errBackfillJobRunning)
	acquired := h.cloud.leaseCalls

	// 结算在途作业时续租失败（租约已过期或被取代）：只记日志，不去申请新租约，也不会对同一函数重复提交。
	h.cloud.renewErr = scfinvoker.ErrCollectorPublishLeaseStale
	_, err = r.EnsureBackfillFunction(context.Background(), "spot")
	require.ErrorIs(t, err, errBackfillJobRunning)
	require.Equal(t, 1, h.cloud.submits)
	require.Equal(t, acquired, h.cloud.leaseCalls, "结算在途作业时只续租，失败只记日志，不去申请新租约")
}

func TestBackfillLeaseHeldByRealtimeIsRetriedLaterWithoutTrackingAJob(t *testing.T) {
	h, r, _ := backfillHarness(t)
	h.cloud.acquireErr = heldError()
	_, err := r.EnsureBackfillFunction(context.Background(), "spot")
	require.ErrorIs(t, err, scfinvoker.ErrCollectorPublishLeaseHeld)
	require.Empty(t, r.backfillJobs)
	require.Zero(t, h.cloud.submits)
}
