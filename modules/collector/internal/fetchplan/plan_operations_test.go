package fetchplan

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/cloud/cloudtest"
	"github.com/mooyang-code/moox/modules/collector/internal/cloud/tencentscf"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/stretchr/testify/require"
)

// 批 2 T5 新增的对账器场景：云上已有同名函数时接管、重启续做、慢操作、暂停与操作表、过渡期间的修复与升级、
// 计划外函数、RPC 删除、进程退出。断言看云上状态。

// gate 让某个写请求停在那里，直到放行。
type gate struct {
	mu      sync.Mutex
	request string
	name    string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGate(request, name string) *gate {
	return &gate{request: request, name: name, entered: make(chan struct{}), release: make(chan struct{})}
}

func (g *gate) hook(request string, ref tencentscf.FunctionRef) error {
	if request != g.request || (g.name != "" && ref.FunctionName != g.name) {
		return nil
	}
	g.once.Do(func() { close(g.entered) })
	<-g.release
	return nil
}

func (g *gate) open() { close(g.release) }

func (g *gate) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-g.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("写请求没有到达")
	}
}

// 自动接管：云上已有同名函数（环境是旧部署的、带旧的创建预留号），计划照样走到已生效，没有 already exists，也不删不重建。
func TestReconcilerTakesOverExistingFunctionsByName(t *testing.T) {
	h := newPlanHarness(t, 60)
	for _, name := range planFunctionNames(t, 60) {
		h.scf.Seed(scfRef(name), &cloudtest.Function{
			Info: tencentscf.FunctionInfo{Status: "Active", Runtime: "Go1", Type: "Event", MemorySize: 64, Timeout: 60, MaxInstanceConcurrency: 1,
				PublicNetStatus: "ENABLE", VpcID: "vpc-1", SubnetID: "subnet-1", Environment: map[string]string{
					"MOOX_CREATE_RESERVATION_ID": "old-reservation", "MOOX_MARKET_FETCH_PLAN": `{"v":1}`, "MOOX_CODE_PACKAGE_ID": "pkg-old",
				}},
			Handler: "main", COSObject: "packages/pkg-old.zip",
			Timer: &tencentscf.TimerTriggerInfo{Name: "moox-market-fetch-timer", Type: "timer", Cron: "0 */5 * * * * *", Enabled: true, AvailableStatus: "Available", Qualifier: "$LATEST", Message: "market_fetch_timer_v1"},
		})
	}
	r := h.reconciler()
	h.applyFirstPlan(r)
	require.Zero(t, h.cloud.client.Count("CreateFunction"))
	require.Zero(t, h.cloud.client.Count("DeleteFunction"))
	for _, name := range planFunctionNames(t, 60) {
		function := h.mustFunction(name)
		require.NotContains(t, function.Info.Environment, "MOOX_CREATE_RESERVATION_ID", name)
		require.Equal(t, "pkg", function.Info.Environment["MOOX_CODE_PACKAGE_ID"])
		require.Equal(t, "packages/pkg.zip", function.COSObject)
		require.Equal(t, "0 * * * * * *", function.Timer.Cron)
	}
}

// 重启续做：计划推进到一半（第一个函数已建好，第二个卡在建函数）丢弃对账器，用同一个库与同一个假腾讯云新建对账器，
// 计划走完，每个函数的 CreateFunction 只调了一次。
func TestReconcilerResumesAfterARestartWithoutRecreating(t *testing.T) {
	h := newPlanHarness(t, 60)
	names := planFunctionNames(t, 60)
	g := newGate("UpdateFunctionEventInvokeConfig", names[1])
	h.cloud.client.BeforeWrite = g.hook
	r := h.reconciler()
	r.opWaitMax = 50 * time.Millisecond
	require.NoError(t, h.reconcile(r))
	require.NotNil(t, h.open())
	// 进程退出：在跑的操作被取消（第二个函数建出来了，但还没写完配置与定时器）。
	g.waitEntered(t)
	go func() { time.Sleep(20 * time.Millisecond); g.open() }()
	r.StopOperations(true)

	restarted := h.reconciler()
	planBaselineSettle(t, h, restarted)
	require.Equal(t, 2, h.cloud.client.Count("CreateFunction"), "每个函数只建一次，重启后接着做")
	for _, name := range names {
		require.True(t, h.timerEnabled(name), name)
	}
}

// 慢操作：Ensure 卡住 3 轮，期间不报失败、不重复起操作；结束后那一轮确认。
func TestSlowEnsureIsNotRestartedAndIsConfirmedWhenItFinishes(t *testing.T) {
	h := newPlanHarness(t, 60)
	names := planFunctionNames(t, 60)
	g := newGate("CreateFunction", names[0])
	h.cloud.client.BeforeWrite = g.hook
	r := h.reconciler()
	r.opWaitMax = 30 * time.Millisecond
	for round := 0; round < 3; round++ {
		require.NoError(t, h.reconcile(r), "慢操作不是失败")
		progress, err := decodeFetchPlanProgress(h.open().Progress)
		require.NoError(t, err)
		require.False(t, progress.Pushed[names[0]])
		h.now = h.now.Add(20 * time.Second)
	}
	require.Equal(t, 1, h.cloud.countOps("ensure", names[0]), "不重复起操作")
	g.open()
	h.waitOps(r)
	require.NoError(t, h.reconcile(r))
	progress, err := decodeFetchPlanProgress(h.lastRecord().Progress)
	require.NoError(t, err)
	require.True(t, progress.Pushed[names[0]], "结束后当轮确认")
	require.Equal(t, 1, h.cloud.countOps("ensure", names[0]))
}

// 暂停遇到排队的操作：并发名额被占满，一个会启用定时器的 Ensure 在排队。DisableAll 在它被取消、在跑的操作结束之前
// 一直返回未完成；最终云上定时器是停用，之后没有任何操作再把它打开。
func TestPauseCancelsQueuedOperationsAndWaitsForRunningOnes(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.applyFirstPlan(r)
	names := h.cloudTimers()
	done, err := r.DisableAll(context.Background())
	require.NoError(t, err)
	require.True(t, done)

	resumed := h.reconciler()
	resumed.Operations = make(chan struct{}, 1)
	resumed.opWaitMax = 30 * time.Millisecond
	release := make(chan struct{})
	busy, err := resumed.ops().start(opKeyOf(names[1]), opKeyRepair, true, func(ctx context.Context, _ *functionOp) error { <-release; return nil })
	require.NoError(t, err)
	waitHolding(t, resumed, busy)
	// 排队的 Ensure：恢复后的启用（它拿不到名额）。
	spec, err := resumed.desiredSpec(functionTarget{Name: names[0], Region: "ap-nanjing", Plan: h.cloudPlan(names[0]), Cron: "0 * * * * * *"})
	require.NoError(t, err)
	queued, err := resumed.startEnsure(opKeyRepair, spec)
	require.NoError(t, err)

	done, err = resumed.DisableAll(context.Background())
	require.NoError(t, err)
	require.False(t, done, "在跑的操作没结束")
	select {
	case <-queued.done:
	case <-time.After(5 * time.Second):
		t.Fatal("排队的操作没有被取消")
	}
	require.ErrorIs(t, queued.err, context.Canceled, "排队的操作被取消")
	require.Zero(t, h.cloud.countOps("ensure", names[0])-1, "排队的 Ensure 一次也没碰云端")
	_, err = resumed.startEnsure(opKeyRepair, spec)
	require.ErrorIs(t, err, errOpPausing, "正在暂停时不再起可能打开定时器的操作")

	close(release)
	h.waitOps(resumed)
	done, err = resumed.DisableAll(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	for _, name := range names {
		require.False(t, h.timerEnabled(name), name)
	}
	time.Sleep(20 * time.Millisecond)
	for _, name := range names {
		require.False(t, h.timerEnabled(name), "之后没有操作再打开 %s", name)
	}
}

// 暂停遇到在跑的操作：Ensure 正在第 4 步（改环境与配置），暂停等它结束后再停定时器，最终是停用；
// 它最后设定时器时看到空间正在暂停，设成停用，这次成功不算本阶段的确认。
func TestPauseWaitsForARunningEnsureThenDisables(t *testing.T) {
	h := newPlanHarness(t, 60)
	names := planFunctionNames(t, 60)
	g := newGate("UpdateFunctionEventInvokeConfig", names[0])
	h.cloud.client.BeforeWrite = g.hook
	r := h.reconciler()
	r.opWaitMax = 30 * time.Millisecond
	require.NoError(t, h.reconcile(r))
	g.waitEntered(t)

	done, err := r.DisableAll(context.Background())
	require.NoError(t, err)
	require.False(t, done, "Ensure 还在第 4 步")
	g.open()
	h.waitOps(r)
	done, err = r.DisableAll(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	for _, name := range names {
		require.False(t, h.timerEnabled(name), name)
	}
	progress, err := decodeFetchPlanProgress(h.open().Progress)
	require.NoError(t, err)
	require.False(t, progress.Pushed[names[0]], "暂停时设成停用的成功不算确认")
}

// 带旧份期间的读回修复与升级：已生效状态里数据集还带着未过窗口的旧份时触发一次读回修复和一次代码包升级，
// 函数环境仍是存下来的 Target（新旧两份），没有被改成只有一份。
func TestRepairAndUpgradeWhileAnOldCopyIsKeptUseTheStoredTarget(t *testing.T) {
	h := newPlanHarness(t, 120)
	r := h.reconciler()
	h.applyFirstPlan(r)
	h.setSubjects(119)
	require.NoError(t, h.reconcile(r))
	require.Nil(t, h.open())
	content := h.appliedContent()
	name := h.cloudTimers()[0]
	transitional := h.mustFunction(name).Info.Environment[marketfetch.PlanEnvName]
	require.Len(t, h.cloudPlan(name).Datasets[0].Copies, 2, "已生效的计划带新旧两份")

	// 读回修复：函数被手工删掉（不受这份计划影响的函数才做校验，这里直接调读回修复）。
	h.remove(name)
	applied, _ := h.store.LatestApplied(context.Background(), "crypto")
	appliedFunctions, _ := h.content(applied).Applied()
	r.lastVerified = time.Time{}
	require.NoError(t, r.verifyFleet(context.Background(), scfFunctions(appliedFunctions)))
	h.waitOps(r)
	require.Equal(t, transitional, h.mustFunction(name).Info.Environment[marketfetch.PlanEnvName], "修复按存下来的 Target 重建")

	// 升级：只换代码，环境沿用云上的过渡计划。
	h.cloud.setPackages("pkg", "pkg-new")
	h.now = h.now.Add(planUpgradeCheckInterval)
	require.NoError(t, h.reconcile(r))
	h.waitOps(r)
	function := h.mustFunction(name)
	require.Equal(t, "pkg-new", function.Info.Environment["MOOX_CODE_PACKAGE_ID"])
	require.Equal(t, transitional, function.Info.Environment[marketfetch.PlanEnvName], "升级不改环境")
	previous, err := marketfetch.EncodePlan(content.Functions[name].Old)
	require.NoError(t, err)
	require.NotEqual(t, previous, transitional, "两份的计划与上一份确实不同，断言才有意义")
}

// 计划里新建的函数被建出来时，环境就是计划里它的 Target。
func TestNewFunctionIsCreatedWithItsTarget(t *testing.T) {
	h := newPlanHarness(t, 120)
	r := h.reconciler()
	h.applyFirstPlan(r)
	h.setSubjects(160)
	require.NoError(t, h.reconcile(r))
	content := h.content(h.lastRecord())
	var created string
	for name, function := range content.Functions {
		if function.Create {
			created = name
		}
	}
	require.NotEmpty(t, created)
	want, err := marketfetch.EncodePlan(content.Functions[created].Target)
	require.NoError(t, err)
	require.Equal(t, want, h.mustFunction(created).Info.Environment[marketfetch.PlanEnvName])
	require.True(t, h.timerEnabled(created))
}

// 暂停期间的补采函数：空间处于系统暂停、正在恢复追赶，补采函数的就绪缓存是空的，EnsureBackfillFunction 能把函数建出来；
// 同时 RPC 删除也能执行。
func TestBackfillFunctionAndDeletionStillWorkWhileTheSpaceIsPaused(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.applyFirstPlan(r)
	done, err := r.DisableAll(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	require.True(t, r.pausing.Load())

	name, err := r.EnsureBackfillFunction(context.Background(), "spot")
	require.NoError(t, err)
	function := h.mustFunction(name)
	require.Nil(t, function.Timer)
	require.Equal(t, "backfill", function.Info.Environment["MOOX_MARKET_FETCH_MODE"])

	victim := h.cloudTimers()[0]
	results, err := r.DeleteCloudFunctions(context.Background(), DeleteCloudFunctionsRequest{Names: []string{victim}})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.True(t, results[0].Done, results[0].Error)
	_, exists := h.cloudFunction(victim)
	require.False(t, exists)
}

// 暂停再恢复：计划推进到一半（已有函数确认了下发、还有一个新建的函数）时暂停，再恢复。
// 已确认的函数被重新推了一次，新建的函数收到它的 Target 并启用定时器。
func TestPauseAndResumeRepushesAnOpenPlan(t *testing.T) {
	h := newPlanHarness(t, 120)
	r := h.reconciler()
	h.applyFirstPlan(r)
	h.setSubjects(160)
	var fresh string
	for _, name := range planFunctionNames(t, 160) {
		if _, exists := h.cloudFunction(name); !exists {
			fresh = name
		}
	}
	require.NotEmpty(t, fresh)
	g := newGate("CreateFunction", fresh)
	h.cloud.client.BeforeWrite = g.hook
	r.opWaitMax = 30 * time.Millisecond
	require.NoError(t, h.reconcile(r))
	g.waitEntered(t)
	progress, err := decodeFetchPlanProgress(h.open().Progress)
	require.NoError(t, err)
	require.Len(t, progress.Pushed, 3, "已有函数确认了下发")
	ensures := map[string]int{}
	for _, name := range h.cloudTimers() {
		ensures[name] = h.cloud.countOps("ensure", name)
	}

	// 暂停：新建的函数还在建（第 1 步），等它结束后才停用。
	done, err := r.DisableAll(context.Background())
	require.NoError(t, err)
	require.False(t, done)
	g.open()
	h.waitOps(r)
	done, err = r.DisableAll(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	progress, err = decodeFetchPlanProgress(h.open().Progress)
	require.NoError(t, err)
	require.Empty(t, progress.Pushed)

	// 恢复：新的对账器重推。
	resumed := h.reconciler()
	done, err = resumed.EnableAll(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	require.NoError(t, h.reconcile(resumed))
	content := h.content(h.lastRecord())
	for name, count := range ensures {
		require.Equal(t, count+1, h.cloud.countOps("ensure", name), "已确认的函数重新推一次：%s", name)
	}
	want, err := marketfetch.EncodePlan(content.Functions[fresh].Target)
	require.NoError(t, err)
	require.Equal(t, want, h.mustFunction(fresh).Info.Environment[marketfetch.PlanEnvName])
	for _, name := range planFunctionNames(t, 160) {
		require.True(t, h.timerEnabled(name), name)
	}
}

// 恢复时代码包落后：先升级、后开定时器；升级失败时一个定时器都不开（启用前代码包落后的不启用，见 EnableAll）。
func TestResumeUpgradesBeforeEnablingTimersAndKeepsFailedUpgradesDisabled(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.applyFirstPlan(r)
	done, err := r.DisableAll(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	h.cloud.setPackages("pkg", "pkg-new")
	names := h.cloudTimers()
	recover := h.failWrites("UpdateFunctionCode", names[0])
	h.scf.ResetWrites()

	resumed := h.reconciler()
	_, err = resumed.EnableAll(context.Background())
	require.ErrorIs(t, err, errCodeUpgradePending)
	require.False(t, h.timerEnabled(names[0]), "升级失败的函数定时器保持停用")
	recover()
	done, err = resumed.EnableAll(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	writes := h.scf.Writes()
	for _, name := range names {
		assertCloudOrder(t, writes, cloudtest.Key(scfRef(name)), "pkg-new")
		require.True(t, h.timerEnabled(name))
	}
}

// 计划外函数：符合本空间命名格式、不在计划里的两个函数（一个在第二片命名空间）在读回时被停掉定时器，都没被删，
// ListCloudFunctions 标成计划外；属于另一个已配置空间 crypto-x 的函数没被碰。
func TestUnplannedFunctionsAreStoppedButNotDeleted(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	r.OtherSpaces = map[string]map[string][]string{"crypto-x": {"spot": {"ap-nanjing"}}}
	h.applyFirstPlan(r)
	enabledTimer := func() *tencentscf.TimerTriggerInfo {
		return &tencentscf.TimerTriggerInfo{Name: "moox-market-fetch-timer", Type: "timer", Cron: "0 * * * * * *", Enabled: true, AvailableStatus: "Available", Qualifier: "$LATEST", Message: "market_fetch_timer_v1"}
	}
	seed := func(ref tencentscf.FunctionRef) {
		h.scf.Seed(ref, &cloudtest.Function{Info: tencentscf.FunctionInfo{Status: "Active", Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "pkg-old"}}, Timer: enabledTimer()})
	}
	orphan := scfRefIn(PlanFunctionName("crypto", "spot", "ap-nanjing", 7), "ap-nanjing", "default")
	shard := scfRefIn(PlanFunctionName("crypto", "spot", "ap-nanjing", 50), "ap-nanjing", "default-2")
	other := scfRefIn(PlanFunctionName("crypto-x", "spot", "ap-nanjing", 0), "ap-nanjing", "default")
	for _, ref := range []tencentscf.FunctionRef{orphan, shard, other} {
		seed(ref)
	}
	h.now = h.now.Add(2 * time.Hour)
	require.NoError(t, h.reconcile(r))
	h.waitOps(r)
	for _, ref := range []tencentscf.FunctionRef{orphan, shard} {
		function, ok := h.scf.Lookup(ref)
		require.True(t, ok, "计划外的函数不删除：%s", ref.FunctionName)
		require.False(t, function.Timer.Enabled, "计划外的函数定时器被停用：%s", ref.FunctionName)
	}
	function, ok := h.scf.Lookup(other)
	require.True(t, ok)
	require.True(t, function.Timer.Enabled, "另一个空间的函数不能碰")

	views, err := r.ListCloudFunctions(context.Background())
	require.NoError(t, err)
	unplanned := map[string]bool{}
	for _, view := range views {
		if view.Unplanned {
			unplanned[view.Name] = true
		}
	}
	require.Equal(t, map[string]bool{orphan.FunctionName: true, shard.FunctionName: true}, unplanned)
}

// RPC 删除：删掉一个计划内的函数后，下一轮立即读回并重建，不用等一小时。
func TestDeletedPlannedFunctionIsRebuiltOnTheNextRound(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.applyFirstPlan(r)
	h.now = h.now.Add(time.Minute)
	require.NoError(t, h.reconcile(r))
	require.False(t, r.lastVerified.IsZero(), "读回过")
	victim := h.cloudTimers()[0]
	results, err := r.DeleteCloudFunctions(context.Background(), DeleteCloudFunctionsRequest{Prefix: r.fleetPrefix() + "spot-nanjing-000"})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.True(t, results[0].Done)
	_, exists := h.cloudFunction(victim)
	require.False(t, exists)

	h.now = h.now.Add(time.Minute)
	require.NoError(t, h.reconcile(r))
	require.True(t, h.timerEnabled(victim), "下一轮就重建")
}

// 进程退出：有操作在跑时关闭，操作被取消，关闭不挂住；之后不再接受新操作。
func TestClosingCancelsRunningOperations(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	op, err := r.ops().start("fn-a", opKeyRepair, true, func(ctx context.Context, _ *functionOp) error {
		<-ctx.Done()
		return ctx.Err()
	})
	require.NoError(t, err)
	waitHolding(t, r, op)
	reconcilers := &Reconcilers{}
	reconcilers.Store("crypto", r)
	closed := make(chan struct{})
	go func() { reconcilers.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("关闭挂住了")
	}
	require.ErrorIs(t, op.err, context.Canceled)
	_, err = r.ops().start("fn-b", opKeyRepair, false, func(context.Context, *functionOp) error { return nil })
	require.ErrorIs(t, err, errOpsClosed)
}

// 升级遇到 UpdateFailed 的函数：UpgradeCode 报错后读回确认函数处于失败状态，退回 Ensure（重做更新）按已确认的期望内容修好，
// 代码包是最新的，环境仍是原来那份计划，定时器照常。
func TestUpgradeFallsBackToEnsureForAnUpdateFailedFunction(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.applyFirstPlan(r)
	target := h.cloudTimers()[0]
	plan := h.mustFunction(target).Info.Environment[marketfetch.PlanEnvName]
	h.mutate(target, func(function *cloudtest.Function) { function.Info.Status = "UpdateFailed" })
	h.cloud.client.AcceptUpdateFailed = true
	h.cloud.setPackages("pkg", "pkg-new")
	h.cloud.resetOperations()
	h.now = h.now.Add(planUpgradeCheckInterval)
	r.lastVerified = h.now
	require.NoError(t, h.reconcile(r))
	h.waitOps(r)

	function := h.mustFunction(target)
	require.Equal(t, "Active", function.Info.Status)
	require.Equal(t, "pkg-new", function.Info.Environment["MOOX_CODE_PACKAGE_ID"])
	require.Equal(t, plan, function.Info.Environment[marketfetch.PlanEnvName])
	require.True(t, function.Timer.Enabled)
	var order []string
	for _, call := range h.cloud.operations() {
		if call.Name == target {
			order = append(order, call.Op)
		}
	}
	require.Equal(t, []string{"upgrade", "ensure"}, order, "UpgradeCode 失败后退回 Ensure")
}
