package fetchplan

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/cloud/cloudtest"
	"github.com/mooyang-code/moox/modules/collector/internal/cloud/tencentscf"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/stretchr/testify/require"
)

// T5 两路审查之后补的用例：删函数不越过空间、重启时按已确认的状态启用、跨轮结束的失败要上报、计划外识别看命名空间、
// 删补采函数后能重建、未生效计划期间删掉的函数下一轮重建。

func activeTimerFunction(packageID string) *cloudtest.Function {
	return &cloudtest.Function{
		Info:  tencentscf.FunctionInfo{Status: "Active", Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": packageID}},
		Timer: &tencentscf.TimerTriggerInfo{Name: "moox-market-fetch-timer", Type: "timer", Cron: "0 * * * * * *", Enabled: true, AvailableStatus: "Available", Qualifier: "$LATEST", Message: "market_fetch_timer_v1"},
	}
}

// B1：按名字删别的空间的函数被拒绝，函数还在。
func TestDeleteCloudFunctionsRefusesFunctionsOfAnotherSpace(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.applyFirstPlan(r)
	stockcn := scfRefIn(PlanFunctionName("stockcn", "equity", "ap-nanjing", 0), "ap-nanjing", "default")
	h.scf.Seed(stockcn, activeTimerFunction("pkg"))

	results, err := r.DeleteCloudFunctions(context.Background(), DeleteCloudFunctionsRequest{Names: []string{stockcn.FunctionName}})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.False(t, results[0].Done)
	require.Contains(t, results[0].Error, "不属于空间 crypto")
	_, exists := h.scf.Lookup(stockcn)
	require.True(t, exists, "别的空间的函数不能删")
	require.Zero(t, h.cloud.countOps("delete", ""))
}

// B1：crypto 按前缀删，不碰前缀相同的 crypto-x 的函数。
func TestDeleteCloudFunctionsByPrefixKeepsTheOtherSpaceWithTheSamePrefix(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	r.OtherSpaces = map[string]map[string][]string{"crypto-x": {"spot": {"ap-nanjing"}}}
	h.applyFirstPlan(r)
	other := scfRefIn(PlanFunctionName("crypto-x", "spot", "ap-nanjing", 0), "ap-nanjing", "default")
	h.scf.Seed(other, activeTimerFunction("pkg"))
	require.True(t, strings.HasPrefix(other.FunctionName, r.fleetPrefix()), "前缀确实相同")

	results, err := r.DeleteCloudFunctions(context.Background(), DeleteCloudFunctionsRequest{Prefix: r.fleetPrefix()})
	require.NoError(t, err)
	require.Len(t, results, 2, "只删本空间的两个函数")
	for _, result := range results {
		require.True(t, result.Done, result.Name)
		require.NotEqual(t, other.FunctionName, result.Name)
	}
	_, exists := h.scf.Lookup(other)
	require.True(t, exists)
	require.Equal(t, []string{other.FunctionName}, h.cloudTimers(), "本空间的函数删掉了，crypto-x 的还在")
}

// M1：5m 计划加 1m 任务，下发确认（定时器改成每分钟）、计划还没生效时进程重启：EnableAll 按已确认的 Target 启用，
// 不把 cron 改回每 5 分钟；计划生效后读回仍是每分钟。
func TestRestartKeepsTheConfirmedCron(t *testing.T) {
	h := newPlanHarness(t, 60)
	h.tasks.tasks = []domain.CollectionTask{{
		SpaceID: "crypto", TaskID: "spot-5m", Enabled: true, DataType: "kline", TagIDs: []string{"binance_spot"},
		CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"ds_spot_5m","frequency":"5m"}`,
	}}
	r := h.reconciler()
	planBaselineSettle(t, h, r)
	name := h.cloudTimers()[0]
	fiveMinutes := h.mustFunction(name).Timer.Cron
	require.NotEqual(t, "0 * * * * * *", fiveMinutes)

	h.tasks.tasks = append(h.tasks.tasks, domain.CollectionTask{
		SpaceID: "crypto", TaskID: "spot-1m", Enabled: true, DataType: "kline", TagIDs: []string{"binance_spot"},
		CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"ds_spot_1m","frequency":"1m"}`,
	})
	r.lastVerified = h.now
	// 生效日志写不进去：计划下发完也留在未生效状态。
	h.activity.failures = 1000
	_ = h.reconcile(r)
	open := h.open()
	require.NotNil(t, open)
	progress, err := decodeFetchPlanProgress(open.Progress)
	require.NoError(t, err)
	require.True(t, progress.Pushed[name], "下发已确认")
	content := h.content(open)
	transitionalCron := content.Functions[name].Cron
	require.Equal(t, transitionalCron, h.mustFunction(name).Timer.Cron)
	require.NotEqual(t, fiveMinutes, transitionalCron)

	// 进程重启：新的对账器先 EnableAll，再对账。
	restarted := h.reconciler()
	done, err := restarted.EnableAll(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	require.Equal(t, transitionalCron, h.mustFunction(name).Timer.Cron, "重启不能把已确认的 cron 改回已生效计划的")
	h.activity.failures = 0
	planBaselineSettle(t, h, restarted)
	require.Equal(t, content.Functions[name].Cron, h.mustFunction(name).Timer.Cron, "P 之后仍是每分钟")
	require.Equal(t, transitionalCron, h.mustFunction(name).Timer.Cron)
}

// M2：读回修复跨轮才结束、并且失败：下一轮把结果取走，按 cloud 报失败（不再悄悄丢掉）。
func TestRepairThatFailsAfterTheRoundIsReported(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.applyFirstPlan(r)
	metrics := roundMetrics(h, r)
	name := h.cloudTimers()[0]
	h.mutate(name, func(function *cloudtest.Function) {
		function.Info.Environment["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"] = "tampered"
	})
	release := make(chan struct{})
	h.cloud.client.BeforeWrite = func(request string, ref tencentscf.FunctionRef) error {
		if request == "UpdateFunctionConfiguration" && ref.FunctionName == name {
			<-release
			return errors.New("InternalError: update failed late")
		}
		return nil
	}
	r.opWaitMax = 20 * time.Millisecond
	h.now = h.now.Add(2 * time.Hour)
	require.NoError(t, h.reconcile(r), "修复还在跑，不是失败")
	requireNoFailure(t, metrics)

	close(release)
	h.waitOps(r)
	h.now = h.now.Add(time.Minute)
	_ = h.reconcile(r)
	require.Equal(t, float64(1), failureLabel(metrics, "cloud"), "跨轮结束的失败要报出来")
}

// M2：计划外函数停定时器失败：下一轮按 cloud 报失败。
func TestUnplannedTimerStopFailureIsReported(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.applyFirstPlan(r)
	metrics := roundMetrics(h, r)
	orphan := scfRefIn(PlanFunctionName("crypto", "spot", "ap-nanjing", 7), "ap-nanjing", "default")
	h.scf.Seed(orphan, activeTimerFunction("pkg"))
	h.cloud.client.BeforeWrite = func(request string, ref tencentscf.FunctionRef) error {
		if request == "EnsureTimerTrigger" && ref.FunctionName == orphan.FunctionName {
			return errors.New("InternalError: trigger update failed")
		}
		return nil
	}
	h.now = h.now.Add(2 * time.Hour)
	_ = h.reconcile(r)
	h.waitOps(r)
	h.now = h.now.Add(time.Minute)
	_ = h.reconcile(r)
	require.Equal(t, float64(1), failureLabel(metrics, "cloud"), "计划外函数停不掉要报出来")
	function, _ := h.scf.Lookup(orphan)
	require.True(t, function.Timer.Enabled)
}

// M3：命名空间配置改过之后，旧命名空间里的同名副本算计划外：定时器被停用、不删；在用的函数不受影响。
func TestStaleCopyInAnOldNamespaceIsStoppedWithoutTouchingTheLiveFunction(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.applyFirstPlan(r)
	name := h.cloudTimers()[0]
	copyRef := scfRefIn(name, "ap-nanjing", "old-namespace")
	h.scf.Seed(copyRef, activeTimerFunction("pkg-old"))
	live := h.cloud.countOps("ensure", name) + h.cloud.countOps("timer", name)

	h.now = h.now.Add(2 * time.Hour)
	require.NoError(t, h.reconcile(r))
	h.waitOps(r)
	copyFunction, exists := h.scf.Lookup(copyRef)
	require.True(t, exists, "副本不删")
	require.False(t, copyFunction.Timer.Enabled, "旧命名空间里的副本被停用")
	require.True(t, h.timerEnabled(name), "在用的函数不受影响")
	require.Equal(t, live+1, h.cloud.countOps("ensure", name)+h.cloud.countOps("timer", name), "只有停副本这一次操作")

	views, err := r.ListCloudFunctions(context.Background())
	require.NoError(t, err)
	found := 0
	for _, view := range views {
		if view.Name != name {
			continue
		}
		found++
		require.Equal(t, view.Namespace == "old-namespace", view.Unplanned, view.Namespace)
		require.True(t, view.Readback.Exists)
	}
	require.Equal(t, 2, found, "两份各有自己的一项与读回")
	readback, ok := r.TimerReadback(name, "ap-nanjing")
	require.True(t, ok)
	require.Equal(t, "pkg", readback.PackageID, "副本的读回不覆盖在用函数的")
}

// M4：RPC 删掉补采函数后，下一次 EnsureBackfillFunction 重建它（就绪缓存随删除作废）。
func TestDeletedBackfillFunctionIsRebuiltOnTheNextUse(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	name, err := r.EnsureBackfillFunction(context.Background(), "spot")
	require.NoError(t, err)
	results, err := r.DeleteCloudFunctions(context.Background(), DeleteCloudFunctionsRequest{Names: []string{name}})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.True(t, results[0].Done, results[0].Error)
	_, err = r.EnsureBackfillFunction(context.Background(), "spot")
	require.NoError(t, err)
	require.Equal(t, 2, h.cloud.client.Count("CreateFunction"))
	require.Equal(t, "backfill", h.mustFunction(name).Info.Environment["MOOX_MARKET_FETCH_MODE"])
}

// M5：有未生效的计划时，RPC 删掉一个不受这份计划影响的函数，下一轮就重建。现货、永续各一个数据集，只排除一个现货标的：
// 计划只改现货的函数，永续的函数不受影响。
func TestDeletedUntouchedFunctionIsRebuiltWhileAPlanIsOpen(t *testing.T) {
	h := newPlanHarness(t, 60)
	h.tasks.tasks = []domain.CollectionTask{
		{SpaceID: "crypto", TaskID: "spot-1m", Enabled: true, DataType: "kline", TagIDs: []string{"binance_spot"},
			CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"ds_spot","frequency":"1m"}`},
		{SpaceID: "crypto", TaskID: "swap-1m", Enabled: true, DataType: "kline", TagIDs: []string{"binance_swap"},
			CollectParams: `{"provider":"binance","market_type":"swap","subject_tags":["binance_swap"],"target_dataset_id":"ds_swap","frequency":"1m"}`},
	}
	r := h.reconciler()
	r.Groups.Symbols = planBaselineSymbols{h.symbols}
	r.Deploy.Networks["ap-hongkong"] = PlanNetwork{VPCID: "vpc-2", SubnetID: "subnet-2"}
	exclusions := &memExclusions{}
	r.Exclusions = exclusions
	planBaselineSettle(t, h, r)
	h.now = h.now.Add(time.Minute)
	require.NoError(t, h.reconcile(r))
	require.False(t, r.lastVerified.IsZero(), "读回过")

	// 生效日志写不进去：计划下发完也留在未生效状态。
	h.activity.failures = 1000
	exclusions.items = []store.SubjectExclusion{{SpaceID: "crypto", SubjectID: "S000-USDT", MarketType: "spot"}}
	_ = h.reconcile(r)
	open := h.open()
	require.NotNil(t, open)
	content := h.content(open)
	var untouched string
	for _, name := range content.SortedFunctionNames() {
		function := content.Functions[name]
		if function.IsSCF() && !function.Push && !function.Create {
			untouched = name
			break
		}
	}
	require.NotEmpty(t, untouched, "永续的函数不受这份计划影响")
	results, err := r.DeleteCloudFunctions(context.Background(), DeleteCloudFunctionsRequest{Names: []string{untouched}})
	require.NoError(t, err)
	require.True(t, results[0].Done, results[0].Error)

	h.now = h.now.Add(time.Minute)
	_ = h.reconcile(r)
	require.NotNil(t, h.open(), "计划还没生效")
	require.True(t, h.timerEnabled(untouched), "下一轮就重建")
	want, err := marketfetch.EncodePlan(content.Functions[untouched].Old)
	require.NoError(t, err)
	require.Equal(t, want, h.mustFunction(untouched).Info.Environment[marketfetch.PlanEnvName])
}

// M6：Ensure 在暂停开始之后才走到设定时器那一步：暂停钩子把定时器设成停用，DisableAll 第二次调用之前云上就已经是停用。
func TestEnsureFinishingDuringPauseSetsTheTimerDisabled(t *testing.T) {
	h := newPlanHarness(t, 60)
	names := planFunctionNames(t, 60)
	var mu sync.Mutex
	entered := make(chan struct{})
	release := make(chan struct{})
	once := sync.Once{}
	h.cloud.client.BeforeWrite = func(request string, ref tencentscf.FunctionRef) error {
		mu.Lock()
		defer mu.Unlock()
		if request == "UpdateFunctionEventInvokeConfig" && ref.FunctionName == names[0] {
			once.Do(func() { close(entered) })
			mu.Unlock()
			<-release
			mu.Lock()
		}
		return nil
	}
	r := h.reconciler()
	r.opWaitMax = 20 * time.Millisecond
	require.NoError(t, h.reconcile(r))
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Ensure 没有走到第 4 步")
	}
	done, err := r.DisableAll(context.Background())
	require.NoError(t, err)
	require.False(t, done)
	close(release)
	h.waitOps(r)
	require.False(t, h.timerEnabled(names[0]), "Ensure 设定时器时看到正在暂停，设成停用")
}

// 小项：同一空间换了新的对账器（恢复）之后，旧对账器上起的操作在进程退出时也被取消。
func TestClosingAlsoStopsReplacedReconcilers(t *testing.T) {
	h := newPlanHarness(t, 60)
	old, current := h.reconciler(), h.reconciler()
	op, err := old.ops().start("fn-backfill", opKeyBackfill, false, func(ctx context.Context, _ *functionOp) error {
		<-ctx.Done()
		return ctx.Err()
	})
	require.NoError(t, err)
	waitHolding(t, old, op)
	reconcilers := &Reconcilers{}
	reconcilers.Store("crypto", old)
	reconcilers.Store("crypto", current)
	require.Same(t, current, reconcilers.Get("crypto"))
	closed := make(chan struct{})
	go func() { reconcilers.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("关闭挂住了")
	}
	require.ErrorIs(t, op.err, context.Canceled)
}

// 小项：缺代码包时只看本空间的函数，前缀相同的 crypto-x 的函数不挡住 crypto。
func TestMissingPackageIgnoresFunctionsOfAnotherSpaceWithTheSamePrefix(t *testing.T) {
	h := newPlanHarness(t, 60)
	h.tasks.tasks = nil
	h.cloud.setPackages()
	other := scfRefIn(PlanFunctionName("crypto-x", "spot", "ap-nanjing", 0), "ap-nanjing", "default")
	h.scf.Seed(other, activeTimerFunction("pkg"))
	r := h.reconciler()
	r.OtherSpaces = map[string]map[string][]string{"crypto-x": {"spot": {"ap-nanjing"}}}
	require.NoError(t, r.packageGate(context.Background()))
	h.scf.Seed(scfRefIn(PlanFunctionName("crypto", "spot", "ap-nanjing", 0), "ap-nanjing", "default"), activeTimerFunction("pkg"))
	require.ErrorIs(t, r.packageGate(context.Background()), errNoCodePackage, "本空间的函数在云上时照样挡住")
}

// 小项：已生效计划里没有云函数时，读回仍扫计划外的同前缀函数并停掉定时器。
func TestUnplannedFunctionsAreStoppedEvenWithoutAppliedCloudFunctions(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	orphan := scfRefIn(PlanFunctionName("crypto", "spot", "ap-nanjing", 3), "ap-nanjing", "default")
	h.scf.Seed(orphan, activeTimerFunction("pkg"))
	require.NoError(t, r.verifyFleet(context.Background(), nil))
	h.waitOps(r)
	function, ok := h.scf.Lookup(orphan)
	require.True(t, ok)
	require.False(t, function.Timer.Enabled)
}

// T5～T7 审查小项：计划外扫描每个地域只清点一次，采集函数与补采函数按各自前缀从同一份清单里筛。
func TestUnplannedScanListsEachRegionOnce(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	orphan := scfRefIn(PlanFunctionName("crypto", "spot", "ap-nanjing", 3), "ap-nanjing", "default")
	h.scf.Seed(orphan, activeTimerFunction("pkg"))
	staleBackfill := scfRefIn(BackfillFunctionName("crypto", "spot", "ap-nanjing"), "ap-nanjing", "old-namespace")
	h.scf.Seed(staleBackfill, activeTimerFunction("pkg"))
	before := h.scf.InventoryCalls()

	refs, err := r.scanUnplanned(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, len(r.planRegions()), h.scf.InventoryCalls()-before)
	names := map[string]bool{}
	for _, ref := range refs {
		names[ref.Name] = true
	}
	require.True(t, names[orphan.FunctionName], "采集函数")
	require.True(t, names[staleBackfill.FunctionName], "补采函数")
	require.Len(t, refs, 2)
}

// T5～T7 审查小项：命名空间改过之后，只删旧命名空间里的同名副本时，在用的那一份还在：
// 下一轮不去掉它的确认，也不再推一次。
func TestDeletingAStaleCopyKeepsTheConfirmationOfTheLiveFunction(t *testing.T) {
	h := newPlanHarness(t, 120)
	r := h.reconciler()
	h.applyFirstPlan(r)
	h.setSubjects(119)
	// 生效日志写不进去：计划下发完也留在未生效状态。
	h.activity.failures = 1000
	_ = h.reconcile(r)
	open := h.open()
	require.NotNil(t, open)
	progress, err := decodeFetchPlanProgress(open.Progress)
	require.NoError(t, err)
	content := h.content(open)
	var victim string
	for _, name := range content.SortedFunctionNames() {
		if progress.Pushed[name] {
			victim = name
			break
		}
	}
	require.NotEmpty(t, victim, "有已确认下发的函数")
	live := scfRef(victim)
	legacy := scfRefIn(victim, live.Region, "legacy")
	h.scf.Seed(legacy, activeTimerFunction("pkg-old"))
	ensures := h.cloud.countOps("ensure", victim)

	results, err := r.DeleteCloudFunctions(context.Background(), DeleteCloudFunctionsRequest{Names: []string{victim}, Namespace: "legacy", Region: live.Region})
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.True(t, results[0].Done, results[0].Error)
	require.Equal(t, "legacy", results[0].Namespace)

	h.now = h.now.Add(20 * time.Second)
	_ = h.reconcile(r)
	require.NotNil(t, h.open(), "计划仍未生效")
	progress, err = decodeFetchPlanProgress(h.open().Progress)
	require.NoError(t, err)
	require.True(t, progress.Pushed[victim], "在用的函数仍算已确认")
	require.Equal(t, ensures, h.cloud.countOps("ensure", victim), "在用的函数没有被再推一次")
	_, exists := h.scf.Lookup(legacy)
	require.False(t, exists, "旧副本删掉了")
	_, exists = h.scf.Lookup(live)
	require.True(t, exists)
}

// 未生效计划里已确认下发的函数被 RPC 删掉：下一轮去掉它的确认、按 Target 重推（Ensure 建出来），不等读回修复。
func TestDeletedPushedFunctionIsRepushed(t *testing.T) {
	h := newPlanHarness(t, 120)
	r := h.reconciler()
	h.applyFirstPlan(r)
	// 生效日志写不进去：计划下发完也留在未生效状态。
	h.activity.failures = 1000
	h.setSubjects(119)
	_ = h.reconcile(r)
	open := h.open()
	require.NotNil(t, open)
	progress, err := decodeFetchPlanProgress(open.Progress)
	require.NoError(t, err)
	name := h.cloudTimers()[0]
	require.True(t, progress.Pushed[name])
	results, err := r.DeleteCloudFunctions(context.Background(), DeleteCloudFunctionsRequest{Names: []string{name}})
	require.NoError(t, err)
	require.True(t, results[0].Done, results[0].Error)
	require.NoError(t, r.reconfirmDeleted(context.Background(), h.open()))
	require.Empty(t, r.takeDeletedFunctions(), "记录取走后清空")
	after, err := decodeFetchPlanProgress(h.open().Progress)
	require.NoError(t, err)
	require.False(t, after.Pushed[name], "去掉确认")

	h.activity.failures = 0
	require.NoError(t, h.reconcile(r))
	require.Nil(t, h.open())
	want, err := marketfetch.EncodePlan(h.appliedContent().Functions[name].Target)
	require.NoError(t, err)
	require.Equal(t, want, h.mustFunction(name).Info.Environment[marketfetch.PlanEnvName], "按 Target 重建")
}

// 复审小项：定时器已经停用的计划外函数，每小时的读回不再重发停用。
func TestAlreadyStoppedUnplannedFunctionIsLeftAlone(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.applyFirstPlan(r)
	orphan := scfRefIn(PlanFunctionName("crypto", "spot", "ap-nanjing", 7), "ap-nanjing", "default")
	stopped := activeTimerFunction("pkg")
	stopped.Timer.Enabled = false
	h.scf.Seed(orphan, stopped)
	h.now = h.now.Add(2 * time.Hour)
	require.NoError(t, h.reconcile(r))
	h.waitOps(r)
	require.Zero(t, h.cloud.countOps("timer", orphan.FunctionName), "已经停用的不再发停用")
}

// T6：同名函数在旧命名空间里留下的副本是计划外函数；按名字删时两份都删，结果按命名空间区分；删掉后不再列为计划外。
func TestDeleteByNameReportsTheNamespaceOfEachCopy(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.applyFirstPlan(r)
	name := h.cloudTimers()[0]
	planned := scfRef(name)
	legacy := scfRefIn(name, planned.Region, "legacy")
	h.scf.Seed(legacy, activeTimerFunction("pkg-old"))
	h.now = h.now.Add(2 * time.Hour)
	require.NoError(t, h.reconcile(r))
	h.waitOps(r)
	unplanned := r.UnplannedFunctions()
	require.Len(t, unplanned, 1)
	require.Equal(t, "legacy", unplanned[0].Namespace)
	require.Equal(t, name, unplanned[0].Name)

	results, err := r.DeleteCloudFunctions(context.Background(), DeleteCloudFunctionsRequest{Names: []string{name}})
	require.NoError(t, err)
	namespaces := map[string]bool{}
	for _, result := range results {
		require.Equal(t, name, result.Name)
		require.True(t, result.Done, result.Namespace)
		namespaces[result.Namespace] = true
	}
	require.Equal(t, map[string]bool{"default": true, "legacy": true}, namespaces)
	require.Empty(t, r.UnplannedFunctions(), "删掉的副本不再列为计划外")
}

// T6 复审：补采函数在旧命名空间里的副本，读回时列为计划外（种类 backfill）；预览与实际删除用同一段匹配，预览列出的就是实际删掉的，预览不删。
func TestDeleteDryRunListsExactlyWhatIsDeletedIncludingBackfillCopies(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.applyFirstPlan(r)
	backfill := BackfillFunctionName("crypto", "spot", "ap-nanjing")
	current := scfRefIn(backfill, "ap-nanjing", "default")
	legacy := scfRefIn(backfill, "ap-nanjing", "legacy")
	h.scf.Seed(current, &cloudtest.Function{Info: tencentscf.FunctionInfo{Status: "Active"}})
	h.scf.Seed(legacy, &cloudtest.Function{Info: tencentscf.FunctionInfo{Status: "Active"}})

	views, err := r.ListCloudFunctions(context.Background())
	require.NoError(t, err)
	var unplanned []CloudFunctionView
	for _, view := range views {
		if view.Unplanned {
			unplanned = append(unplanned, view)
		}
	}
	require.Len(t, unplanned, 1, "补采函数的旧副本看得见")
	require.Equal(t, "legacy", unplanned[0].Namespace)
	require.Equal(t, "backfill", unplanned[0].Kind)

	request := DeleteCloudFunctionsRequest{Prefix: r.backfillPrefix(), DryRun: true}
	preview, err := r.DeleteCloudFunctions(context.Background(), request)
	require.NoError(t, err)
	require.Len(t, preview, 2)
	for _, item := range preview {
		require.False(t, item.Done)
		require.Empty(t, item.Error)
	}
	_, exists := h.scf.Lookup(legacy)
	require.True(t, exists, "预览不删")
	require.Zero(t, h.cloud.countOps("delete", ""))

	request.DryRun = false
	results, err := r.DeleteCloudFunctions(context.Background(), request)
	require.NoError(t, err)
	require.Len(t, results, len(preview))
	for i, result := range results {
		require.True(t, result.Done, result.Namespace)
		require.Equal(t, preview[i].Namespace, result.Namespace)
	}

	_, err = r.DeleteCloudFunctions(context.Background(), DeleteCloudFunctionsRequest{Prefix: "moox-fetch-plan-", DryRun: true})
	require.ErrorContains(t, err, "不是本空间的函数名前缀", "无效前缀在预览时就拒绝")
	_, err = r.DeleteCloudFunctions(context.Background(), DeleteCloudFunctionsRequest{Names: []string{backfill}, Region: "ap-tokyo", DryRun: true})
	require.ErrorContains(t, err, "不在空间 crypto")
}

// T6 复审：同名函数在旧命名空间里的副本与配置命名空间里在用的函数，按 --namespace 删只删副本，在用的不动。
func TestDeleteByNamespaceKeepsTheFunctionInUse(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.applyFirstPlan(r)
	name := h.cloudTimers()[0]
	planned := scfRef(name)
	legacy := scfRefIn(name, planned.Region, "legacy")
	h.scf.Seed(legacy, activeTimerFunction("pkg-old"))

	request := DeleteCloudFunctionsRequest{Names: []string{name}, Namespace: "legacy", Region: planned.Region, DryRun: true}
	preview, err := r.DeleteCloudFunctions(context.Background(), request)
	require.NoError(t, err)
	require.Len(t, preview, 1)
	require.Equal(t, "legacy", preview[0].Namespace)

	request.DryRun = false
	results, err := r.DeleteCloudFunctions(context.Background(), request)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.True(t, results[0].Done)
	require.Equal(t, "legacy", results[0].Namespace)
	_, exists := h.scf.Lookup(legacy)
	require.False(t, exists, "旧副本删掉了")
	require.True(t, h.timerEnabled(name), "在用的函数还在、定时器开着")

	missing, err := r.DeleteCloudFunctions(context.Background(), DeleteCloudFunctionsRequest{Names: []string{name}, Namespace: "other"})
	require.NoError(t, err)
	require.Len(t, missing, 1)
	require.Contains(t, missing[0].Error, "云上没有符合条件的这个函数")
	require.True(t, h.timerEnabled(name))
}

// 配置了 backfill_region 时补采函数放在那个地域：计划内的函数、清点的地域都包含它；
// 原来放在市场第一个地域的补采函数仍认得出是本空间的（列为计划外，可以删）。
func TestBackfillRegionOverride(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	market := ""
	for name := range r.Options.Markets {
		market = name
	}
	first, ok := r.BackfillRegion(market)
	require.True(t, ok)
	require.Equal(t, r.Options.Markets[market][0], first)

	r.Options.BackfillRegion = "ap-guangzhou"
	region, ok := r.BackfillRegion(market)
	require.True(t, ok)
	require.Equal(t, "ap-guangzhou", region)
	_, ok = r.BackfillRegion("no-such-market")
	require.False(t, ok, "没配置的市场仍然没有补采函数")
	require.Contains(t, r.planRegions(), "ap-guangzhou")
	require.Contains(t, r.planRegions(), first)
	planned, err := r.plannedNames(context.Background())
	require.NoError(t, err)
	require.Contains(t, planned, refKey(r.backfillRef(market, "ap-guangzhou")))
	require.NotContains(t, planned, refKey(r.backfillRef(market, first)))
	require.True(t, r.ownsBackfillName(BackfillFunctionName(r.SpaceID, market, "ap-guangzhou")))
	require.True(t, r.ownsBackfillName(BackfillFunctionName(r.SpaceID, market, first)))
}
