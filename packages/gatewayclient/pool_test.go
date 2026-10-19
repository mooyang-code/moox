package gatewayclient

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/pool/connpool"
)

// 客户端空闲超时必须大于最长的调用间隔（采集函数每分钟一次），否则连接会在两次调用之间被回收，每分钟重建。
func TestClientIdleTimeoutCoversTheCollectionInterval(t *testing.T) {
	require.Greater(t, clientIdleTimeout, 2*time.Minute)
	require.Less(t, clientIdleTimeout, clientMaxLifetime)
}

func TestPoolConfigValidate(t *testing.T) {
	require.NoError(t, PoolConfig{}.Validate())
	require.NoError(t, PoolConfig{MinIdle: 16, MaxIdle: 32}.Validate())
	require.NoError(t, PoolConfig{MinIdle: 16}.Validate(), "MaxIdle 省略时使用默认值")
	require.NoError(t, PoolConfig{MinIdle: 32}.Validate())
	require.Error(t, PoolConfig{MinIdle: 64}.Validate(), "超过默认 max_idle(32) 会形成补建后关闭的循环")
	require.NoError(t, PoolConfig{MinIdle: 64, MaxIdle: 64}.Validate())
	require.Error(t, PoolConfig{MinIdle: -1}.Validate())
	require.Error(t, PoolConfig{MinIdle: 65}.Validate())
	require.Error(t, PoolConfig{MaxIdle: 129}.Validate())
	require.Error(t, PoolConfig{MinIdle: 16, MaxIdle: 8}.Validate())
	require.Error(t, FileConfig{Caller: "access", KeyFile: "k", Pool: PoolConfig{MinIdle: 100}}.Validate())
}

func TestTunedSettingsAndLegacyDefaults(t *testing.T) {
	tuned := tunedPoolSettings(PoolConfig{MinIdle: 16})
	require.Equal(t, 32, tuned.maxIdle)
	require.Equal(t, 16, tuned.minIdle)
	require.Equal(t, clientIdleTimeout, tuned.idleTimeout)
	require.True(t, tuned.fastConnect)
	require.Equal(t, 64, tunedPoolSettings(PoolConfig{MaxIdle: 64}).maxIdle)
	legacy := legacyPoolSettings()
	require.Equal(t, 8, legacy.maxIdle)
	require.Zero(t, legacy.idleTimeout)
	require.False(t, legacy.fastConnect)
}

func TestConnectBudgetFitsInsideTheCallBudget(t *testing.T) {
	// PrimaryStore 的调用上限是 5 秒：两次拨号加发送必须放得下。
	first := connectBudget(5*time.Second, 0)
	require.Equal(t, 2*time.Second, first)
	second := connectBudget(5*time.Second-first, 1)
	require.Equal(t, 2*time.Second, second)
	require.LessOrEqual(t, first+second+retryReserve, 5*time.Second+retryReserve)
	require.Equal(t, maxConnectTimeout, connectBudget(time.Minute, 0), "长预算也不超过上限")
	require.Equal(t, maxConnectTimeout, connectBudget(time.Minute, 1))
	require.Zero(t, connectBudget(500*time.Millisecond, 1), "剩余不足时没有拨号预算")
}

func connectFailure(ctx context.Context) error {
	markConnectFailure(ctx)
	return errs.NewFrameError(errs.RetClientConnectFail, "tcp client transport connection pool: dial failed")
}

// 获取连接失败时请求尚未发出：对写方法也重试一次，并重新签名（新的 nonce）。
func TestConnectFailureRetriesWritesWithAFreshNonce(t *testing.T) {
	for _, method := range []string{"GetSecret", "UpdateSecret"} {
		t.Run(method, func(t *testing.T) {
			source := &sourceStub{directory: testDirectory(t, "a")}
			c := newTestClient(t, tunnelConfig(source))
			var nonces []string
			c.invoke = func(ctx context.Context, ep endpoint, service, method string, _ int, raw []byte, headers http.Header) ([]byte, error) {
				claims, err := gatewayauth.Verify(c.config.Credentials, gatewayauth.Request{Method: "POST", Path: "/" + service + "/" + method, TargetNode: ep.target, Callee: service, Func: method, Body: raw}, headers, time.Now())
				require.NoError(t, err)
				nonces = append(nonces, claims.Nonce)
				if len(nonces) == 1 {
					return nil, connectFailure(ctx)
				}
				return raw, nil
			}
			body := []byte(`{}`)
			response, err := c.Forward(context.Background(), secretService, method, codec.SerializationTypeJSON, body)
			require.NoError(t, err)
			require.Equal(t, body, response)
			require.Len(t, nonces, 2)
			require.NotEqual(t, nonces[0], nonces[1])
			require.Equal(t, uint64(1), c.PoolStats().ConnectRetriesRecovered)
			require.Zero(t, c.PoolStats().ConnectRetriesFailed)
		})
	}
}

func TestConnectFailureRetriesOnlyOnce(t *testing.T) {
	source := &sourceStub{directory: testDirectory(t, "a")}
	c := newTestClient(t, tunnelConfig(source))
	calls := 0
	c.invoke = func(ctx context.Context, _ endpoint, _, _ string, _ int, _ []byte, _ http.Header) ([]byte, error) {
		calls++
		return nil, connectFailure(ctx)
	}
	_, err := c.Forward(context.Background(), secretService, "UpdateSecret", codec.SerializationTypeJSON, []byte(`{}`))
	require.Error(t, err)
	require.Equal(t, 2, calls)
	require.Equal(t, uint64(1), c.PoolStats().ConnectRetriesFailed)
	require.Zero(t, c.PoolStats().ConnectRetriesRecovered)
}

// 请求可能已发出的失败（超时、发出后的网络错误），以及没有本地建连失败标记的 111（下游回传），写方法都不重试。
func TestWritesAreNotRetriedWhenTheRequestMayHaveBeenSent(t *testing.T) {
	for name, failure := range map[string]error{
		"timeout":                errs.NewFrameError(errs.RetClientTimeout, "timeout"),
		"network error":          errs.NewFrameError(errs.RetClientNetErr, "reset"),
		"downstream 111":         errs.NewFrameError(errs.RetClientConnectFail, "gateway could not reach upstream"),
		"business error":         errs.New(10001, "rejected"),
		"read frame after write": errs.NewFrameError(errs.RetClientReadFrameErr, "eof"),
	} {
		t.Run(name, func(t *testing.T) {
			source := &sourceStub{directory: testDirectory(t, "a")}
			c := newTestClient(t, tunnelConfig(source))
			calls := 0
			c.invoke = func(context.Context, endpoint, string, string, int, []byte, http.Header) ([]byte, error) {
				calls++
				return nil, failure
			}
			_, err := c.Forward(context.Background(), secretService, "UpdateSecret", codec.SerializationTypeJSON, []byte(`{}`))
			require.Error(t, err)
			require.Equal(t, 1, calls)
		})
	}
}

func TestConnectFailureHonorsNoRetryAndTheRemainingBudget(t *testing.T) {
	source := &sourceStub{directory: testDirectory(t, "a")}
	c := newTestClient(t, tunnelConfig(source))
	calls := 0
	c.invoke = func(ctx context.Context, _ endpoint, _, _ string, _ int, _ []byte, _ http.Header) ([]byte, error) {
		calls++
		return nil, connectFailure(ctx)
	}
	_, err := c.Forward(WithoutRetry(context.Background()), secretService, "UpdateSecret", codec.SerializationTypeJSON, []byte(`{}`))
	require.Error(t, err)
	require.Equal(t, 1, calls, "WithoutRetry 下由调用方自己重试")

	calls = 0
	short, cancel := context.WithTimeout(context.Background(), minRetryRemaining/2)
	defer cancel()
	_, err = c.Forward(short, secretService, "UpdateSecret", codec.SerializationTypeJSON, []byte(`{}`))
	require.Error(t, err)
	require.Equal(t, 1, calls, "剩余预算不足时放弃重试，交给补采")
	require.Zero(t, c.PoolStats().ConnectRetriesFailed, "没有发起重试就不算重试失败")
}

// 连接池在获取连接失败时必须置位调用状态，这是写方法能安全重试的依据。
func TestPoolGetFailureMarksTheCall(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())

	pool := newRPCPool(tunedPoolSettings(PoolConfig{}))
	t.Cleanup(func() { _ = pool.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx, state := withCallState(ctx)
	opts := connpool.NewGetOptions()
	opts.WithContext(ctx)
	opts.WithDialTimeout(time.Second)
	_, err = pool.Get("tcp", address, opts)
	require.Error(t, err)
	require.True(t, state.connectFailed.Load())
	require.Equal(t, uint64(1), pool.stats.snapshot().DialsErr)
}

func externalCollector(t *testing.T) *Client {
	t.Helper()
	return newTestClient(t, Config{Mode: External, Credentials: gatewayauth.Credentials{Caller: "scf-collector", KeyID: "assigned-scf-key-71", Secret: "fixture-key"},
		AccessAddress: "access.example.test:11004", AccessInstanceID: "access@storage"})
}

// 幂等写（周期提交）在请求可能已发出的失败后重发一次，但只在剩余预算足够时；其他写方法不重发。
func TestIdempotentWriteRetriesAfterAmbiguousFailureWithinBudget(t *testing.T) {
	for name, failure := range map[string]error{
		"connection reset after send": errs.NewFrameError(errs.RetClientNetErr, "reset"),
		"response frame lost":         errs.NewFrameError(errs.RetClientReadFrameErr, "eof"),
	} {
		t.Run(name, func(t *testing.T) {
			c := externalCollector(t)
			calls := 0
			c.invoke = func(context.Context, endpoint, string, string, int, []byte, http.Header) ([]byte, error) {
				calls++
				if calls == 1 {
					return nil, failure
				}
				return []byte("ok"), nil
			}
			response, err := c.Forward(context.Background(), "trpc.moox.storage.PrimaryStore", "CommitTimeSeriesBatch", codec.SerializationTypePB, []byte{0x0a, 1, 'a'})
			require.NoError(t, err)
			require.Equal(t, []byte("ok"), response)
			require.Equal(t, 2, calls)

			calls = 0
			_, err = c.Forward(context.Background(), "trpc.moox.storage.PrimaryStore", "UpsertFields", codec.SerializationTypePB, []byte{0x0a, 1, 'a'})
			require.Error(t, err)
			require.Equal(t, 1, calls, "非幂等写不重发")
		})
	}
}

func TestRetryBudgetOnlyLimitsIdempotentWrites(t *testing.T) {
	c := externalCollector(t)
	calls := 0
	c.invoke = func(context.Context, endpoint, string, string, int, []byte, http.Header) ([]byte, error) {
		calls++
		return nil, errs.NewFrameError(errs.RetClientNetErr, "reset")
	}
	short, cancel := context.WithTimeout(context.Background(), minRetryRemaining/2)
	defer cancel()
	_, err := c.Forward(short, "trpc.moox.storage.PrimaryStore", "CommitTimeSeriesBatch", codec.SerializationTypePB, []byte{0x0a, 1, 'a'})
	require.Error(t, err)
	require.Equal(t, 1, calls, "剩余预算不足时幂等写放弃重发，交给补采")

	// 只读方法保持改动前的行为：只要调用还没过期就重试，不受 minRetryRemaining 约束。
	reader := newTestClient(t, Config{Mode: External, Credentials: gatewayauth.Credentials{Caller: "moox-skill", KeyID: "assigned-scf-key-71", Secret: "fixture-key"},
		AccessAddress: "access.example.test:11004", AccessInstanceID: "access@storage"})
	readCalls := 0
	reader.invoke = func(context.Context, endpoint, string, string, int, []byte, http.Header) ([]byte, error) {
		readCalls++
		return nil, errs.NewFrameError(errs.RetClientNetErr, "reset")
	}
	_, err = reader.Forward(short, "trpc.moox.storage.PrimaryStore", "ReadTimeSeriesRows", codec.SerializationTypePB, []byte{0x0a, 1, 'a'})
	require.Error(t, err)
	require.Equal(t, 2, readCalls)
}

// 端到端：Forward 经真实的 tRPC 客户端与连接池调用，目标端口拒绝连接时，标记必须穿过 tRPC 客户端传到连接池，
// 写方法才会在建连失败后重试。（其余测试手工置标记或直接调连接池，覆盖不到这条链路。）
func TestConnectFailureThroughTheRealTRPCClientIsRetried(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())

	source := &sourceStub{directory: testDirectory(t, "a")}
	config := tunnelConfig(source)
	config.Tunnels = tunnelFunc(func(context.Context, string) (string, error) { return address, nil })
	c := newTestClient(t, config)
	_, err = c.Forward(context.Background(), secretService, "UpdateSecret", codec.SerializationTypeJSON, []byte(`{}`))
	require.Error(t, err)
	stats := c.PoolStats()
	require.Equal(t, uint64(2), stats.DialsErr, "第一次尝试与重试各拨号一次")
	require.Equal(t, uint64(1), stats.ConnectRetriesFailed, "重试之后仍失败")
}

// 失败回调拿到每次失败尝试的原因：第一次建连失败会重试，重试再失败就放弃。
func TestCallFailureObserverSeesEveryFailedAttempt(t *testing.T) {
	source := &sourceStub{directory: testDirectory(t, "a")}
	c := newTestClient(t, tunnelConfig(source))
	var seen []CallFailure
	c.SetCallFailureObserver(func(failure CallFailure) { seen = append(seen, failure) })
	c.invoke = func(ctx context.Context, _ endpoint, _, _ string, _ int, _ []byte, _ http.Header) ([]byte, error) {
		return nil, connectFailure(ctx)
	}
	_, err := c.Forward(context.Background(), secretService, "UpdateSecret", codec.SerializationTypeJSON, []byte(`{}`))
	require.Error(t, err)
	require.Len(t, seen, 2)
	require.True(t, seen[0].ConnectFailed)
	require.True(t, seen[0].Retrying)
	require.Equal(t, 0, seen[0].Attempt)
	require.False(t, seen[1].Retrying)
	require.Equal(t, 1, seen[1].Attempt)
	require.Equal(t, "a", seen[1].Target)
	require.Equal(t, "UpdateSecret", seen[1].Method)
	require.Greater(t, seen[0].Remaining, time.Duration(0))
	c.SetCallFailureObserver(nil)
	_, _ = c.Forward(context.Background(), secretService, "UpdateSecret", codec.SerializationTypeJSON, []byte(`{}`))
	require.Len(t, seen, 2, "取消观察者之后不再回调")
}

func acceptingListener(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	var accepted atomic.Int64
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = conn
		}
	}()
	return listener.Addr().String(), &accepted
}

func getAs(t *testing.T, pool *rpcPool, address string, attempt int) net.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	ctx, state := withCallState(ctx)
	state.attempt = attempt
	opts := connpool.NewGetOptions()
	opts.WithContext(ctx)
	opts.WithDialTimeout(2 * time.Second)
	conn, err := pool.Get("tcp", address, opts)
	require.NoError(t, err)
	return conn
}

// 预热只在某个地址的打开连接数低于 min_idle 时补齐，不触碰已有连接；到位后不再拨号，并发请求直接复用空闲连接。
func TestWarmupFillsOnlyTheShortfall(t *testing.T) {
	address, accepted := acceptingListener(t)
	pool := newRPCPool(tunedPoolSettings(PoolConfig{MinIdle: 3}))
	t.Cleanup(func() { _ = pool.Close() })

	first := getAs(t, pool, address, 0)
	require.NoError(t, first.Close())
	require.Eventually(t, func() bool { return accepted.Load() == 1 }, 3*time.Second, 10*time.Millisecond)

	pool.warmCycle(time.Now())
	require.Eventually(t, func() bool { return accepted.Load() == 3 }, 3*time.Second, 10*time.Millisecond, "已有 1 条，只补 2 条")
	require.Equal(t, uint64(3), pool.stats.snapshot().WarmupOK)
	require.Zero(t, pool.stats.snapshot().WarmupErr)

	pool.warmCycle(time.Now())
	time.Sleep(100 * time.Millisecond)
	require.EqualValues(t, 3, accepted.Load(), "连接数到位后不再拨号")
	require.Equal(t, uint64(3), pool.stats.snapshot().WarmupOK, "也不再取放已有连接")

	dials := pool.stats.snapshot().DialsOK
	held := []net.Conn{getAs(t, pool, address, 0), getAs(t, pool, address, 0), getAs(t, pool, address, 0)}
	for _, conn := range held {
		require.NoError(t, conn.Close())
	}
	require.Equal(t, dials, pool.stats.snapshot().DialsOK, "预热过的并发请求不需要在请求路径上拨号")
}

func TestWarmupStopsWatchingIdleTargetsAndClosesCleanly(t *testing.T) {
	address, accepted := acceptingListener(t)
	pool := newRPCPool(tunedPoolSettings(PoolConfig{MinIdle: 2}))
	conn := getAs(t, pool, address, 0)
	require.NoError(t, conn.Close())
	pool.mu.Lock()
	for _, target := range pool.targets {
		target.lastUsed = time.Now().Add(-2 * warmWatchLimit)
	}
	pool.mu.Unlock()
	pool.warmCycle(time.Now())
	time.Sleep(100 * time.Millisecond)
	require.EqualValues(t, 1, accepted.Load(), "太久没有被请求使用的地址不再预热")
	pool.mu.Lock()
	require.Empty(t, pool.targets)
	pool.mu.Unlock()

	done := make(chan error, 1)
	go func() { done <- pool.Close() }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Close 必须等待预热协程退出且不能卡住")
	}
	require.NoError(t, pool.Close(), "Close 幂等")
	_, err := pool.dial(&connpool.DialOptions{Network: "tcp", Address: address, Timeout: time.Second})
	require.ErrorIs(t, err, net.ErrClosed, "关闭后不再拨号")
	require.EqualValues(t, 1, accepted.Load())
}

// 重试可能是因为取到了已失效的空闲连接（网关重启后）：重试必须避开主池里的空闲连接，现建一条。
func TestRetryAvoidsTheMainPoolIdleConnections(t *testing.T) {
	address, accepted := acceptingListener(t)
	pool := newRPCPool(tunedPoolSettings(PoolConfig{}))
	t.Cleanup(func() { _ = pool.Close() })

	first := getAs(t, pool, address, 0)
	require.NoError(t, first.Close())
	again := getAs(t, pool, address, 0)
	require.NoError(t, again.Close())
	time.Sleep(100 * time.Millisecond)
	require.EqualValues(t, 1, accepted.Load(), "第一次尝试复用主池的空闲连接")

	retry := getAs(t, pool, address, 1)
	require.NoError(t, retry.Close())
	require.Eventually(t, func() bool { return accepted.Load() == 2 }, 3*time.Second, 10*time.Millisecond, "重试不复用主池的空闲连接")
	retryAgain := getAs(t, pool, address, 1)
	require.NoError(t, retryAgain.Close())
	require.Eventually(t, func() bool { return accepted.Load() == 3 }, 3*time.Second, 10*time.Millisecond, "重试连接用完即关，下一次重试也现建，不复用上一次重试的连接")
}

func TestWarmupAvoidsTheMinuteBoundary(t *testing.T) {
	at := func(second int) time.Time { return time.Date(2026, 10, 19, 12, 30, second, 0, time.UTC) }
	for _, second := range []int{0, 1, 4, 58, 59} {
		require.True(t, inBurstWindow(at(second)), second)
	}
	for _, second := range []int{5, 20, 30, 57} {
		require.False(t, inBurstWindow(at(second)), second)
	}
}
