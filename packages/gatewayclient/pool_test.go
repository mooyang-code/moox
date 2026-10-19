package gatewayclient

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/pool/connpool"
)

// 客户端空闲超时必须明显小于服务端回收时间：TLS 连接复用前没有探测，客户端先回收才不会把请求发到已关闭的连接上。
func TestClientIdleTimeoutStaysBelowServer(t *testing.T) {
	require.Less(t, clientIdleTimeout+5*time.Minute, servicecatalog.HostGatewayServerIdleTimeout, "至少留 5 分钟余量")
	require.Less(t, clientIdleTimeout, clientMaxLifetime)
}

func TestPoolConfigValidate(t *testing.T) {
	require.NoError(t, PoolConfig{}.Validate())
	require.NoError(t, PoolConfig{MinIdle: 16, MaxIdle: 32}.Validate())
	require.NoError(t, PoolConfig{MinIdle: 16}.Validate(), "MaxIdle 省略时使用默认值")
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
	require.Equal(t, uint64(1), pool.stats.snapshot().RequestDialsErr)
}

// MinIdle 的后台补连接固定使用 200ms 拨号超时；dial 把它识别出来，计入预热统计，请求路径的拨号单独计数。
func TestDialSeparatesBackgroundWarmupFromRequests(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn
		}
	}()
	pool := newRPCPool(tunedPoolSettings(PoolConfig{MinIdle: 2}))
	t.Cleanup(func() { _ = pool.Close() })

	conn, err := pool.dial(&connpool.DialOptions{Network: "tcp", Address: listener.Addr().String(), Timeout: 200 * time.Millisecond})
	require.NoError(t, err)
	_ = conn.Close()
	conn, err = pool.dial(&connpool.DialOptions{Network: "tcp", Address: listener.Addr().String(), Timeout: 2 * time.Second})
	require.NoError(t, err)
	_ = conn.Close()
	stats := pool.stats.snapshot()
	require.Equal(t, uint64(1), stats.WarmupDialsOK)
	require.Equal(t, uint64(1), stats.RequestDialsOK)

	// 没有预热配置的池不把短超时当作后台拨号。
	plain := newRPCPool(tunedPoolSettings(PoolConfig{}))
	t.Cleanup(func() { _ = plain.Close() })
	conn, err = plain.dial(&connpool.DialOptions{Network: "tcp", Address: listener.Addr().String(), Timeout: 200 * time.Millisecond})
	require.NoError(t, err)
	_ = conn.Close()
	require.Equal(t, uint64(1), plain.stats.snapshot().RequestDialsOK)
	require.Zero(t, plain.stats.snapshot().WarmupDialsOK)
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

// 预热：连接池第一次被使用后，后台把空闲连接补到 MinIdle，之后的请求直接复用，不再在请求路径上拨号。
func TestMinIdleWarmsConnectionsBeforeRequestsNeedThem(t *testing.T) {
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
	pool := newRPCPool(tunedPoolSettings(PoolConfig{MinIdle: 3}))
	t.Cleanup(func() { _ = pool.Close() })

	opts := connpool.NewGetOptions()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	opts.WithContext(ctx)
	first, err := pool.Get("tcp", listener.Addr().String(), opts)
	require.NoError(t, err)
	_ = first.Close()
	require.Eventually(t, func() bool { return pool.stats.snapshot().WarmupDialsOK >= 3 }, 5*time.Second, 20*time.Millisecond)
	before := pool.stats.snapshot().RequestDialsOK
	held := make([]net.Conn, 0, 3)
	for i := 0; i < 3; i++ {
		o := connpool.NewGetOptions()
		c2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
		o.WithContext(c2)
		conn, err := pool.Get("tcp", listener.Addr().String(), o)
		cancel2()
		require.NoError(t, err)
		held = append(held, conn)
	}
	for _, conn := range held {
		_ = conn.Close()
	}
	require.Equal(t, before, pool.stats.snapshot().RequestDialsOK, "预热过的并发请求不需要在请求路径上拨号")
}
