package factorudf

import (
	"context"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func faultyBatch(window []float64) *Batch {
	return &Batch{NSeries: 1, InLen: len(window), OutLen: 1, Inputs: [][]float64{window}, Params: []float64{float64(len(window))}, Out: make([]float64, 1)}
}

func window(n int, fill float64) []float64 {
	w := make([]float64, n)
	for i := range w {
		w[i] = fill + float64(i)
	}
	return w
}

// 死循环被截止时间中断，实例被丢弃，下一次调用用新实例。
func TestInfiniteLoopIsInterruptedAndInstanceDiscarded(t *testing.T) {
	rt := newTestRuntime(t, Options{Limits: Limits{CallTimeout: 300 * time.Millisecond}})
	m, err := rt.Load(context.Background(), testWasm(t, "faulty"))
	require.NoError(t, err)
	ctx := context.Background()

	w := window(5, 1)
	w[2] = -7777777.0
	started := time.Now()
	err = m.Eval(ctx, faultyBatch(w))
	var ce *CallError
	require.True(t, errors.As(err, &ce), "%v", err)
	require.Equal(t, ReasonTimeout, ce.Reason)
	require.Less(t, time.Since(started), 5*time.Second)
	require.Equal(t, PoolStats{Total: 0, Idle: 0}, m.Stats(), "出错的实例被丢弃")

	good := faultyBatch(window(5, 1))
	require.NoError(t, m.Eval(ctx, good))
	require.Equal(t, 3.0, good.Out[0])
	require.Equal(t, PoolStats{Total: 1, Idle: 1}, m.Stats(), "下一次调用用新实例，并归还")
}

func TestPanicIsATrapNotAProcessCrash(t *testing.T) {
	rt := newTestRuntime(t, Options{})
	m, err := rt.Load(context.Background(), testWasm(t, "faulty"))
	require.NoError(t, err)
	w := window(4, 1)
	w[1] = -8888888.0
	err = m.Eval(context.Background(), faultyBatch(w))
	var ce *CallError
	require.True(t, errors.As(err, &ce), "%v", err)
	require.Equal(t, ReasonTrap, ce.Reason)
	// 引擎进程不受影响：同一个模块立刻还能正常计算。
	require.NoError(t, m.Eval(context.Background(), faultyBatch(window(4, 1))))
}

func TestMemoryRunawayHitsTheInstanceLimit(t *testing.T) {
	rt := newTestRuntime(t, Options{Limits: Limits{MemoryLimitPages: 64}}) // 4 MiB
	m, err := rt.Load(context.Background(), testWasm(t, "faulty"))
	require.NoError(t, err)
	w := window(4, 1)
	w[0] = -9999999.0
	err = m.Eval(context.Background(), faultyBatch(w))
	var ce *CallError
	require.True(t, errors.As(err, &ce), "%v", err)
	require.Equal(t, ReasonTrap, ce.Reason)
	require.NoError(t, m.Eval(context.Background(), faultyBatch(window(4, 1))))
}

func TestInvalidParamsAreRejectedByTheModule(t *testing.T) {
	rt := newTestRuntime(t, Options{})
	m, err := rt.Load(context.Background(), testWasm(t, "ts_mean"))
	require.NoError(t, err)
	b := &Batch{NSeries: 1, InLen: 3, OutLen: 1, Inputs: [][]float64{{1, 2, 3}}, Params: []float64{9999999}, Out: make([]float64, 1)}
	err = m.Eval(context.Background(), b)
	var ce *CallError
	require.True(t, errors.As(err, &ce))
	require.Equal(t, ReasonCode, ce.Reason)
	require.EqualValues(t, 1, ce.Code)
	// 窗口长度与参数不一致。
	b = &Batch{NSeries: 1, InLen: 4, OutLen: 1, Inputs: [][]float64{{1, 2, 3, 4}}, Params: []float64{3}, Out: make([]float64, 1)}
	err = m.Eval(context.Background(), b)
	require.True(t, errors.As(err, &ce))
	require.EqualValues(t, 2, ce.Code)
}

func TestBatchInputValidation(t *testing.T) {
	rt := newTestRuntime(t, Options{})
	m, err := rt.Load(context.Background(), testWasm(t, "ts_mean"))
	require.NoError(t, err)
	bad := []*Batch{
		{NSeries: 1, InLen: 3, OutLen: 1, Inputs: [][]float64{{1, 2, 3}, {1, 2, 3}}, Params: []float64{3}, Out: make([]float64, 1)}, // 输入个数
		{NSeries: 1, InLen: 3, OutLen: 1, Inputs: [][]float64{{1, 2}}, Params: []float64{3}, Out: make([]float64, 1)},               // 输入长度
		{NSeries: 1, InLen: 3, OutLen: 1, Inputs: [][]float64{{1, 2, 3}}, Params: nil, Out: make([]float64, 1)},                     // 参数
		{NSeries: 1, InLen: 3, OutLen: 2, Inputs: [][]float64{{1, 2, 3}}, Params: []float64{3}, Out: make([]float64, 1)},            // 输出长度
		{NSeries: 1, InLen: 2, OutLen: 3, Inputs: [][]float64{{1, 2}}, Params: []float64{3}, Out: make([]float64, 3)},               // 输入比输出短
	}
	for i, b := range bad {
		var ce *CallError
		require.True(t, errors.As(m.Eval(context.Background(), b), &ce), i)
		require.Equal(t, ReasonInput, ce.Reason, i)
	}
}

// 输出的 NaN 被规范化，±Inf 记为 NaN。
func TestOutputIsNormalized(t *testing.T) {
	rt := newTestRuntime(t, Options{})
	m, err := rt.Load(context.Background(), testWasm(t, "ts_mean"))
	require.NoError(t, err)
	b := &Batch{NSeries: 1, InLen: 3, OutLen: 1, Inputs: [][]float64{{math.NaN(), 2, 3}}, Params: []float64{3}, Out: make([]float64, 1)}
	require.NoError(t, m.Eval(context.Background(), b))
	require.Equal(t, math.Float64bits(canonicalNaN), math.Float64bits(b.Out[0]))
}

type countingBudget struct {
	mu    sync.Mutex
	limit int64
	used  int64
	peak  int64
}

func (b *countingBudget) Acquire(n int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used+n > b.limit {
		return errors.New("预算不足")
	}
	b.used += n
	b.peak = max(b.peak, b.used)
	return nil
}

func (b *countingBudget) Release(n int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.used -= n
}

func (b *countingBudget) get() (used, peak int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used, b.peak
}

// 预算：并发创建与复用时总量不超过预算；超预算拒绝；关闭后释放。
func TestBudgetAdmissionAndRelease(t *testing.T) {
	budget := &countingBudget{limit: 6 << 20}
	rt, err := NewRuntime(context.Background(), Options{Budget: budget, Limits: Limits{PoolSize: 16}})
	require.NoError(t, err)
	ctx := context.Background()
	m, err := rt.Load(ctx, testWasm(t, "ts_mean"))
	require.NoError(t, err)

	var wg sync.WaitGroup
	var rejected atomic.Int32
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b := &Batch{NSeries: 1, InLen: 4, OutLen: 2, Inputs: [][]float64{{1, 2, 3, 4}}, Params: []float64{3}, Out: make([]float64, 2)}
			if err := m.Eval(ctx, b); err != nil {
				require.ErrorIs(t, err, ErrBudget)
				rejected.Add(1)
			}
		}()
	}
	wg.Wait()
	used, peak := budget.get()
	require.LessOrEqual(t, peak, budget.limit, "任何时刻总量不超过预算")
	require.Positive(t, used, "空闲实例仍占用预算")

	// 一次需要超大暂存区的调用：被预算拒绝，不动内存。
	big := &Batch{NSeries: 1, InLen: 1 << 20, OutLen: 1, Inputs: [][]float64{make([]float64, 1<<20)}, Params: []float64{1 << 19}, Out: make([]float64, 1)}
	err = m.Eval(ctx, big)
	require.ErrorIs(t, err, ErrBudget)

	require.NoError(t, rt.Close(ctx))
	used, _ = budget.get()
	require.Zero(t, used, "关闭后全部释放")
}

func TestPoolIsBoundedAndWaitersAreServed(t *testing.T) {
	rt := newTestRuntime(t, Options{Limits: Limits{PoolSize: 2}})
	m, err := rt.Load(context.Background(), testWasm(t, "ts_mean"))
	require.NoError(t, err)
	ctx := context.Background()
	a, err := m.Acquire(ctx)
	require.NoError(t, err)
	b, err := m.Acquire(ctx)
	require.NoError(t, err)

	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	_, err = m.Acquire(short)
	require.ErrorIs(t, err, context.DeadlineExceeded, "达到上限后借出方等待")

	got := make(chan *Instance, 1)
	go func() {
		in, _ := m.Acquire(ctx)
		got <- in
	}()
	time.Sleep(20 * time.Millisecond)
	m.Release(ctx, a)
	select {
	case in := <-got:
		require.Same(t, a, in)
	case <-time.After(2 * time.Second):
		t.Fatal("归还后等待者应拿到实例")
	}
	m.Discard(ctx, b)
	require.Equal(t, 1, m.Stats().Total)
}

func TestIdleInstancesAreClosed(t *testing.T) {
	budget := &countingBudget{limit: 1 << 30}
	rt, err := NewRuntime(context.Background(), Options{Budget: budget, Limits: Limits{IdleTimeout: time.Minute}})
	require.NoError(t, err)
	defer rt.Close(context.Background())
	ctx := context.Background()
	m, err := rt.Load(ctx, testWasm(t, "ts_mean"))
	require.NoError(t, err)
	in, err := m.Acquire(ctx)
	require.NoError(t, err)
	m.Release(ctx, in)
	require.Equal(t, 0, m.Reap(ctx, time.Now()))
	require.Equal(t, 1, m.Reap(ctx, time.Now().Add(2*time.Minute)))
	require.Equal(t, PoolStats{}, m.Stats())
	used, _ := budget.get()
	require.Zero(t, used)
}

func TestSameHashLoadsOnce(t *testing.T) {
	rt := newTestRuntime(t, Options{})
	a, err := rt.Load(context.Background(), testWasm(t, "ts_mean"))
	require.NoError(t, err)
	b, err := rt.Load(context.Background(), testWasm(t, "ts_mean"))
	require.NoError(t, err)
	require.Same(t, a, b)
}

func TestBatchCapacity(t *testing.T) {
	// floor(预算 / (8 × (输入个数 × 输入长 + 参数个数 + 输出长)))
	n, err := BatchCapacity(64<<20, 1, 1, 1000, 100)
	require.NoError(t, err)
	require.Equal(t, (64<<20)/(8*(1000+1+100)), n)
	n, err = BatchCapacity(1<<20, 2, 3, 40, 10)
	require.NoError(t, err)
	require.Equal(t, (1<<20)/(8*(2*40+3+10)), n)
	_, err = BatchCapacity(1<<10, 1, 1, 1<<20, 1)
	require.ErrorContains(t, err, "超过批字节上限")
	_, err = BatchCapacity(1<<30, 8, 16, math.MaxInt32, math.MaxInt32)
	require.Error(t, err, "单条放不下")
	_, err = BatchCapacity(1<<30, 8, 16, math.MaxInt64>>8, 1)
	require.Error(t, err, "乘法溢出被检查")
}

func TestGroupByLAndSplit(t *testing.T) {
	calls := []SeriesCall{{L: 5}, {L: 3}, {L: 5}, {L: 3}, {L: 7}}
	groups := GroupByL(calls)
	require.Equal(t, []Group{{L: 3, Series: []int{1, 3}}, {L: 5, Series: []int{0, 2}}, {L: 7, Series: []int{4}}}, groups, "L 不同不混批，组内保持顺序")
	require.Equal(t, [][]int{{0, 1}, {2, 3}, {4}}, SplitBatches([]int{0, 1, 2, 3, 4}, 2))
	require.Nil(t, SplitBatches([]int{0}, 0))
}

// 一致性测试能抓到问题：带全局状态的模块、读窗口外（过去与未来）的模块。
func TestConformanceCatchesBadModules(t *testing.T) {
	rt := newTestRuntime(t, Options{})
	cases := []struct {
		module string
		failed []string
	}{
		{"bad_state", []string{testChunk, testBatch, testDeterminism}},
		{"bad_past", []string{testChunk}},
		{"bad_future", []string{testFuture}},
	}
	for _, tc := range cases {
		t.Run(tc.module, func(t *testing.T) {
			m, err := rt.Load(context.Background(), testWasm(t, tc.module))
			require.NoError(t, err, "能通过静态模块检查，要靠一致性测试拦住")
			rep, err := RunConformance(context.Background(), rt, m, nil, ConformanceOptions{})
			require.NoError(t, err)
			require.False(t, rep.Passed)
			var failed []string
			for _, f := range rep.Failures() {
				failed = append(failed, f.Name)
			}
			for _, want := range tc.failed {
				require.Contains(t, failed, want, "%+v", rep.Failures())
			}
		})
	}
}

func TestConformanceHonoursTotalTimeout(t *testing.T) {
	rt := newTestRuntime(t, Options{})
	m, err := rt.Load(context.Background(), testWasm(t, "ema_trunc"))
	require.NoError(t, err)
	rep, err := RunConformance(context.Background(), rt, m, nil, ConformanceOptions{Timeout: time.Millisecond})
	require.NoError(t, err)
	require.False(t, rep.Passed)
	require.Contains(t, rep.Failures()[0].Detail, "总时限")
}

// 同一个 wasm 的测试输出摘要是稳定的（回归检查：CI amd64 与开发机 arm64 必须相同）。
func TestConformanceDigestIsStable(t *testing.T) {
	digests := map[string]string{}
	for i := 0; i < 2; i++ {
		rt := newTestRuntime(t, Options{})
		m, err := rt.Load(context.Background(), testWasm(t, "ts_mean"))
		require.NoError(t, err)
		rep, err := RunConformance(context.Background(), rt, m, loadFixtures(t, "ts_mean"), ConformanceOptions{})
		require.NoError(t, err)
		require.True(t, rep.Passed)
		if prev, ok := digests["ts_mean"]; ok {
			require.Equal(t, prev, rep.Digest)
		}
		digests["ts_mean"] = rep.Digest
	}
}
