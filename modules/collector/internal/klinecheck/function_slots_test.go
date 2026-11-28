package klinecheck

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/stretchr/testify/require"
)

// 同一个补采函数的分块共用一个计数：补齐最多占到预留并发 − 1，补缺口（含强制重拉）能占满预留并发；预留并发为 1 时补齐也给 1。
func TestFunctionSlotsAreSharedBySeedAndGapChunks(t *testing.T) {
	for _, reserved := range []int{1, 2, 5} {
		t.Run(fmt.Sprintf("预留并发%d", reserved), func(t *testing.T) {
			b := &Backfiller{FunctionConcurrency: func(string) int { return reserved }}
			key := functionKey("stockus", "equity", "ap-nanjing")
			limit := b.functionLimit("stockus")
			seedLimit := SeedFunctionLimit(limit)
			require.Equal(t, reserved, limit)
			require.Equal(t, max(reserved-1, 1), seedLimit)

			// 补齐先占满它那一份：补缺口仍有 1 个名额（预留并发为 1 时没有可留的）。
			for range seedLimit {
				require.True(t, b.acquireFunction(key, seedLimit))
			}
			require.False(t, b.acquireFunction(key, seedLimit), "补齐最多占到预留并发 − 1")
			if reserved > 1 {
				require.True(t, b.acquireFunction(key, limit), "补齐占满它那一份时补缺口仍能拿到名额")
			}
			require.False(t, b.acquireFunction(key, limit), "合计不超过预留并发")
			require.Equal(t, reserved, b.functionBusy[key])
			for range reserved {
				b.releaseFunction(key)
			}
			require.Zero(t, b.functionBusy[key])

			// 补缺口先占着 1 个：补齐少跑 1 个，补缺口还能把剩下的占满。
			require.True(t, b.acquireFunction(key, limit))
			seeds := 0
			for b.acquireFunction(key, seedLimit) {
				seeds++
			}
			require.Equal(t, max(seedLimit-1, 0), seeds, "补缺口占着名额时补齐少跑")
			gaps := 1
			for b.acquireFunction(key, limit) {
				gaps++
			}
			require.Equal(t, reserved, seeds+gaps)
		})
	}

	b := &Backfiller{}
	require.Equal(t, DefaultMaxInFlight, b.functionLimit("crypto"), "没配用默认值")
	key := functionKey("crypto", "spot", "ap-singapore")
	require.True(t, b.acquireFunction(key, 1))
	require.False(t, b.acquireFunction(key, 1), "占满了")
	require.True(t, b.acquireFunction(functionKey("crypto", "swap", "ap-singapore"), 1), "同地域不同市场是另一个函数")
	b.releaseFunction(key)
	b.releaseFunction(key)
	require.Zero(t, b.functionBusy[key], "归还不会减成负数")
}

// 补齐占满它那一份名额时（2026-11-25 美股补齐占满南京补采函数）：补缺口的分块照常调用补采函数，
// 补齐分块拿不到名额就退回待执行、不算失败；补齐的分块跑完归还后它接着补完。
func TestGapChunksKeepASlotWhileSeedFillsItsShare(t *testing.T) {
	b, invoker, rows, base := seedFixture(t, 2*time.Hour, 5, 12, 20)
	b.Refill = false
	ctx := context.Background()
	require.NoError(t, b.SeedTick(ctx, "crypto"))
	b.Wait()
	seed := seedOf(t, b)
	require.Equal(t, store.SeedRunning, seed.Status)

	key := functionKey("crypto", "spot", "ap-nanjing")
	share := SeedFunctionLimit(DefaultMaxInFlight)
	for range share {
		require.True(t, b.acquireFunction(key, share))
	}
	rows.mu.Lock()
	rows.missing["ETH-USDT"][base.Add(30*time.Minute).Unix()] = true
	rows.mu.Unlock()
	gapJob, chunks, err := b.Submit(ctx, JobRequest{SpaceID: "crypto", SubjectID: "ETH-USDT", From: base, To: base.Add(time.Hour)}, store.BackfillSourceAuto)
	require.NoError(t, err)
	require.Equal(t, 1, chunks)

	require.NoError(t, b.Tick(ctx, "crypto"))
	b.Wait()
	require.Len(t, invoker.events, 1, "只有补缺口的分块调用了补采函数")
	item := invoker.events[0]["data"].(map[string]any)["items"].([]any)[0].(map[string]any)
	require.Equal(t, "ETH-USDT", item["subject_id"])
	require.NoError(t, b.Tick(ctx, "crypto"))
	b.Wait()
	status, err := b.Status(ctx, gapJob)
	require.NoError(t, err)
	require.Equal(t, store.BackfillSucceeded, status.Job.Status)
	counts, err := b.Store.ChunkStatusCounts(ctx, seed.JobIDs[0])
	require.NoError(t, err)
	require.Equal(t, 3, counts[store.BackfillQueued], "补齐分块退回待执行")
	require.Zero(t, counts[store.BackfillFailed], "拿不到名额不算失败")
	require.Equal(t, share, b.functionBusy[key], "补缺口跑完归还了名额")

	for range share {
		b.releaseFunction(key)
	}
	require.NoError(t, b.Tick(ctx, "crypto"))
	b.Wait()
	require.NoError(t, b.Tick(ctx, "crypto"))
	require.NoError(t, b.SeedTick(ctx, "crypto"))
	require.Equal(t, store.SeedDone, seedOf(t, b).Status)
	require.Len(t, invoker.events, 4)
}

// 补缺口的分块占着名额时补齐少跑：预留并发 5、补缺口占 2 个，补齐 3 块只放 2 块（合计到 4），第 3 块退回。
func TestSeedRunsFewerChunksWhileGapChunksHoldTheFunction(t *testing.T) {
	b, invoker, _, _ := seedFixture(t, 2*time.Hour, 5, 12, 20)
	b.Refill = false
	// 认领不设限，只看函数名额。
	b.Seed.Slots = func(context.Context, string) int { return DefaultMaxInFlight }
	gate := &gatedInvoker{next: invoker, gate: make(chan struct{})}
	b.Invoker = gate
	ctx := context.Background()
	require.NoError(t, b.SeedTick(ctx, "crypto"))
	b.Wait()
	seed := seedOf(t, b)

	key := functionKey("crypto", "spot", "ap-nanjing")
	require.True(t, b.acquireFunction(key, DefaultMaxInFlight))
	require.True(t, b.acquireFunction(key, DefaultMaxInFlight))
	require.NoError(t, b.Tick(ctx, "crypto"))
	require.Eventually(t, func() bool {
		counts, err := b.Store.ChunkStatusCounts(ctx, seed.JobIDs[0])
		return err == nil && gate.started.Load() == 2 && counts[store.BackfillQueued] == 1
	}, 5*time.Second, 10*time.Millisecond, "两块在调用补采函数，一块退回")
	b.mu.Lock()
	busy := b.functionBusy[key]
	b.mu.Unlock()
	require.Equal(t, SeedFunctionLimit(DefaultMaxInFlight), busy)

	close(gate.gate)
	b.Wait()
	require.Equal(t, int32(2), gate.started.Load())
	require.Equal(t, 2, b.functionBusy[key], "补齐分块跑完归还，补缺口的两个还占着")
}

// 补采函数占满时补缺口的分块退回待执行、退回这次尝试、不触发补位，归还名额后下一轮照常执行。
func TestGapChunkRequeuesWhenTheFunctionIsFull(t *testing.T) {
	b, invoker, _, base := newBackfillFixture(t)
	ctx := context.Background()
	jobID, chunks, err := b.Submit(ctx, JobRequest{SpaceID: "crypto", SubjectID: "BTC-USDT", From: base, To: base.Add(time.Hour)}, store.BackfillSourceAuto)
	require.NoError(t, err)
	require.Equal(t, 1, chunks)
	key := functionKey("crypto", "spot", "ap-nanjing")
	for range DefaultMaxInFlight {
		require.True(t, b.acquireFunction(key, DefaultMaxInFlight))
	}

	chunk := claimOne(t, b, jobID)
	require.False(t, b.runChunk(ctx, "crypto", chunk, false, false), "退回不触发补位")
	require.Empty(t, invoker.events)
	_, rows, err := b.Store.GetJob(ctx, jobID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, store.BackfillQueued, rows[0].Status)
	require.Zero(t, rows[0].Attempts, "退回这次尝试")
	require.Equal(t, DefaultMaxInFlight, b.functionBusy[key])

	b.releaseFunction(key)
	require.NoError(t, b.Tick(ctx, "crypto"))
	b.Wait()
	require.Len(t, invoker.events, 1)
	require.Equal(t, DefaultMaxInFlight-1, b.functionBusy[key], "跑完归还")
}

// 分块不论怎样结束都归还函数名额：成功、函数报失败、调用结果不明（上下文取消）、调用崩溃、补齐分块等写入速率时上下文取消。
func TestFunctionSlotIsReleasedHoweverTheChunkEnds(t *testing.T) {
	cases := []struct {
		name   string
		seed   bool
		setup  func(b *Backfiller, invoker *slotInvoker, functions *slotFunctions)
		panics bool
		invoke bool
	}{
		{name: "成功", invoke: true},
		{name: "函数报失败", invoke: true, setup: func(_ *Backfiller, invoker *slotInvoker, _ *slotFunctions) {
			invoker.next.(*healingInvoker).fail = true
		}},
		{name: "调用结果不明", invoke: true, setup: func(_ *Backfiller, invoker *slotInvoker, _ *slotFunctions) {
			invoker.err = context.Canceled
		}},
		{name: "调用崩溃", invoke: true, panics: true, setup: func(_ *Backfiller, invoker *slotInvoker, _ *slotFunctions) {
			invoker.panic = true
		}},
		{name: "补齐分块等写入速率时上下文取消", seed: true, setup: func(b *Backfiller, _ *slotInvoker, functions *slotFunctions) {
			b.Seed.RowsPerSecond = 1
			functions.cancelOnEnsure = true
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, healing, _, base := newBackfillFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			key := functionKey("crypto", "spot", "ap-nanjing")
			functions := &slotFunctions{b: b, key: key, cancel: cancel}
			invoker := &slotInvoker{b: b, key: key, next: healing}
			b.Functions = func(string) FunctionProvider { return functions }
			b.Invoker = invoker
			if tc.setup != nil {
				tc.setup(b, invoker, functions)
			}
			jobID, _, err := b.Submit(context.Background(), JobRequest{SpaceID: "crypto", SubjectID: "BTC-USDT", From: base, To: base.Add(time.Hour)}, store.BackfillSourceAuto)
			require.NoError(t, err)
			chunk := claimOne(t, b, jobID)

			var recovered any
			func() {
				defer func() { recovered = recover() }()
				b.runChunk(ctx, "crypto", chunk, false, tc.seed)
			}()
			require.Equal(t, tc.panics, recovered != nil)
			require.Equal(t, []int{1}, functions.busy, "调用补采函数前占了名额")
			if tc.invoke {
				require.Equal(t, []int{1}, invoker.busy, "调用期间占着名额")
			} else {
				require.Empty(t, invoker.busy)
			}
			require.Zero(t, b.functionBusy[key], "结束后归还")
		})
	}
}

func claimOne(t *testing.T, b *Backfiller, jobID string) store.BackfillChunk {
	t.Helper()
	claimed, err := b.Store.ClaimChunks(context.Background(), jobID, 1, b.now().Add(chunkInvokeLimit+chunkSafetyMargin))
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	return claimed[0]
}

func (b *Backfiller) functionBusyOf(key string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.functionBusy[key]
}

// slotFunctions 在保证函数存在时记下函数名额的占用数，cancelOnEnsure 时顺带取消分块的上下文。
type slotFunctions struct {
	b              *Backfiller
	key            string
	cancel         context.CancelFunc
	cancelOnEnsure bool
	busy           []int
}

func (f *slotFunctions) BackfillRegion(string) (string, bool) { return "ap-nanjing", true }

func (f *slotFunctions) BackfillRegions(string) []string { return nil }

func (f *slotFunctions) EnsureBackfillFunctionIn(context.Context, string, string) (string, error) {
	f.busy = append(f.busy, f.b.functionBusyOf(f.key))
	if f.cancelOnEnsure {
		f.cancel()
	}
	return "moox-fetch-plan-backfill-crypto-spot-nanjing", nil
}

// slotInvoker 在调用时记下函数名额的占用数，可让调用崩溃或返回错误，其余交给 next。
type slotInvoker struct {
	b     *Backfiller
	key   string
	next  Invoker
	err   error
	panic bool
	busy  []int
}

func (s *slotInvoker) Invoke(ctx context.Context, spaceID, functionName string, event map[string]any) (marketfetch.InvocationResult, error) {
	s.busy = append(s.busy, s.b.functionBusyOf(s.key))
	if s.panic {
		panic("补采函数调用崩溃")
	}
	if s.err != nil {
		return marketfetch.InvocationResult{}, s.err
	}
	return s.next.Invoke(ctx, spaceID, functionName, event)
}

// gatedInvoker 让调用停在 gate 上，直到测试放行，用来看同时在跑的分块数。
type gatedInvoker struct {
	next    Invoker
	gate    chan struct{}
	started atomic.Int32
}

func (g *gatedInvoker) Invoke(ctx context.Context, spaceID, functionName string, event map[string]any) (marketfetch.InvocationResult, error) {
	g.started.Add(1)
	<-g.gate
	return g.next.Invoke(ctx, spaceID, functionName, event)
}
