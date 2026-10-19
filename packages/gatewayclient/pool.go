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
	// MinIdle 是每个地址保持的最少打开连接数（预热）：不足时由后台协程补齐，请求路径上复用现成连接。
	// 默认 0；整分钟并发写入的 Access 设置为 16。
	MinIdle int `yaml:"min_idle"`
	// MaxIdle 是每个（地址，协议）子池保留的空闲连接上限，不是客户端的总连接数上限。默认 32。
	MaxIdle int `yaml:"max_idle"`
}

const (
	maxPoolMinIdle = 64
	maxPoolMaxIdle = 128

	defaultTunedMaxIdle = 32
	legacyMaxIdle       = 8

	// clientIdleTimeout 是客户端回收空闲连接的时间，必须大于最长的调用间隔（采集函数每分钟一次）。
	// 网关服务端不回收空闲连接，连接是否还活着靠 TCP keepalive 和调用时的重试兜底。
	clientIdleTimeout = 8 * time.Minute
	// clientMaxLifetime 定期轮换老连接，避免长期持有被中间设备悄悄丢弃的连接。
	clientMaxLifetime = 30 * time.Minute

	// 预热：每 warmInterval 检查一次，只在某个地址的打开连接数低于 min_idle 时才补，不触碰已有连接（不重置它们的空闲计时）；
	// 超过 warmWatchLimit 没有被请求使用的地址不再预热；整分钟的前后几秒是业务突发窗口，避开它。
	warmInterval     = 10 * time.Second
	warmDialTimeout  = 3 * time.Second
	warmWatchLimit   = 15 * time.Minute
	burstWindowAfter = 5 // 每分钟的第 0～4 秒与第 58～59 秒不预热

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
	// 按生效的 max_idle（省略时是默认值）判断：min_idle 超过它，预热每轮取满再归还时会关掉多出的连接，形成补建后关闭的循环。
	maxIdle := c.MaxIdle
	if maxIdle == 0 {
		maxIdle = defaultTunedMaxIdle
	}
	if c.MinIdle > maxIdle {
		return errors.New("gateway_client.pool.min_idle must not exceed max_idle (default 32)")
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
	// forceClose 为 true 时连接用完即关，不归还空闲池（重试子池：每次重试都现建连接）。
	forceClose bool
}

func legacyPoolSettings() poolSettings { return poolSettings{maxIdle: legacyMaxIdle} }

// retryPoolSettings 用于重试子池：重试往往是因为主池里取到了已失效的连接（网关重启后），
// 重试连接如果成功后被复用，下一次重试仍可能取到同一批已失效的连接，所以用完即关。
func retryPoolSettings() poolSettings { return poolSettings{maxIdle: legacyMaxIdle, forceClose: true} }

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
	if s.forceClose {
		options = append(options, connpool.WithForceClose(true))
	}
	return options
}

// PoolStats 是连接池拨号与建连重试的累计计数，由进程自己暴露为指标。
type PoolStats struct {
	// DialsOK/Err 是新建连接的总次数（请求路径与预热合计）；预热到位后稳态应接近 0。
	DialsOK, DialsErr uint64
	// WarmupOK/Err 是预热协程补连接时每一次取连接的结果。
	WarmupOK, WarmupErr uint64
	// ConnectRetriesRecovered 是建连失败后重试成功的调用数；ConnectRetriesFailed 是重试后仍失败的调用数。
	ConnectRetriesRecovered, ConnectRetriesFailed uint64
}

type poolCounters struct {
	dialsOK, dialsErr               atomic.Uint64
	warmupOK, warmupErr             atomic.Uint64
	retriesRecovered, retriesFailed atomic.Uint64
}

func (c *poolCounters) dial(ok bool) {
	if ok {
		c.dialsOK.Add(1)
	} else {
		c.dialsErr.Add(1)
	}
}

func (c *poolCounters) snapshot() PoolStats {
	return PoolStats{
		DialsOK: c.dialsOK.Load(), DialsErr: c.dialsErr.Load(),
		WarmupOK: c.warmupOK.Load(), WarmupErr: c.warmupErr.Load(),
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
