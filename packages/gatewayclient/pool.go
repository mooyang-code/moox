package gatewayclient

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"trpc.group/trpc-go/trpc-go/pool/connpool"
)

// PoolConfig 是内部网关客户端连接池的可调参数，零值表示使用默认。
type PoolConfig struct {
	// MinIdle 是连接池保持的最少空闲连接数（预热）：空闲连接过期或被回收后由连接池后台补齐，
	// 请求路径上只复用现成连接。默认 0；整分钟并发写入的 Access 设置为 16。
	MinIdle int `yaml:"min_idle"`
	// MaxIdle 是每个（地址，协议）子池保留的空闲连接上限，不是客户端的总连接数上限。默认 32。
	MaxIdle int `yaml:"max_idle"`
}

const (
	maxPoolMinIdle = 64
	maxPoolMaxIdle = 128

	defaultTunedMaxIdle = 32
	legacyMaxIdle       = 8

	// clientIdleTimeout 必须明显小于服务端的回收时间（servicecatalog.HostGatewayServerIdleTimeout），
	// 见 TestClientIdleTimeoutStaysBelowServer。
	clientIdleTimeout = 8 * time.Minute
	// clientMaxLifetime 定期轮换老连接，避免长期持有被中间设备悄悄丢弃的连接。
	clientMaxLifetime = 30 * time.Minute

	// connpool 的 MinIdle 后台补连接固定使用 200ms 的拨号超时，跨境 TLS 握手经常超过它；
	// 后台补连接走 rpcPool.dial，在那里把过短的超时抬到 backgroundDialTimeout。
	backgroundDialThreshold = 250 * time.Millisecond
	backgroundDialTimeout   = 3 * time.Second

	// 请求路径的拨号预算：总预算（服务目录里 PrimaryStore 为 5 秒）内要留出至少 retryReserve 给发送与提交。
	maxConnectTimeout = 3 * time.Second
	retryReserve      = time.Second
	minRetryRemaining = 1500 * time.Millisecond
)

// Validate 检查连接池参数范围。
func (c PoolConfig) Validate() error {
	if c.MinIdle < 0 || c.MinIdle > maxPoolMinIdle {
		return errors.New("gateway_client.pool.min_idle must be between 0 and 64")
	}
	if c.MaxIdle < 0 || c.MaxIdle > maxPoolMaxIdle {
		return errors.New("gateway_client.pool.max_idle must be between 0 and 128")
	}
	if c.MaxIdle != 0 && c.MaxIdle < c.MinIdle {
		return errors.New("gateway_client.pool.max_idle must not be below min_idle")
	}
	return nil
}

// poolSettings 是 rpcPool 的运行参数。零值等同于改动前的行为（最多 8 条空闲、trpc-go 默认空闲超时）。
type poolSettings struct {
	maxIdle     int
	minIdle     int
	idleTimeout time.Duration
	maxLifetime time.Duration
	// fastConnect 为 true 时请求路径的拨号超时受 connectBudget 约束，建连失败可以在预算内重试。
	fastConnect bool
}

func legacyPoolSettings() poolSettings { return poolSettings{maxIdle: legacyMaxIdle} }

// tunedPoolSettings 用于连接本机/跨主机网关的客户端（Internal 与 Tunnel 模式）。
func tunedPoolSettings(c PoolConfig) poolSettings {
	s := poolSettings{maxIdle: c.MaxIdle, minIdle: c.MinIdle, idleTimeout: clientIdleTimeout, maxLifetime: clientMaxLifetime, fastConnect: true}
	if s.maxIdle == 0 {
		s.maxIdle = defaultTunedMaxIdle
	}
	return s
}

func (s poolSettings) options(dial connpool.DialFunc) []connpool.Option {
	options := []connpool.Option{connpool.WithDialFunc(dial), connpool.WithMaxIdle(s.maxIdle)}
	if s.idleTimeout > 0 {
		options = append(options, connpool.WithIdleTimeout(s.idleTimeout), connpool.WithPoolIdleTimeout(2*s.idleTimeout))
	}
	if s.maxLifetime > 0 {
		options = append(options, connpool.WithMaxConnLifetime(s.maxLifetime))
	}
	if s.minIdle > 0 {
		options = append(options, connpool.WithMinIdle(s.minIdle))
	}
	return options
}

// PoolStats 是连接池拨号与建连重试的累计计数，由进程自己暴露为指标。
type PoolStats struct {
	// RequestDialsOK/Err 是请求路径上新建连接的次数；预热后稳态应接近 0。
	RequestDialsOK, RequestDialsErr uint64
	// WarmupDialsOK/Err 是连接池后台补连接（预热与替换过期连接）的次数。
	WarmupDialsOK, WarmupDialsErr uint64
	// ConnectRetriesRecovered 是建连失败后重试成功的调用数；ConnectRetriesFailed 是重试后仍失败的调用数。
	ConnectRetriesRecovered, ConnectRetriesFailed uint64
}

type poolCounters struct {
	requestDialsOK, requestDialsErr atomic.Uint64
	warmupDialsOK, warmupDialsErr   atomic.Uint64
	retriesRecovered, retriesFailed atomic.Uint64
}

func (c *poolCounters) snapshot() PoolStats {
	return PoolStats{
		RequestDialsOK: c.requestDialsOK.Load(), RequestDialsErr: c.requestDialsErr.Load(),
		WarmupDialsOK: c.warmupDialsOK.Load(), WarmupDialsErr: c.warmupDialsErr.Load(),
		ConnectRetriesRecovered: c.retriesRecovered.Load(), ConnectRetriesFailed: c.retriesFailed.Load(),
	}
}

// callState 是单次调用的上下文状态：连接池在获取连接失败（请求尚未发出）时置位，Forward 以此判断可以安全重试。
// 只看 trpc 错误码 111 不够：下游回传的错误也可能带这个码，不能证明本次请求没有执行。
type callState struct {
	attempt       int
	connectFailed atomic.Bool
}

type callStateKey struct{}

func withCallState(ctx context.Context) (context.Context, *callState) {
	state := &callState{}
	return context.WithValue(ctx, callStateKey{}, state), state
}

func callStateFrom(ctx context.Context) *callState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(callStateKey{}).(*callState)
	return state
}

// markConnectFailure 记录本次调用在获取连接阶段失败（请求尚未发出）。
func markConnectFailure(ctx context.Context) {
	if state := callStateFrom(ctx); state != nil {
		state.connectFailed.Store(true)
	}
}

// connectBudget 是请求路径单次拨号的超时：第一次最多占剩余时间的 40%，第二次留出 retryReserve 给发送与提交，
// 都不超过 maxConnectTimeout。
func connectBudget(remaining time.Duration, attempt int) time.Duration {
	var budget time.Duration
	if attempt == 0 {
		budget = remaining * 2 / 5
	} else {
		budget = remaining - retryReserve
	}
	return max(min(budget, maxConnectTimeout), 0)
}
