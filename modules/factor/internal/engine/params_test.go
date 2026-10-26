package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/factorwire"
)

type paramManagerFake struct {
	mu     sync.Mutex
	states []factorwire.MemberParamState
	err    error
	calls  int
}

func (f *paramManagerFake) GetTaskParams(context.Context, string) ([]factorwire.MemberParamState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.states, f.err
}

func (f *paramManagerFake) set(period int64, window float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states = []factorwire.MemberParamState{{MemberKey: "pmean__var", LastSubmittedPeriod: period, LastRequestID: "r",
		Params: domain.ParamSnapshot{"window": {Default: window}}}}
}

func varMembers() []domain.MemberDef {
	def := domain.MustDefinition("pmean", "pmean", "params:\n  window: {type: int, min: 2, max: 30, default: 5}\noutput: ts_mean(close, window)\n")
	return []domain.MemberDef{
		{Key: "pmean__var", Factor: def, Kind: domain.MemberKindVar, Params: map[string]float64{"window": 5}},
		{Key: "pmean", Factor: def, Kind: domain.MemberKindFixed, Params: map[string]float64{"window": 5}},
	}
}

var (
	paramTask = domain.FactorTask{TaskID: "ftask_a", SpaceID: "crypto", Freq: "1m"}
	t0        = time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
)

func newTestSelector(t *testing.T, manager *paramManagerFake) (*paramSelector, string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "data", "engine", "param_cache.json")
	selector, err := newParamSelector(manager, file, 30*time.Millisecond)
	require.NoError(t, err)
	selector.retry = time.Millisecond
	return selector, file
}

func windowOf(t *testing.T, got map[string]domain.ParamSnapshot, key string) float64 {
	t.Helper()
	snapshot, ok := got[key]
	require.True(t, ok, "成员 %s 应有提交值", key)
	return snapshot.Value("window", "BTC")
}

// 没有可变成员时不读管理端。
func TestSelectSkipsTasksWithoutVarMembers(t *testing.T) {
	manager := &paramManagerFake{}
	selector, _ := newTestSelector(t, manager)
	got, err := selector.Select(context.Background(), paramTask, varMembers()[1:], t0)
	require.NoError(t, err)
	require.Nil(t, got)
	require.Zero(t, manager.calls)
}

// 同一期重试、写出前重启：参数都与第一次开算时相同；写完完成标记后才清掉记录，下一期读最新的提交值。
func TestSelectKeepsParamsForTheSamePeriodUntilDone(t *testing.T) {
	ctx := context.Background()
	manager := &paramManagerFake{}
	manager.set(t0.Unix(), 10)
	selector, file := newTestSelector(t, manager)

	first, err := selector.Select(ctx, paramTask, varMembers(), t0)
	require.NoError(t, err)
	require.Equal(t, 10.0, windowOf(t, first, "pmean__var"))
	require.NotContains(t, first, "pmean", "固定成员不在结果里")

	// 策略在这一期开算之后又提交了新值（同一目标期的重复提交会被管理端拒绝，这里模拟后续更大的目标期读到的情况）。
	manager.set(t0.Unix(), 20)
	retry, err := selector.Select(ctx, paramTask, varMembers(), t0)
	require.NoError(t, err)
	require.Equal(t, 10.0, windowOf(t, retry, "pmean__var"), "同一期重试沿用第一次的值")
	require.Equal(t, 1, manager.calls, "重试不再读管理端")

	// 重启：从文件里恢复进行中记录。
	restarted, err := newParamSelector(manager, file, 30*time.Millisecond)
	require.NoError(t, err)
	again, err := restarted.Select(ctx, paramTask, varMembers(), t0)
	require.NoError(t, err)
	require.Equal(t, 10.0, windowOf(t, again, "pmean__var"), "写出前重启，参数与第一次相同")
	require.Equal(t, 1, manager.calls)

	// 完成标记写成功：清掉记录，下一期读最新值。
	require.NoError(t, restarted.Done(ctx, paramTask.TaskID, t0))
	next := t0.Add(time.Minute)
	manager.set(next.Unix(), 20)
	fresh, err := restarted.Select(ctx, paramTask, varMembers(), next)
	require.NoError(t, err)
	require.Equal(t, 20.0, windowOf(t, fresh, "pmean__var"))
	require.Equal(t, 2, manager.calls)
}

// Done 只清同一期的记录；别的期次的 Done（迟到的重复调用）不会清掉正在计算的记录。
func TestDoneOnlyClearsMatchingPeriod(t *testing.T) {
	ctx := context.Background()
	manager := &paramManagerFake{}
	manager.set(t0.Unix(), 10)
	selector, _ := newTestSelector(t, manager)
	_, err := selector.Select(ctx, paramTask, varMembers(), t0)
	require.NoError(t, err)
	require.NoError(t, selector.Done(ctx, paramTask.TaskID, t0.Add(-time.Minute)))
	manager.set(t0.Unix(), 99)
	got, err := selector.Select(ctx, paramTask, varMembers(), t0)
	require.NoError(t, err)
	require.Equal(t, 10.0, windowOf(t, got, "pmean__var"))
	require.NoError(t, selector.Done(ctx, "ftask_other", t0), "没有记录时什么也不做")
}

// 从未提交过：不进结果，按成员自己的默认参数计算。
func TestSelectNeverSubmittedUsesMemberDefaults(t *testing.T) {
	manager := &paramManagerFake{states: []factorwire.MemberParamState{{MemberKey: "pmean__var"}}}
	selector, _ := newTestSelector(t, manager)
	got, err := selector.Select(context.Background(), paramTask, varMembers(), t0)
	require.NoError(t, err)
	require.Empty(t, got)
}

// 管理端不可达：用缓存的最近一次读到的值，计算不阻塞；缓存重启后还在。没有缓存时按成员默认参数。
func TestSelectFallsBackToCacheWhenManagerIsUnreachable(t *testing.T) {
	ctx := context.Background()
	manager := &paramManagerFake{}
	manager.set(t0.Unix(), 10)
	selector, file := newTestSelector(t, manager)
	var reasons []string
	selector.onStale = func(_, reason string) { reasons = append(reasons, reason) }
	_, err := selector.Select(ctx, paramTask, varMembers(), t0)
	require.NoError(t, err)
	require.NoError(t, selector.Done(ctx, paramTask.TaskID, t0))

	manager.err = errors.New("mgr down")
	next := t0.Add(time.Minute)
	got, err := selector.Select(ctx, paramTask, varMembers(), next)
	require.NoError(t, err)
	require.Equal(t, 10.0, windowOf(t, got, "pmean__var"), "管理端不可达时用缓存值")
	require.Equal(t, []string{"unreachable"}, reasons)
	require.NoError(t, selector.Done(ctx, paramTask.TaskID, next))

	restarted, err := newParamSelector(manager, file, 30*time.Millisecond)
	require.NoError(t, err)
	again, err := restarted.Select(ctx, paramTask, varMembers(), next.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, 10.0, windowOf(t, again, "pmean__var"), "重启后缓存还在")

	// 没有缓存的任务：不可达时按成员默认参数计算，不报错。
	other := domain.FactorTask{TaskID: "ftask_b", SpaceID: "crypto", Freq: "1m"}
	got, err = restarted.Select(ctx, other, varMembers(), next)
	require.NoError(t, err)
	require.Empty(t, got)
}

// 读到的提交值目标期晚于本期（超前）：用缓存的值并告警，不读超前的值。
func TestSelectIgnoresAheadSubmissions(t *testing.T) {
	ctx := context.Background()
	manager := &paramManagerFake{}
	manager.set(t0.Unix(), 10)
	selector, _ := newTestSelector(t, manager)
	var reasons []string
	selector.onStale = func(_, reason string) { reasons = append(reasons, reason) }
	_, err := selector.Select(ctx, paramTask, varMembers(), t0)
	require.NoError(t, err)
	require.NoError(t, selector.Done(ctx, paramTask.TaskID, t0))

	// 追赶：策略已经提交了目标期 t0+5 的参数，这时引擎在算 t0+1。
	manager.set(t0.Add(5*time.Minute).Unix(), 77)
	got, err := selector.Select(ctx, paramTask, varMembers(), t0.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, 10.0, windowOf(t, got, "pmean__var"), "不用超前的提交值")
	require.Equal(t, []string{"ahead"}, reasons)
}

// 租约冲突是错误：这一期不算，也不写进行中记录。
func TestSelectReturnsLeaseConflict(t *testing.T) {
	manager := &paramManagerFake{err: ErrLeaseConflict}
	selector, file := newTestSelector(t, manager)
	_, err := selector.Select(context.Background(), paramTask, varMembers(), t0)
	require.ErrorIs(t, err, ErrLeaseConflict)
	_, statErr := os.Stat(file)
	require.True(t, os.IsNotExist(statErr))
}

// 缓存文件写失败：不能带着没落盘的参数开算，也不留下半截的进行中记录。
func TestSelectFailsWhenCacheCannotBePersisted(t *testing.T) {
	manager := &paramManagerFake{}
	manager.set(t0.Unix(), 10)
	selector, _ := newTestSelector(t, manager)
	blocker := filepath.Join(t.TempDir(), "blocked")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	selector.file = filepath.Join(blocker, "param_cache.json")
	_, err := selector.Select(context.Background(), paramTask, varMembers(), t0)
	require.ErrorContains(t, err, "persist factor param cache")
	require.Empty(t, selector.cache.InFlight)
}

// 缓存文件是原子写：不留下临时文件。
func TestPersistLeavesNoTempFiles(t *testing.T) {
	manager := &paramManagerFake{}
	manager.set(t0.Unix(), 10)
	selector, file := newTestSelector(t, manager)
	_, err := selector.Select(context.Background(), paramTask, varMembers(), t0)
	require.NoError(t, err)
	entries, err := os.ReadDir(filepath.Dir(file))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "param_cache.json", entries[0].Name())
}

// 引擎重置清掉取参缓存：管理端的提交已一并清除，重置后不能再复用旧的进行中记录或最近一次读到的值，重启后也一样。
func TestResetClearsParamCache(t *testing.T) {
	ctx := context.Background()
	manager := &paramManagerFake{}
	manager.set(t0.Unix(), 10)
	selector, file := newTestSelector(t, manager)
	_, err := selector.Select(ctx, paramTask, varMembers(), t0)
	require.NoError(t, err)
	require.NoError(t, selector.Reset())

	manager.states = nil
	manager.err = errors.New("mgr down")
	got, err := selector.Select(ctx, paramTask, varMembers(), t0)
	require.NoError(t, err)
	require.Empty(t, got, "重置后管理端不可达也不能再用旧提交值")

	restarted, err := newParamSelector(manager, file, 30*time.Millisecond)
	require.NoError(t, err)
	require.NoError(t, restarted.Reset())
	var nilSelector *paramSelector
	require.NoError(t, nilSelector.Reset())
}
