package unitwatch

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
	"github.com/stretchr/testify/require"
)

// fakeHost 模拟一台主机：若干单元、每个组件的检查结果，以及可推进的时钟。
type fakeHost struct {
	t          *testing.T
	root       string
	plans      map[string]unitruntime.Plan
	now        time.Time
	calls      []string
	operations []string
	respond    func(component string) (unitruntime.Result, error)
	// tick 是每次检查消耗的时间。
	tick time.Duration
}

func newFakeHost(t *testing.T, units map[string][]string) *fakeHost {
	t.Helper()
	root := t.TempDir()
	h := &fakeHost{t: t, root: root, plans: map[string]unitruntime.Plan{}, now: time.Date(2026, 10, 18, 12, 0, 20, 0, time.UTC)}
	for unit, components := range units {
		path := filepath.Join(root, unit, "current", "runtime.json")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte("{}"), 0o600))
		plan := unitruntime.Plan{HostID: "control", DeploymentRoot: root}
		for _, id := range components {
			plan.Components = append(plan.Components, unitruntime.Component{ID: id})
		}
		h.plans[path] = plan
	}
	require.NoError(t, os.MkdirAll(filepath.Join(root, "run", "logs"), 0o700))
	h.respond = func(string) (unitruntime.Result, error) { return healthy(0), nil }
	return h
}

func healthy(pid int) unitruntime.Result {
	return unitruntime.Result{Components: []unitruntime.Status{{State: "running", PID: pid, Ready: true}}}
}

func (h *fakeHost) options() Options {
	return Options{
		Root: h.root,
		Now:  func() time.Time { return h.now },
		LoadPlan: func(path string) (unitruntime.Plan, error) {
			plan, ok := h.plans[path]
			if !ok {
				return unitruntime.Plan{}, errors.New("unknown plan " + path)
			}
			return plan, nil
		},
		Execute: func(_ context.Context, path, operation string, ids []string, _ unitruntime.Options) (unitruntime.Result, error) {
			require.Contains(h.t, []string{"healthcheck", "status"}, operation)
			require.Len(h.t, ids, 1)
			h.calls = append(h.calls, ids[0])
			h.operations = append(h.operations, operation)
			h.now = h.now.Add(h.tick)
			return h.respond(ids[0])
		},
	}
}

func (h *fakeHost) run() Report {
	h.t.Helper()
	h.calls, h.operations = nil, nil
	report, err := Run(context.Background(), h.options())
	require.NoError(h.t, err)
	return report
}

func outcomes(report Report) map[string]string {
	out := map[string]string{}
	for _, c := range report.Components {
		out[c.Component] = c.Outcome
	}
	return out
}

func controlUnits() map[string][]string {
	return map[string][]string{
		"host":         {"host-gateway", "host-agent"},
		"prod":         {"admin", "eventbus", "monitor", "collector"},
		"old.previous": {"admin"}, // 旧部署留下的目录不参与
	}
}

// 依赖顺序：admin → host-gateway → eventbus → host-agent，然后按单元与计划顺序检查其余组件。
func TestRunChecksInDependencyOrder(t *testing.T) {
	h := newFakeHost(t, controlUnits())
	report := h.run()
	require.Equal(t, []string{"admin", "host-gateway", "eventbus", "host-agent", "monitor", "collector"}, h.calls)
	require.Equal(t, "control", report.HostID)
	for _, c := range report.Components {
		require.Equal(t, OutcomeHealthy, c.Outcome, c.Component)
	}
}

// 上游没运行起来时，其余上游照常检查，下游本轮跳过。
func TestRunSkipsDependentsWhenHeadIsDown(t *testing.T) {
	h := newFakeHost(t, controlUnits())
	h.respond = func(component string) (unitruntime.Result, error) {
		if component == "admin" {
			return unitruntime.Result{Components: []unitruntime.Status{{State: "stopped", Action: "started"}}}, errors.New("release process did not become ready")
		}
		return healthy(0), nil
	}
	report := h.run()
	require.Equal(t, []string{"admin", "host-gateway", "eventbus", "host-agent"}, h.calls)
	got := outcomes(report)
	require.Equal(t, OutcomeStartFailed, got["admin"])
	require.Equal(t, OutcomeSkippedDependency, got["monitor"])
	require.Equal(t, OutcomeSkippedDependency, got["collector"])
}

// 维护锁被占用（发布进行中）：第一次检查就被跳过，其余组件不再检查。
func TestRunStopsWhenMaintenanceLockIsBusy(t *testing.T) {
	h := newFakeHost(t, controlUnits())
	h.respond = func(string) (unitruntime.Result, error) { return unitruntime.Result{Skipped: true}, nil }
	report := h.run()
	require.Equal(t, []string{"admin"}, h.calls)
	for _, c := range report.Components {
		require.Equal(t, OutcomeSkippedLocked, c.Outcome, c.Component)
	}
}

// 发布或初始化被中断留下的持久屏障：整轮跳过，不删除屏障。
func TestRunReportsPersistentBarrier(t *testing.T) {
	h := newFakeHost(t, controlUnits())
	barrier := filepath.Join(h.root, "run", "installation.json")
	require.NoError(t, os.WriteFile(barrier, []byte("{}"), 0o600))
	report := h.run()
	require.Equal(t, "installation", report.Barrier)
	require.Empty(t, h.calls)
	require.FileExists(t, barrier)
}

// 各种检查结果的分类；拉起时附带运行输出日志里的崩溃线索；主机重启清理的残留汇总进报告。
func TestRunClassifiesOutcomesAndCollectsEvidence(t *testing.T) {
	h := newFakeHost(t, map[string][]string{"prod": {"admin", "monitor", "collector", "strategy", "factor-mgr", "cloudnode"}})
	log := "starting\npanic: runtime error: index out of range [3] with length 3\n\ngoroutine 1 [running]:\nfatal error: concurrent map writes\n"
	require.NoError(t, os.WriteFile(filepath.Join(h.root, "run", "logs", "monitor.log"), []byte(log), 0o600))
	h.respond = func(component string) (unitruntime.Result, error) {
		switch component {
		case "monitor":
			return unitruntime.Result{Components: []unitruntime.Status{{State: "running", PID: 42, Ready: true, Action: "started"}},
				BootRecovered: []unitruntime.BootRecovery{{Component: "monitor", Kind: "pid"}}}, nil
		case "collector":
			return unitruntime.Result{Components: []unitruntime.Status{{State: "running", PID: 43, Ready: true, Action: "restarted"}}}, nil
		case "strategy":
			return unitruntime.Result{Components: []unitruntime.Status{{State: "paused"}}}, nil
		case "factor-mgr":
			return unitruntime.Result{Components: []unitruntime.Status{{State: "running", PID: 44}}}, nil
		case "cloudnode":
			return unitruntime.Result{Components: []unitruntime.Status{{State: "draining", PID: 45}}}, nil
		}
		return healthy(1), nil
	}
	report := h.run()
	got := outcomes(report)
	require.Equal(t, OutcomeStarted, got["monitor"])
	require.Equal(t, OutcomeRestarted, got["collector"])
	require.Equal(t, OutcomeSkippedPaused, got["strategy"])
	require.Equal(t, OutcomeNotReady, got["factor-mgr"])
	require.Equal(t, OutcomeSkippedDraining, got["cloudnode"])
	for _, c := range report.Components {
		if c.Component == "monitor" {
			require.Equal(t, []string{"panic: runtime error: index out of range [3] with length 3", "fatal error: concurrent map writes"}, c.Evidence)
			require.Equal(t, 1, c.RecentRestarts)
		}
	}
	require.Equal(t, []unitruntime.BootRecovery{{Component: "monitor", Kind: "pid"}}, report.BootRecovered)
}

// 启动成功后又崩溃：每轮都能拉起成功，按自动拉起次数统计，10 分钟内第 3 次进入持续崩溃；
// 之后每 5 分钟才尝试一次；连续正常 10 分钟判定恢复。
func TestRunCrashLoopBackoffAndRecovery(t *testing.T) {
	h := newFakeHost(t, map[string][]string{"prod": {"collector"}})
	crashing := true
	h.respond = func(string) (unitruntime.Result, error) {
		if crashing {
			return unitruntime.Result{Components: []unitruntime.Status{{State: "running", PID: 7, Ready: true, Action: "started"}}}, nil
		}
		return healthy(7), nil
	}
	component := func(r Report) ComponentReport { return r.Components[0] }

	first := component(h.run())
	require.Equal(t, OutcomeStarted, first.Outcome)
	require.False(t, first.CrashLoop)
	h.now = h.now.Add(time.Minute)
	require.False(t, component(h.run()).CrashLoop)
	h.now = h.now.Add(time.Minute)
	third := component(h.run())
	require.True(t, third.EnteredCrashLoop)
	require.True(t, third.CrashLoop)
	require.Equal(t, 3, third.RecentRestarts)

	h.now = h.now.Add(time.Minute)
	backoff := h.run()
	require.Empty(t, h.calls, "持续崩溃时未到间隔不再检查")
	require.Equal(t, OutcomeSkippedBackoff, component(backoff).Outcome)

	h.now = h.now.Add(4 * time.Minute)
	crashing = false
	retried := component(h.run())
	require.Equal(t, []string{"collector"}, h.calls)
	require.Equal(t, OutcomeHealthy, retried.Outcome)
	require.True(t, retried.CrashLoop, "一次正常不算恢复")

	for i := 0; i < 10; i++ {
		h.now = h.now.Add(time.Minute)
		last := component(h.run())
		if last.Recovered {
			require.False(t, last.CrashLoop)
			return
		}
	}
	t.Fatal("连续正常 10 分钟后应判定恢复")
}

// 软期限：超过之后不再开始新的检查，已经开始的不取消。
func TestRunSoftDeadline(t *testing.T) {
	h := newFakeHost(t, map[string][]string{"prod": {"admin", "monitor", "collector"}})
	h.tick = 3 * time.Minute
	opts := h.options()
	opts.SoftDeadline = 4 * time.Minute
	report, err := Run(context.Background(), opts)
	require.NoError(t, err)
	require.Equal(t, []string{"admin", "monitor"}, h.calls)
	require.Equal(t, OutcomeSkippedDeadline, outcomes(report)["collector"])
}

// 台账：每次拉起都记；持续未就绪只在第一次出现时记一行；正常不记。
func TestRunLedgerRecordsRestartsAndDeduplicatesConditions(t *testing.T) {
	h := newFakeHost(t, map[string][]string{"prod": {"monitor", "collector"}})
	h.respond = func(component string) (unitruntime.Result, error) {
		if component == "monitor" {
			return unitruntime.Result{Components: []unitruntime.Status{{State: "running", PID: 9, Ready: true, Action: "restarted"}}}, nil
		}
		return unitruntime.Result{Components: []unitruntime.Status{{State: "running", PID: 10}}}, nil
	}
	for i := 0; i < 3; i++ {
		report := h.run()
		if i == 2 {
			for _, c := range report.Components {
				if c.Component == "collector" {
					require.Equal(t, int64(120), c.NotReadySeconds)
				}
			}
		}
		h.now = h.now.Add(time.Minute)
	}
	require.Equal(t, 3, ledgerLines(t, filepath.Join(h.root, "run", "watchdog", "monitor.jsonl")))
	require.Equal(t, 1, ledgerLines(t, filepath.Join(h.root, "run", "watchdog", "collector.jsonl")))
	info, err := os.Stat(filepath.Join(h.root, "run", "watchdog", "state.json"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func ledgerLines(t *testing.T, path string) int {
	t.Helper()
	file, err := os.Open(path)
	require.NoError(t, err)
	defer file.Close()
	count := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var entry ledgerEntry
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &entry))
		count++
	}
	return count
}
