package marketfetch

import (
	"context"
	"errors"
	"testing"
	"time"

	cloudnodepb "github.com/mooyang-code/moox/modules/cloudfunction/proto/cloudfunctiongen"
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

func TestBackfillTransientRenewFailureKeepsTheStillValidLease(t *testing.T) {
	h, r, _ := backfillHarness(t)
	h.cloud.runningAll = true
	_, err := r.EnsureBackfillFunction(context.Background(), "spot")
	require.ErrorIs(t, err, errBackfillJobRunning)
	acquired := h.cloud.leaseCalls
	lease := r.backfillLease
	require.NotNil(t, lease)

	// 续租遇到通信错误：租约可能仍然有效，丢掉句柄后 Admin 会拒绝重新申请（租约未过期），在途作业也失去续租保护。
	h.cloud.renewErr = errors.New("rpc: response lost")
	_, err = r.ensureBackfillLease(context.Background())
	require.ErrorContains(t, err, "response lost")
	require.Same(t, lease, r.backfillLease, "通信错误时保留句柄")
	require.Equal(t, acquired, h.cloud.leaseCalls, "不去申请新租约")

	// 恢复后继续续租同一个租约。
	h.cloud.renewErr = nil
	got, err := r.ensureBackfillLease(context.Background())
	require.NoError(t, err)
	require.Equal(t, lease.LeaseID, got.LeaseID)

	// 明确得知租约已过期或被取代：丢弃并重新申请。
	h.cloud.renewErr = scfinvoker.ErrCollectorPublishLeaseStale
	fresh, err := r.ensureBackfillLease(context.Background())
	require.NoError(t, err)
	require.Equal(t, acquired+1, h.cloud.leaseCalls)
	require.NotNil(t, fresh)
}

func TestBackfillSettlingKeepsTheLeaseAliveEvenWhenTheJobQueryFails(t *testing.T) {
	h, r, name := backfillHarness(t)
	h.cloud.runningAll = true
	_, err := r.EnsureBackfillFunction(context.Background(), "spot")
	require.ErrorIs(t, err, errBackfillJobRunning)
	renewsBefore := h.cloud.renews

	// 作业结果持续读不到：作业仍可能在运行，每次重试都要续租，否则超过租约 TTL 后租约过期。
	h.cloud.runningAll = false
	h.cloud.batchErr = errors.New("get batch results: gateway unavailable")
	for i := 0; i < 3; i++ {
		_, err = r.EnsureBackfillFunction(context.Background(), "spot")
		require.ErrorContains(t, err, "gateway unavailable")
	}
	require.Equal(t, renewsBefore+3, h.cloud.renews)
	require.Contains(t, r.backfillJobs, name)
	require.NotNil(t, r.backfillLease)
	require.Zero(t, h.cloud.releases)
}

func TestBackfillGivesUpOnAJobThatNeverReachesATerminalState(t *testing.T) {
	// 放弃之后一切以云端实际状态为准：函数已经存在就直接就绪，不重复提交；函数不存在才重新创建。
	for name, tc := range map[string]struct {
		functionExists bool
		wantSubmits    int
	}{
		"the function exists in the cloud":     {functionExists: true, wantSubmits: 1},
		"the function is missing in the cloud": {functionExists: false, wantSubmits: 2},
	} {
		t.Run(name, func(t *testing.T) {
			h, r, fn := backfillHarness(t)
			h.cloud.runningAll = true
			_, err := r.EnsureBackfillFunction(context.Background(), "spot")
			require.ErrorIs(t, err, errBackfillJobRunning)
			first := r.backfillJobs[fn].id
			if !tc.functionExists {
				delete(h.cloud.backfill, fn) // 作业记录丢失，函数也没有创建出来
			}

			// 放弃期限之内：一直挡住，不重复提交。
			h.now = h.now.Add(backfillJobGiveUpAge - time.Minute)
			_, err = r.EnsureBackfillFunction(context.Background(), "spot")
			require.ErrorIs(t, err, errBackfillJobRunning)
			require.Equal(t, 1, h.cloud.submits)

			// 超过 CloudNode 单项预算（16 分钟）很久仍无法确认终态：不再等，释放租约、移除在途记录，按云端实际状态判断；
			// 旧作业即使还活着，也会被新租约的围栏令牌隔离。
			h.now = h.now.Add(2 * time.Minute)
			got, err := r.EnsureBackfillFunction(context.Background(), "spot")
			require.Equal(t, tc.wantSubmits, h.cloud.submits)
			require.Equal(t, 1, h.cloud.releases, "放弃旧作业时释放了它的租约")
			if tc.functionExists {
				require.NoError(t, err)
				require.Equal(t, fn, got)
				require.Empty(t, r.backfillJobs)
			} else {
				require.ErrorIs(t, err, errBackfillJobRunning, "重新提交的新作业同样停在运行中")
				require.NotEqual(t, first, r.backfillJobs[fn].id)
			}
		})
	}
}

func TestBackfillLeaseHeldByRealtimeIsRetriedLaterWithoutTrackingAJob(t *testing.T) {
	h, r, _ := backfillHarness(t)
	h.cloud.acquireErr = heldError()
	_, err := r.EnsureBackfillFunction(context.Background(), "spot")
	require.ErrorIs(t, err, scfinvoker.ErrCollectorPublishLeaseHeld)
	require.Empty(t, r.backfillJobs)
	require.Zero(t, h.cloud.submits)
}
