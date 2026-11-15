package fetchplan

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/cloud"
	"github.com/mooyang-code/moox/modules/collector/internal/cloud/cloudtest"
	"github.com/mooyang-code/moox/modules/collector/internal/cloud/tencentscf"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/stretchr/testify/require"
)

// waitOps 等对账器在跑的云端操作全部结束（升级等不在当轮等待的操作）。
func (h *planHarness) waitOps(r *PlanReconciler) {
	h.t.Helper()
	require.Eventually(h.t, func() bool { _, running := r.ops().oldestRunning(); return !running }, 5*time.Second, 2*time.Millisecond, "云端操作没有结束")
}

// waitHolding 等操作真正开始（拿到并发名额、开始碰云端）：还在排队的操作会被暂停取消。
func waitHolding(t *testing.T, r *PlanReconciler, op *functionOp) {
	t.Helper()
	require.Eventually(t, func() bool {
		r.ops().mu.Lock()
		defer r.ops().mu.Unlock()
		return op.holding
	}, 5*time.Second, time.Millisecond)
}

// planFunctionNames 返回按配置算出的函数名（排序）。
func planFunctionNames(t *testing.T, subjects int) []string {
	t.Helper()
	state, err := BuildDesiredState(testPlanOptions(), testGroups(subjects, "1m"), nil)
	require.NoError(t, err)
	names := make([]string, 0, len(state.Functions))
	for name := range state.Functions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// failWrites 让某个函数的某种写请求失败，返回解除的函数。
func (h *planHarness) failWrites(request, function string) func() {
	var mu sync.Mutex
	active := true
	h.cloud.client.BeforeWrite = func(name string, ref tencentscf.FunctionRef) error {
		mu.Lock()
		defer mu.Unlock()
		if active && name == request && ref.FunctionName == function {
			return errTestBoom
		}
		return nil
	}
	return func() {
		mu.Lock()
		defer mu.Unlock()
		active = false
	}
}

func TestPlanReconcilerFullLifecycle(t *testing.T) {
	h := newPlanHarness(t, 120)
	r := h.reconciler()
	require.NoError(t, h.reconcile(r))

	record := h.open()
	require.NotNil(t, record, "the plan stays open until the window ends")
	require.Equal(t, store.FetchPlanApplying, record.Status)
	content := h.content(record)
	change := content.Datasets["ds_spot|1m"]
	require.Equal(t, time.Date(2026, 10, 12, 8, 37, 0, 0, time.UTC).Unix(), change.P)
	require.Equal(t, 15, h.lateness.values["ds_spot"])
	require.Equal(t, []int64{change.P}, h.registrar.registered["ds_spot|1m"])
	// 建函数与下发环境、定时器合成一次 Ensure：每个函数一次。
	require.Len(t, h.cloudTimers(), 3)
	require.Equal(t, 3, h.cloud.client.Count("CreateFunction"))
	require.Equal(t, 3, h.cloud.countOps("ensure", ""))
	for _, name := range h.cloudTimers() {
		function := h.mustFunction(name)
		require.True(t, function.Timer.Enabled, name)
		require.Equal(t, "0 * * * * * *", function.Timer.Cron)
		require.Len(t, h.cloudPlan(name).Datasets[0].Copies, 1)
		require.Equal(t, "access@storage", function.Info.Environment["MOOX_ACCESS_ID"])
		require.Equal(t, "pkg", function.Info.Environment["MOOX_CODE_PACKAGE_ID"])
		require.NotContains(t, function.Info.Environment, "MOOX_CREATE_RESERVATION_ID")
	}

	// 窗口结束前什么都不做；之后收尾并标记已生效。
	h.now = time.Unix(change.P, 0).Add(10 * time.Second)
	require.NoError(t, h.reconcile(r))
	require.NotNil(t, h.open())
	h.now = time.Unix(change.P, 0).Add(30 * time.Second)
	require.NoError(t, h.reconcile(r))
	require.Nil(t, h.open())
	require.Len(t, h.activity.entries, 2)

	// 没有变化：不会产生新计划，也不改云上状态（云端校验另有测试，这里让它保持已完成）。
	writes := len(h.scf.Writes())
	r.lastVerified = h.now
	h.now = h.now.Add(5 * time.Minute)
	require.NoError(t, h.reconcile(r))
	require.Nil(t, h.open())
	require.Len(t, h.scf.Writes(), writes)
	require.Len(t, h.store.records, 1)
}

func TestPlanReconcilerSubjectChangeUsesTwoCopies(t *testing.T) {
	h := newPlanHarness(t, 120)
	r := h.reconciler()
	h.applyFirstPlan(r)

	h.setSubjects(119)
	require.NoError(t, h.reconcile(r))
	record := h.open()
	require.NotNil(t, record)
	content := h.content(record)
	change := content.Datasets["ds_spot|1m"]
	require.Equal(t, []string{"S119-USDT"}, change.Removed)
	require.NotEmpty(t, change.OldHash)
	require.Len(t, h.registrar.registered["ds_spot|1m"], 2)
	for _, name := range h.cloudTimers() {
		copies := h.cloudPlan(name).Datasets[0].Copies
		require.Len(t, copies, 2, name)
		require.Equal(t, change.P, copies[1].From)
		require.Equal(t, change.Hash, copies[1].Hash)
		require.Equal(t, change.OldHash, copies[0].Hash)
	}

	// 收尾：每个函数只留最新一份。
	h.now = time.Unix(change.P, 0).Add(time.Minute)
	require.NoError(t, h.reconcile(r))
	require.Nil(t, h.open())
	for _, name := range h.cloudTimers() {
		require.Len(t, h.cloudPlan(name).Datasets[0].Copies, 1, name)
	}
	logged := 0
	for key := range h.activity.entries {
		if strings.Contains(key, ".2.") {
			logged++
		}
	}
	require.Equal(t, 2, logged, "second plan writes a created and an applied entry")
}

func TestPlanReconcilerRetriesOnlyFailedFunctions(t *testing.T) {
	h := newPlanHarness(t, 120)
	names := planFunctionNames(t, 120)
	recover := h.failWrites("CreateFunction", names[1])
	r := h.reconciler()
	require.Error(t, h.reconcile(r))
	record := h.open()
	progress, err := decodeFetchPlanProgress(record.Progress)
	require.NoError(t, err)
	require.True(t, progress.Transitioned[names[0]])
	require.False(t, progress.Transitioned[names[1]])
	require.Contains(t, record.LastError, names[1])
	_, exists := h.cloudFunction(names[1])
	require.False(t, exists)

	recover()
	before := h.cloud.client.Count("CreateFunction")
	require.NoError(t, h.reconcile(r))
	require.Equal(t, before+1, h.cloud.client.Count("CreateFunction"), "only the failed function is created again")
	require.Equal(t, 1, h.cloud.countOps("ensure", names[0]), "confirmed functions are not pushed again")
	require.Len(t, h.cloudTimers(), 3)
}

func TestPlanReconcilerRestartContinuesWithoutNewPeriod(t *testing.T) {
	h := newPlanHarness(t, 60)
	h.registrar.failures = 1
	r := h.reconciler()
	require.Error(t, h.reconcile(r))
	record := h.open()
	require.Equal(t, store.FetchPlanPending, record.Status)
	period := h.content(record).Datasets["ds_spot|1m"].P

	// 重启：新的对账器实例，晚一些恢复，但没有登记成功过，P 仍然在未来则不重选。
	h.now = h.now.Add(20 * time.Second)
	restarted := h.reconciler()
	require.NoError(t, h.reconcile(restarted))
	record = h.open()
	require.Equal(t, period, h.content(record).Datasets["ds_spot|1m"].P)
	require.Equal(t, []int64{period}, h.registrar.registered["ds_spot|1m"])
	require.Len(t, h.cloudTimers(), 2)
}

func TestPlanReconcilerReselectsPeriodOnlyOnExplicitStaleRejection(t *testing.T) {
	h := newPlanHarness(t, 60)
	h.registrar.failures = 1
	r := h.reconciler()
	require.Error(t, h.reconcile(r), "an unknown outcome is replayed, not reselected")
	original := h.content(h.open()).Datasets["ds_spot|1m"].P
	require.Empty(t, h.registrar.registered)

	// 保存计划后 Collector 宕机，恢复时 P 已过：Storage 明确拒绝，重选更晚的 P。
	h.now = h.now.Add(10 * time.Minute)
	h.registrar.stale = 1
	require.NoError(t, h.reconcile(r))
	reselected := h.content(h.open()).Datasets["ds_spot|1m"].P
	require.Greater(t, reselected, original)
	require.Empty(t, h.registrar.registered)

	require.NoError(t, h.reconcile(r))
	require.Equal(t, []int64{reselected}, h.registrar.registered["ds_spot|1m"])
	for _, name := range h.cloudTimers() {
		require.Equal(t, reselected, h.cloudPlan(name).Datasets[0].Copies[0].From)
	}
}

func TestPlanReconcilerActivityLogFailureKeepsPlanOpenAndDoesNotDuplicate(t *testing.T) {
	h := newPlanHarness(t, 60)
	h.activity.failures = 3
	r := h.reconciler()
	require.NoError(t, h.reconcile(r))
	change := h.content(h.open()).Datasets["ds_spot|1m"]
	h.now = time.Unix(change.P, 0).Add(time.Minute)
	require.Error(t, h.reconcile(r))
	require.NotNil(t, h.open(), "the plan is not applied before both entries are written")
	require.NoError(t, h.reconcile(r))
	require.Nil(t, h.open())
	require.Len(t, h.activity.entries, 2)
}

func TestPlanReconcilerRetiresFunctionsWhenSubjectsShrink(t *testing.T) {
	h := newPlanHarness(t, 120)
	r := h.reconciler()
	h.applyFirstPlan(r)

	h.setSubjects(40)
	require.NoError(t, h.reconcile(r))
	record := h.open()
	content := h.content(record)
	var retired []string
	for name, function := range content.Functions {
		if function.New == nil {
			retired = append(retired, name)
		}
	}
	require.Len(t, retired, 2)
	transitional := map[string]string{}
	for _, name := range retired {
		transitional[name] = h.mustFunction(name).Info.Environment[marketfetch.PlanEnvName]
	}
	h.now = time.Unix(content.Datasets["ds_spot|1m"].P, 0).Add(time.Minute)
	ensures := h.cloud.countOps("ensure", "")
	require.NoError(t, h.reconcile(r))
	require.Nil(t, h.open())
	for _, name := range retired {
		require.False(t, h.timerEnabled(name), "%s must be disabled after the window", name)
		require.Equal(t, transitional[name], h.mustFunction(name).Info.Environment[marketfetch.PlanEnvName], "退役的函数保留过渡环境")
		require.Equal(t, 1, h.cloud.countOps("timer", name), "退役只停定时器")
	}
	require.Equal(t, ensures+1, h.cloud.countOps("ensure", ""), "收尾只给留下的那个函数下发最终计划")
}

func TestPlanReconcilerKeepsStateWhenEnabledTasksHaveNoSubjects(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.applyFirstPlan(r)

	h.setSubjects(0)
	for i := 0; i < 2*planGoneDebounce; i++ {
		require.NoError(t, h.reconcile(r))
		require.Nil(t, h.open(), "an enabled task with no resolvable subjects must not retire the fleet")
	}
}

func TestPlanReconcilerRepairsDriftHourly(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.applyFirstPlan(r)

	// 云端读回与计划一致：不写。
	h.now = h.now.Add(2 * time.Hour)
	writes := len(h.scf.Writes())
	require.NoError(t, h.reconcile(r))
	require.Len(t, h.scf.Writes(), writes)
	require.True(t, h.now.Equal(r.lastVerified))

	// 被手工改动（受管环境的分配哈希、定时器的 cron）：下一次读回时改回。
	names := h.cloudTimers()
	hash := h.mustFunction(names[0]).Info.Environment["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"]
	h.mutate(names[0], func(function *cloudtest.Function) {
		function.Info.Environment["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"] = "tampered"
	})
	h.mutate(names[1], func(function *cloudtest.Function) { function.Timer.Cron = "0 */5 * * * * *" })
	h.now = h.now.Add(2 * time.Hour)
	require.NoError(t, h.reconcile(r))
	require.Equal(t, hash, h.mustFunction(names[0]).Info.Environment["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"])
	require.Equal(t, "0 * * * * * *", h.mustFunction(names[1]).Timer.Cron)
	require.True(t, h.now.Equal(r.lastVerified))
}

func TestPlanReconcilerRejectsOversizedEnvironment(t *testing.T) {
	h := newPlanHarness(t, 400)
	r := h.reconciler()
	r.Options.SymbolsPerFunction = 200
	h.tasks.tasks[0].CollectParams = strings.Replace(h.tasks.tasks[0].CollectParams, `"frequency":"1m"`, `"frequency":"1m","symbols_per_function":200`, 1)
	r.Deploy.BaseEnvironment["PADDING"] = strings.Repeat("x", 3000)
	require.Error(t, h.reconcile(r))
	require.Empty(t, h.store.records, "no plan is saved when the environment cannot fit")
}

func TestPlanManagerRunsOneWorkerPerSpaceAndDisablesRemovedSpace(t *testing.T) {
	h := newPlanHarness(t, 60)
	var mu sync.Mutex
	spaces := []string{"crypto"}
	created := 0
	manager := &PlanManager{
		Spaces: func(context.Context) ([]string, error) {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), spaces...), nil
		},
		Factory: func(space string) (*PlanReconciler, error) {
			mu.Lock()
			defer mu.Unlock()
			created++
			r := h.reconciler()
			r.Now = time.Now
			return r, nil
		},
		Interval: 40 * time.Millisecond, Offset: 5 * time.Millisecond, Timeout: time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { manager.Run(ctx); close(finished) }()

	require.Eventually(t, func() bool { return len(h.cloudTimers()) == 2 }, 5*time.Second, 10*time.Millisecond, "the space worker creates its functions")
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	require.Equal(t, 1, created, "one reconciler per space")
	spaces = nil
	mu.Unlock()

	require.Eventually(t, func() bool {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		return len(manager.workers) == 0
	}, 5*time.Second, 10*time.Millisecond, "the worker exits only after timers are disabled")
	cancel()
	<-finished
	for _, name := range h.cloudTimers() {
		require.False(t, h.timerEnabled(name), "every function of the removed space is disabled: %s", name)
	}
}

func TestPlanReconcilerCreateFailureDoesNotBlockExistingFunctions(t *testing.T) {
	h := newPlanHarness(t, 120)
	r := h.reconciler()
	h.applyFirstPlan(r)

	// 160 个标的 → 4 个函数，其中新增的 -003 建函数失败。
	h.setSubjects(160)
	var failing string
	for _, name := range planFunctionNames(t, 160) {
		if strings.HasSuffix(name, "-003") {
			failing = name
		}
	}
	recover := h.failWrites("CreateFunction", failing)
	require.Error(t, h.reconcile(r))
	progress, err := decodeFetchPlanProgress(h.open().Progress)
	require.NoError(t, err)
	require.Len(t, progress.Transitioned, 3, "the three existing functions still receive their transitional environment")
	require.False(t, progress.Transitioned[failing])
	_, exists := h.cloudFunction(failing)
	require.False(t, exists)
	recover()
	require.NoError(t, h.reconcile(r))
	require.True(t, h.timerEnabled(failing))
}

func TestPlanReconcilerRetiredFunctionsGetAValidCronWhenDisabled(t *testing.T) {
	h := newPlanHarness(t, 120)
	r := h.reconciler()
	h.applyFirstPlan(r)
	h.setSubjects(40)
	require.NoError(t, h.reconcile(r))
	p := h.content(h.open()).Datasets["ds_spot|1m"].P
	h.now = time.Unix(p, 0).Add(time.Minute)
	require.NoError(t, h.reconcile(r), "retiring only stops the timer")
	require.Nil(t, h.open())
	for _, name := range h.cloudTimers() {
		timer := h.mustFunction(name).Timer
		require.NotEmpty(t, timer.Cron, name)
	}
}

func TestPlanReconcilerRecreatesMissingFunctionsAndDetectsAccessDrift(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.applyFirstPlan(r)

	names := h.cloudTimers()
	h.remove(names[0])
	created := h.cloud.client.Count("CreateFunction")
	h.now = h.now.Add(2 * time.Hour)
	require.NoError(t, h.reconcile(r))
	require.Equal(t, created+1, h.cloud.client.Count("CreateFunction"), "a function deleted in the cloud is rebuilt from the applied plan")
	require.True(t, h.timerEnabled(names[0]))

	// 调用方密钥轮换：Access 指纹不一致的函数下一次读回时按新身份重推。
	r.Access = func(region string) (map[string]string, error) {
		access, err := testSCFAccessEnvironment(region)
		access["MOOX_CALLER_KEY_ID"] = "assigned-scf-key-18"
		return access, err
	}
	h.now = h.now.Add(2 * time.Hour)
	require.NoError(t, h.reconcile(r))
	for _, name := range h.cloudTimers() {
		require.Equal(t, "assigned-scf-key-18", h.mustFunction(name).Info.Environment["MOOX_CALLER_KEY_ID"], name)
	}
}

func TestTransitionalPlanDropsRemovedSymbolExceptions(t *testing.T) {
	old := &marketfetch.Plan{V: 1, Market: marketfetch.PlanMarket{MarketID: "crypto", Provider: "binance", SourceID: "s", MarketType: "spot"}, Fetch: marketfetch.PlanFetch{Concurrency: 5},
		SymbolExceptions: map[string]string{"1000SHIB-USDT": "1000SHIBUSDT"},
		Datasets:         []marketfetch.PlanDataset{{DatasetID: "d", Frequency: "1m", LatenessS: 15, Copies: []marketfetch.PlanCopy{{From: 100, Hash: "h", Count: 2, Subjects: []marketfetch.PlanSubject{{ID: "1000SHIB-USDT", Index: 0}, {ID: "BTC-USDT", Index: 1}}}}}}}
	next := &marketfetch.Plan{V: 1, Market: old.Market, Fetch: old.Fetch,
		Datasets: []marketfetch.PlanDataset{{DatasetID: "d", Frequency: "1m", LatenessS: 15, Copies: []marketfetch.PlanCopy{{Hash: "h2", Count: 2, Subjects: []marketfetch.PlanSubject{{ID: "1000SHIB-USDT", Index: 0}, {ID: "BTC-USDT", Index: 1}}}}}}}
	content := &FetchPlanContent{Datasets: map[string]*PlanDatasetChange{"d|1m": {Changed: true, HashChanged: true, P: 200, PlanDatasetState: PlanDatasetState{DatasetID: "d", Frequency: "1m", Hash: "h2", Count: 2}}}}
	function := &PlanFunctionChange{Name: "f", Old: old, New: next}
	transitional := content.TransitionalPlan(function)
	require.Empty(t, transitional.SymbolExceptions, "an exception that was removed from the new plan must not survive")

	next.SymbolExceptions = map[string]string{"1000SHIB-USDT": "1000SHIBUSDT"}
	require.Equal(t, "1000SHIBUSDT", content.TransitionalPlan(function).SymbolExceptions["1000SHIB-USDT"])
}

func TestBuildDesiredStateRejectsMarketTypeClash(t *testing.T) {
	groups := testGroups(5, "1m")
	other := testGroups(5, "1h")[0]
	other.Provider, other.SourceID = "okx", "okx"
	other.DatasetID = "ds_okx"
	_, err := BuildDesiredState(testPlanOptions(), append(groups, other), nil)
	require.ErrorContains(t, err, "more than one market")
}

func TestPlanLeadIsLongerWhenFunctionsMustBeCreated(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	require.NoError(t, h.reconcile(r))
	change := h.content(h.open()).Datasets["ds_spot|1m"]
	require.GreaterOrEqual(t, change.P-h.now.Unix(), int64(planLeadTimeWithCreate/time.Second))
}

func TestNamespaceShardsEveryFiftyFunctions(t *testing.T) {
	deploy := PlanDeploy{Namespace: "moox-stockcn-plan", NamespaceCapacity: 50}
	require.Equal(t, "moox-stockcn-plan", deploy.NamespaceFor("moox-fetch-plan-stockcn-equity-nanjing-000"))
	require.Equal(t, "moox-stockcn-plan", deploy.NamespaceFor("moox-fetch-plan-stockcn-equity-nanjing-049"))
	require.Equal(t, "moox-stockcn-plan-2", deploy.NamespaceFor("moox-fetch-plan-stockcn-equity-nanjing-050"))
	require.Equal(t, "moox-stockcn-plan-4", deploy.NamespaceFor("moox-fetch-plan-stockcn-equity-nanjing-169"))
	require.Equal(t, "plain", PlanDeploy{Namespace: "plain"}.NamespaceFor("f-999"))
}

// 建函数失败后的遗留（云上的 CreateFailed 函数）不再靠清理预留：Ensure 先删掉它、等删完，再按期望内容重建。
func TestPlanReconcilerClearsStaleCreateReservationsBeforeRecreating(t *testing.T) {
	h := newPlanHarness(t, 60)
	name := planFunctionNames(t, 60)[0]
	h.scf.Seed(scfRef(name), &cloudtest.Function{Info: tencentscf.FunctionInfo{Status: "CreateFailed", Environment: map[string]string{"MOOX_CREATE_RESERVATION_ID": "old-reservation"}}})
	r := h.reconciler()
	require.NoError(t, h.reconcile(r))
	require.Equal(t, 1, h.cloud.client.Count("DeleteFunction"), "the failed function is deleted before creating")
	function := h.mustFunction(name)
	require.Equal(t, "Active", function.Info.Status)
	require.NotContains(t, function.Info.Environment, "MOOX_CREATE_RESERVATION_ID")
	require.True(t, function.Timer.Enabled)
}

// 云上的同名函数还在创建中（例如重启前起的操作建到一半）：Ensure 等它回到 Active 后接管，不重复建，也不删。
func TestPlanReconcilerWaitsForFreshCreateReservations(t *testing.T) {
	h := newPlanHarness(t, 60)
	name := planFunctionNames(t, 60)[0]
	h.scf.Seed(scfRef(name), &cloudtest.Function{Info: tencentscf.FunctionInfo{Status: "Creating", Environment: map[string]string{}}, Pending: 3})
	r := h.reconciler()
	require.NoError(t, h.reconcile(r))
	require.Equal(t, 1, h.cloud.client.Count("CreateFunction"), "only the other function is created")
	require.Zero(t, h.cloud.client.Count("DeleteFunction"))
	require.True(t, h.timerEnabled(name))
}

func TestPlanManagerDisablesConfiguredSpacesThatAreInactiveAtStartup(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	require.NoError(t, h.reconcile(r)) // 先让该空间有函数与计划，模拟停机前的状态
	require.NotEmpty(t, h.cloudTimers())
	created := 0
	var mu sync.Mutex
	manager := &PlanManager{
		Configured: func(context.Context) ([]string, error) { return []string{"crypto"}, nil },
		Spaces:     func(context.Context) ([]string, error) { return nil, nil },
		Factory: func(string) (*PlanReconciler, error) {
			mu.Lock()
			defer mu.Unlock()
			created++
			reconciler := h.reconciler()
			reconciler.Now = time.Now
			return reconciler, nil
		},
		Interval: 40 * time.Millisecond, Offset: 5 * time.Millisecond, Timeout: time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { manager.Run(ctx); close(finished) }()
	require.Eventually(t, func() bool { return manager.TimersDisabled("crypto") }, 5*time.Second, 10*time.Millisecond, "timers of the inactive space are disabled after a restart")
	for _, name := range h.cloudTimers() {
		require.False(t, h.timerEnabled(name), name)
	}
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-finished
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 1, created, "a space that is already disabled is not wound down again")
}

func TestPlanReconcilerRecreatesFunctionsDeletedInTheCloudAndIgnoresFailedReadbacks(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.applyFirstPlan(r)
	names := h.cloudTimers()
	require.GreaterOrEqual(t, len(names), 2)
	// 一个函数在云端已被删除，另一个这次读回失败（云端暂时读不到）。
	deleted, unreadable := names[0], names[1]
	h.remove(deleted)
	h.cloud.client.BeforeRead = func(_ string, ref tencentscf.FunctionRef) error {
		if ref.FunctionName == unreadable {
			return errors.New("InternalError: context deadline exceeded")
		}
		return nil
	}
	h.now = h.now.Add(2 * time.Hour)
	created := h.cloud.client.Count("CreateFunction")
	untouched := h.cloud.countOps("ensure", unreadable)
	require.NoError(t, h.reconcile(r))
	require.Equal(t, created+1, h.cloud.client.Count("CreateFunction"), "the deleted function is recreated")
	require.True(t, h.timerEnabled(deleted))
	require.Equal(t, untouched, h.cloud.countOps("ensure", unreadable), "an unreadable function is not touched")
	require.True(t, r.lastVerified.Before(h.now), "an unreadable function keeps the verification open for a retry")
}

func TestPlanReconcilerUpgradesFunctionCodeWhenThePackageChanges(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.applyFirstPlan(r)
	h.cloud.setPackages("pkg", "pkg-new")
	h.now = h.now.Add(2 * time.Hour)
	require.NoError(t, h.reconcile(r))
	h.waitOps(r)
	names := h.cloudTimers()
	require.Equal(t, len(names), h.cloud.countOps("upgrade", ""), "every function still on the old package is upgraded")
	for _, name := range names {
		function := h.mustFunction(name)
		require.Equal(t, "pkg-new", function.Info.Environment["MOOX_CODE_PACKAGE_ID"])
		require.Equal(t, "packages/pkg-new.zip", function.COSObject)
		require.True(t, function.Timer.Enabled, "升级不动定时器")
	}
	before := h.cloud.countOps("upgrade", "")
	h.now = h.now.Add(2 * time.Hour)
	require.NoError(t, h.reconcile(r))
	h.waitOps(r)
	require.Equal(t, before, h.cloud.countOps("upgrade", ""), "functions already on the latest package are left alone")
}

func TestTimerNeedsRepairDetectsMessageAndQualifierDrift(t *testing.T) {
	good := func() *cloud.TimerState {
		return &cloud.TimerState{Timer: cloud.Timer{Cron: "0 * * * * * *", Enabled: true}, AvailableStatus: "Available", Qualifier: timerTriggerQualifier, Message: timerTriggerMessage}
	}
	require.True(t, TimerMatches(good(), "0 * * * * * *"))
	bad := good()
	bad.Message = "wrong"
	require.False(t, TimerMatches(bad, "0 * * * * * *"))
	bad = good()
	bad.Qualifier = "v1"
	require.False(t, TimerMatches(bad, "0 * * * * * *"))
	bad = good()
	bad.AvailableStatus = "Disabled"
	require.False(t, TimerMatches(bad, "0 * * * * * *"))
	require.False(t, TimerMatches(nil, "0 * * * * * *"))
	require.True(t, TimerMatches(nil, ""), "没有 cron 时没有定时器也算一致")
	require.False(t, TimerMatches(good(), ""), "没有 cron 时定时器必须停用")
}

// 一个函数同一时刻只有一个操作：在跑的操作结束之前，同一个函数起不了第二个（不论用途），别的函数不受影响；
// 结束之后取走结果，就能起新的。
func TestAuxJobsTrackedUntilTerminalAndBlockResubmission(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	release := make(chan struct{})
	op, err := r.ops().start("fn-a", opKeyUpgrade, false, func(ctx context.Context, _ *functionOp) error { <-release; return nil })
	require.NoError(t, err)
	_, err = r.ops().start("fn-a", opKeyRepair, true, func(context.Context, *functionOp) error { return nil })
	require.ErrorIs(t, err, errOpBusy)
	other, err := r.ops().start("fn-c", opKeyRepair, true, func(context.Context, *functionOp) error { return nil })
	require.NoError(t, err)
	r.ops().wait(context.Background(), []*functionOp{other}, time.Second)
	require.True(t, r.ops().busy("fn-a"))
	require.Nil(t, r.ops().take("fn-a", opKeyUpgrade), "还没结束的操作取不到结果")

	close(release)
	r.ops().wait(context.Background(), []*functionOp{op}, time.Second)
	require.Nil(t, r.ops().take("fn-a", opKeyRepair), "用途不同的结果不被取走")
	require.NotNil(t, r.ops().take("fn-a", opKeyUpgrade))
	_, err = r.ops().start("fn-a", opKeyRepair, true, func(context.Context, *functionOp) error { return nil })
	require.NoError(t, err)
}

// 计划推进的操作在跑时，读回修复与升级都不碰这个函数：本轮跳过，不算失败，也不宣布核对完成。
func TestInFlightPlanJobsAlsoBlockAuxiliaryChanges(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.applyFirstPlan(r)
	names := h.cloudTimers()
	h.mutate(names[0], func(function *cloudtest.Function) {
		function.Info.Environment["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"] = "tampered"
	})
	release := make(chan struct{})
	_, err := r.ops().start(opKeyOf(names[0]), planOpKey(99, planStageTransition), true, func(ctx context.Context, _ *functionOp) error { <-release; return nil })
	require.NoError(t, err)
	h.now = h.now.Add(2 * time.Hour)
	ensures := h.cloud.countOps("ensure", names[0])
	require.NoError(t, h.reconcile(r))
	require.Equal(t, ensures, h.cloud.countOps("ensure", names[0]), "在跑的操作结束前不起修复")
	require.True(t, r.lastVerified.Before(h.now), "核对没有完成，几分钟后重试")
	close(release)
	h.waitOps(r)
	h.now = h.now.Add(planVerifyRetry)
	require.NoError(t, h.reconcile(r))
	require.Equal(t, ensures+1, h.cloud.countOps("ensure", names[0]))
	require.NotEqual(t, "tampered", h.mustFunction(names[0]).Info.Environment["MOOX_MARKET_FETCH_ASSIGNMENT_HASH"])
}

// 一轮里等操作结束的上限：本轮剩余时间减 5 秒，没有截止时间时用 opWaitMax。
func TestUpgradeContextReservesTimeForPlanProgress(t *testing.T) {
	r := &PlanReconciler{opWaitMax: 50 * time.Second}
	parent, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	require.LessOrEqual(t, r.roundWait(parent), 50*time.Second)
	require.Greater(t, r.roundWait(parent), 49*time.Second)
	short, cancelShort := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShort()
	require.LessOrEqual(t, r.roundWait(short), 5*time.Second)
	require.Equal(t, 50*time.Second, r.roundWait(context.Background()))
}

// 启用定时器的操作还在跑：停用不能宣布完成，否则它结束后定时器又被打开；它结束后才停用并确认。
func TestDisableAllWaitsForInFlightEnablingJobs(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.applyFirstPlan(r)
	target := h.cloudTimers()[0]
	release := make(chan struct{})
	op, err := r.ops().start(opKeyOf(target), opKeyEnable, true, func(ctx context.Context, _ *functionOp) error {
		<-release
		return r.Cloud.SetTimer(ctx, r.functionRef(target, "ap-nanjing"), cloud.Timer{Cron: "0 * * * * * *", Enabled: true}, "pkg")
	})
	require.NoError(t, err)
	waitHolding(t, r, op)
	done, err := r.DisableAll(context.Background())
	require.NoError(t, err)
	require.False(t, done)

	close(release)
	h.waitOps(r)
	done, err = r.DisableAll(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	for _, name := range h.cloudTimers() {
		require.False(t, h.timerEnabled(name), name)
	}
}

// 停用请求发出后，读回仍看到定时器启用（例如被别处重新打开）：不能宣布完成，下一次重新停用、再读回确认。
func TestDisableAllDoesNotTrustAReadbackTakenBeforeInFlightJobsSettle(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.applyFirstPlan(r)
	target := h.cloudTimers()[0]
	var mu sync.Mutex
	disabled, reopened := false, false
	h.cloud.client.BeforeWrite = func(name string, ref tencentscf.FunctionRef) error {
		mu.Lock()
		defer mu.Unlock()
		if name == "EnsureTimerTrigger" && ref.FunctionName == target {
			disabled = true
		}
		return nil
	}
	h.cloud.client.BeforeRead = func(name string, ref tencentscf.FunctionRef) error {
		mu.Lock()
		reopen := name == "GetTimerTrigger" && ref.FunctionName == target && disabled && !reopened
		if reopen {
			reopened = true
		}
		mu.Unlock()
		if reopen {
			// 停用之后、读回之前定时器又被打开。
			h.mutate(target, func(function *cloudtest.Function) { function.Timer.Enabled = true })
		}
		return nil
	}
	done, err := r.DisableAll(context.Background())
	require.NoError(t, err)
	require.False(t, done, "读回看到定时器仍启用")
	done, err = r.DisableAll(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	require.False(t, h.timerEnabled(target))
}

func TestReenableAfterDisableRepushesAnUnfinishedPlan(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	require.NoError(t, h.reconcile(r))
	require.NotNil(t, h.open(), "计划在等待生效边界，尚未收尾")
	for _, name := range h.cloudTimers() {
		require.True(t, h.timerEnabled(name), name)
	}

	done, err := r.DisableAll(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	for _, name := range h.cloudTimers() {
		require.False(t, h.timerEnabled(name), name)
	}
	progress, err := DecodeFetchPlanProgress(h.open().Progress)
	require.NoError(t, err)
	require.Empty(t, progress.Transitioned, "停用后下发确认作废")

	// 重新启用（恢复时空间协程用新的对账器）：原计划的下发确认已作废，函数的定时器被重新打开。
	resumed := h.reconciler()
	require.NoError(t, h.reconcile(resumed))
	for _, name := range h.cloudTimers() {
		require.True(t, h.timerEnabled(name), name)
	}
}

func TestDisableAllInvalidatesConfirmationsBeforeSubmittingDisables(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	require.NoError(t, h.reconcile(r))
	require.NotNil(t, h.open())
	before, err := DecodeFetchPlanProgress(h.open().Progress)
	require.NoError(t, err)
	require.NotEmpty(t, before.Transitioned)

	// 停用定时器时失败（进程崩溃的等价情形）：确认位已经先持久化作废，重启后的对账器重新启用就会重推。
	names := h.cloudTimers()
	recover := h.failWrites("EnsureTimerTrigger", names[0])
	_, err = r.DisableAll(context.Background())
	require.Error(t, err)
	after, err := DecodeFetchPlanProgress(h.open().Progress)
	require.NoError(t, err)
	require.Empty(t, after.Transitioned)
	require.Empty(t, after.Finalized)

	recover()
	require.NoError(t, h.reconcile(h.reconciler()))
	for _, name := range names {
		require.True(t, h.timerEnabled(name), name)
	}
}

func TestPlanReconcilerResolvesTheLatestPackageByNameAndUpgradesWhenANewOneIsUploaded(t *testing.T) {
	h := newPlanHarness(t, 60)
	h.cloud.setPackages("pkg-v1")
	r := h.reconciler()
	require.NoError(t, h.reconcile(r))
	require.Equal(t, "pkg-v1", r.packageID())

	// 上传了新版本：超过刷新间隔后自动解析到新包，落后的函数随之升级，不需要改配置。
	change := h.content(h.open()).Datasets["ds_spot|1m"]
	h.now = time.Unix(change.P, 0).Add(time.Minute)
	require.NoError(t, h.reconcile(r))
	h.cloud.setPackages("pkg-v1", "pkg-v2")
	h.now = h.now.Add(2 * time.Hour)
	require.NoError(t, h.reconcile(r))
	h.waitOps(r)
	require.Equal(t, "pkg-v2", r.packageID())
	for _, name := range h.cloudTimers() {
		require.Equal(t, "pkg-v2", h.mustFunction(name).Info.Environment["MOOX_CODE_PACKAGE_ID"], name)
	}

	// 云端暂时读不到包列表：沿用上一次的结果，不让函数停摆。
	h.cloud.setPackageErr(errors.New("store unavailable"))
	h.now = h.now.Add(2 * time.Hour)
	require.NoError(t, h.reconcile(r))
	require.Equal(t, "pkg-v2", r.packageID())
}

func TestPlanReconcilerRefusesToRunWithoutAnyResolvablePackage(t *testing.T) {
	h := newPlanHarness(t, 60)
	h.cloud.setPackages()
	r := h.reconciler()
	// 没有上传过代码包：不能创建函数，明确报错而不是用空包 ID 创建。
	err := h.reconcile(r)
	require.Error(t, err)
	require.Contains(t, err.Error(), "code package")
	require.Zero(t, h.cloud.client.Count("CreateFunction"))
}

func TestBackfillFunctionFollowsANewlyUploadedPackageWithinTheSameReconciler(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	name := BackfillFunctionName("crypto", "spot", "ap-nanjing")
	h.cloud.setPackages("pkg-v1")

	got, err := r.EnsureBackfillFunction(context.Background(), "spot")
	require.NoError(t, err)
	require.Equal(t, name, got)
	require.Equal(t, "pkg-v1", h.mustFunction(name).Info.Environment["MOOX_CODE_PACKAGE_ID"])

	// 就绪缓存记的是已确认的代码包：上传新版本后，补采函数在同一个对账器里被升级，不需要重启 Collector。
	h.cloud.setPackages("pkg-v1", "pkg-v2")
	h.now = h.now.Add(time.Minute)
	_, err = r.EnsureBackfillFunction(context.Background(), "spot")
	require.NoError(t, err)
	require.Equal(t, "pkg-v2", h.mustFunction(name).Info.Environment["MOOX_CODE_PACKAGE_ID"])
	require.Equal(t, "packages/pkg-v2.zip", h.mustFunction(name).COSObject)

	// 之后不再重复。
	ensures := h.cloud.countOps("ensure", name)
	_, err = r.EnsureBackfillFunction(context.Background(), "spot")
	require.NoError(t, err)
	require.Equal(t, ensures, h.cloud.countOps("ensure", name))
}

// 对账协程与补采协程并发解析包名：查询串行，较早发出的旧结果不会覆盖更新的版本。
func TestPackageResolutionIsSerializedAndNeverRegresses(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.cloud.setPackages("pkg-v1", "pkg-v2")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			require.NoError(t, r.refreshPackage(context.Background()))
		}()
	}
	wg.Wait()
	require.Equal(t, "pkg-v2", r.packageID())
}

// 较早读到旧包的补采调用，等到拿到补采锁时必须按最新解析结果判断，不能把别的调用刚升级好的函数降回旧版本：
// 代码包在操作开始时现取，所以 Ensure 写的一定是最新的包。
func TestBackfillFunctionIsNeverDowngradedByAStaleCaller(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	name := BackfillFunctionName("crypto", "spot", "ap-nanjing")
	h.cloud.setPackages("pkg-v1", "pkg-v2")
	_, err := r.EnsureBackfillFunction(context.Background(), "spot")
	require.NoError(t, err)
	require.Equal(t, "pkg-v2", h.mustFunction(name).Info.Environment["MOOX_CODE_PACKAGE_ID"])

	stale := h.reconciler()
	h.cloud.setPackages("pkg-v1")
	require.NoError(t, stale.refreshPackage(context.Background()))
	require.Equal(t, "pkg-v1", stale.packageID())
	stale.backfillMu.Lock() // 模拟另一次调用正在操作补采函数
	done := make(chan error, 1)
	go func() {
		_, err := stale.EnsureBackfillFunction(context.Background(), "spot")
		done <- err
	}()
	h.cloud.setPackages("pkg-v1", "pkg-v2")
	stale.backfillMu.Unlock()
	require.NoError(t, <-done)
	require.Equal(t, "pkg-v2", h.mustFunction(name).Info.Environment["MOOX_CODE_PACKAGE_ID"], "已在 v2 的补采函数不能被降级")
}

// 访问入口或调用方身份变了（主机改名、密钥轮换）：Ensure 按当前身份改写环境（不再删掉重建），之后不再重复。
func TestBackfillFunctionIsRecreatedWhenAccessIdentityChanges(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	name := BackfillFunctionName("crypto", "spot", "ap-nanjing")
	got, err := r.EnsureBackfillFunction(context.Background(), "spot")
	require.NoError(t, err)
	require.Equal(t, name, got)
	ensures := h.cloud.countOps("ensure", name)
	_, err = r.EnsureBackfillFunction(context.Background(), "spot")
	require.NoError(t, err)
	require.Equal(t, ensures, h.cloud.countOps("ensure", name), "身份一致时不再写")

	r.Access = func(region string) (map[string]string, error) {
		access, err := testSCFAccessEnvironment(region)
		access["MOOX_ACCESS_ID"] = "access@overseas"
		return access, err
	}
	_, err = r.EnsureBackfillFunction(context.Background(), "spot")
	require.NoError(t, err)
	require.Equal(t, ensures+1, h.cloud.countOps("ensure", name), "访问入口变了要重新 Ensure")
	require.Equal(t, "access@overseas", h.mustFunction(name).Info.Environment["MOOX_ACCESS_ID"])
	require.Zero(t, h.cloud.client.Count("DeleteFunction"))
	require.Equal(t, 1, h.cloud.client.Count("CreateFunction"))
}

// 暂停：空间从运行集合里移出后，管理器先停用全部定时器再退出协程，TimersDisabled 随之为真（大小写不敏感）；
// 恢复后重新启动协程并清掉该状态。
func TestPlanManagerReportsTimersDisabledAfterPauseAndRestartsOnResume(t *testing.T) {
	h := newPlanHarness(t, 60)
	var mu sync.Mutex
	paused := false
	manager := &PlanManager{
		Spaces: func(context.Context) ([]string, error) {
			mu.Lock()
			defer mu.Unlock()
			if paused {
				return nil, nil
			}
			return []string{"crypto"}, nil
		},
		Configured: func(context.Context) ([]string, error) { return []string{"crypto"}, nil },
		Factory: func(string) (*PlanReconciler, error) {
			r := h.reconciler()
			r.Now = time.Now
			return r, nil
		},
		Interval: 40 * time.Millisecond, Offset: 5 * time.Millisecond, Timeout: time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { manager.Run(ctx); close(finished) }()
	defer func() { cancel(); <-finished }()

	require.Eventually(t, func() bool { return len(h.cloudTimers()) == 2 }, 5*time.Second, 10*time.Millisecond)
	require.False(t, manager.TimersDisabled("crypto"), "运行中的空间不是停用状态")

	mu.Lock()
	paused = true
	mu.Unlock()
	require.Eventually(t, func() bool { return manager.TimersDisabled("CRYPTO") }, 5*time.Second, 10*time.Millisecond, "停用确认后报告定时器已停用")
	for _, name := range h.cloudTimers() {
		require.False(t, h.timerEnabled(name), "全部函数的定时器已停用")
	}

	mu.Lock()
	paused = false
	mu.Unlock()
	require.Eventually(t, func() bool { return !manager.TimersDisabled("crypto") }, 5*time.Second, 10*time.Millisecond, "恢复后重新运行")
}

func TestEnableAllReenablesTimersAfterDisableWithoutWaitingForRounds(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.applyFirstPlan(r)

	done, err := r.DisableAll(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	for _, name := range h.cloudTimers() {
		require.False(t, h.timerEnabled(name), name)
	}

	// 恢复：一次调用就让全部函数的定时器重新启用，并让下一轮对账立即读回校验。
	resumed := h.reconciler()
	done, err = resumed.EnableAll(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	for _, name := range h.cloudTimers() {
		require.True(t, h.timerEnabled(name), name)
	}
	require.True(t, resumed.lastVerified.IsZero())

	// 已经全部启用时再次调用不再写，也算完成。
	writes := len(h.scf.Writes())
	done, err = resumed.EnableAll(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	require.Len(t, h.scf.Writes(), writes)
}

func TestEnableAllWaitsForInFlightJobs(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.applyFirstPlan(r)
	done, err := r.DisableAll(context.Background())
	require.NoError(t, err)
	require.True(t, done)

	resumed := h.reconciler()
	target := h.cloudTimers()[0]
	release := make(chan struct{})
	_, err = resumed.ops().start(opKeyOf(target), opKeyRepair, true, func(ctx context.Context, _ *functionOp) error { <-release; return nil })
	require.NoError(t, err)
	done, err = resumed.EnableAll(context.Background())
	require.NoError(t, err)
	require.False(t, done, "仍有操作在跑的函数不启用，也不能算完成")
	require.False(t, h.timerEnabled(target))

	close(release)
	h.waitOps(resumed)
	done, err = resumed.EnableAll(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	require.True(t, h.timerEnabled(target))
}

// 恢复采集时必须先升级落后的函数，再启用定时器：环境格式随代码版本变化，不能出现“旧代码读新环境”。
func TestEnableAllUpgradesFunctionCodeBeforeEnablingTimers(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.applyFirstPlan(r)
	done, err := r.DisableAll(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	h.cloud.setPackages("pkg", "pkg-new")
	h.scf.ResetWrites()

	resumed := h.reconciler()
	done, err = resumed.EnableAll(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	writes := h.scf.Writes()
	for _, name := range h.cloudTimers() {
		function := h.mustFunction(name)
		require.Equal(t, "pkg-new", function.Info.Environment["MOOX_CODE_PACKAGE_ID"], name)
		require.True(t, function.Timer.Enabled, name)
		// 启用定时器的请求发出时，函数的代码与包 ID 已经是新包。
		key := cloudtest.Key(scfRef(name))
		for _, write := range writes {
			if strings.HasPrefix(write, "EnsureTimerTrigger "+key+" ") && strings.Contains(write, "enabled=true") {
				require.Contains(t, write, "package_id=pkg-new code=packages/pkg-new.zip", "函数 %s 启用定时器时代码必须已经是新版本", name)
			}
		}
	}
}

// 升级失败时，EnableAll 读回确认仍有函数在旧代码包上：返回 errCodeUpgradePending，一个定时器都不启用；升级成功后才启用。
func TestEnableAllRefusesToEnableWhileFunctionsStayOnTheOldPackage(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.applyFirstPlan(r)
	done, err := r.DisableAll(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	h.cloud.setPackages("pkg", "pkg-new")
	stuck := h.cloudTimers()[0]
	recover := h.failWrites("UpdateFunctionCode", stuck)

	resumed := h.reconciler()
	done, err = resumed.EnableAll(context.Background())
	require.ErrorIs(t, err, errCodeUpgradePending)
	require.False(t, done)
	for _, name := range h.cloudTimers() {
		require.False(t, h.timerEnabled(name), "代码没升级完，函数 %s 的定时器不能被启用", name)
	}
	require.Equal(t, "pkg", h.mustFunction(stuck).Info.Environment["MOOX_CODE_PACKAGE_ID"])

	recover()
	done, err = resumed.EnableAll(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	for _, name := range h.cloudTimers() {
		require.True(t, h.timerEnabled(name), name)
	}
}

// 云上已经不存在的函数没有代码可升级，不能让整个空间永远等下去（交给对账重建）；读回失败则无法确认，同样等待。
func TestUpgradeBeforeEnableIgnoresFunctionsGoneFromTheCloudAndWaitsOnReadbackErrors(t *testing.T) {
	h := newPlanHarness(t, 60)
	r := h.reconciler()
	h.applyFirstPlan(r)
	done, err := r.DisableAll(context.Background())
	require.NoError(t, err)
	require.True(t, done)
	names := h.cloudTimers()
	h.remove(names[0])

	resumed := h.reconciler()
	done, err = resumed.EnableAll(context.Background())
	require.NoError(t, err, "已确认不存在的函数不阻塞启用")
	require.True(t, done)
	require.True(t, h.timerEnabled(names[1]))

	h.cloud.client.BeforeRead = func(_ string, ref tencentscf.FunctionRef) error {
		if ref.FunctionName == names[1] {
			return errors.New("InternalError: context deadline exceeded")
		}
		return nil
	}
	_, err = h.reconciler().EnableAll(context.Background())
	require.ErrorIs(t, err, errCodeUpgradePending, "读不到云上就无法确认代码已经升级")
}

func TestFirstStreamPlanUsesShortLead(t *testing.T) {
	stream := &DesiredState{Functions: map[string]*PlanFunction{"a": {Executor: marketfetch.PlanExecutorStreaming}}}
	require.True(t, firstStreamPlan(stream, nil, nil))
	require.False(t, firstStreamPlan(stream, map[string]*PlanFunction{"a": {}}, nil), "已有计划要走正常提前量")
	require.False(t, firstStreamPlan(&DesiredState{Functions: map[string]*PlanFunction{"a": {}}}, nil, nil), "云函数要创建，不能缩短")
	require.False(t, firstStreamPlan(&DesiredState{}, nil, nil))
}

func TestPlanManagerTriggerWakesWorkerAndKeepsRunContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := &PlanManager{
		Interval: time.Hour,
		Spaces:   func(context.Context) ([]string, error) { return []string{"crypto"}, nil },
	}
	worker := &planWorker{wake: make(chan struct{}, 1), done: make(chan struct{})}
	manager.workers = map[string]*planWorker{"crypto": worker}
	go manager.Run(ctx)
	manager.Trigger("CRYPTO")
	manager.Trigger("crypto")
	select {
	case <-worker.wake:
	case <-time.After(5 * time.Second):
		t.Fatal("没有唤醒空间协程")
	}
	close(worker.done)
}
