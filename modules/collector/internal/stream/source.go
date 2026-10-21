// Package stream 是与交易所无关的 WS K 线接收框架：连接管理、订阅、周期闸门、提交、计划同步与观测。
// 交易所只实现 Source（协议），新增数据源不改这里的代码。
package stream

import (
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

// Source 是一个交易所的一个市场（例如币安现货、币安 U 本位永续）的 WS K 线来源。
type Source interface {
	// Key 与 marketdata 注册表里的 SourceKey 一致（provider + source_id），用来构造 series_tag。
	Key() marketdata.SourceKey
	MarketType() string
	// Endpoints 按优先级返回连接地址；连接管理按顺序尝试，失败后退避换下一个。
	Endpoints() []string
	// Limits 是连接与订阅的限额。
	Limits() Limits
	// Stream 返回一个（交易所订阅符号, 频率）对应的流名；不支持的频率返回 false。
	// 订阅符号是计划里的 Pair（币安永续合约按 pair 订阅连续合约 K 线），默认与交易所 symbol 相同。
	Stream(symbol string, frequency marketdata.Frequency) (string, bool)
	// StatusStreams 返回额外订阅的状态流（例如合约上下架推送），没有返回空。
	StatusStreams() []string
	// Subscribe / Unsubscribe 构造订阅报文；一条报文最多 Limits.StreamsPerMessage 个流。
	Subscribe(streams []string, id int) ([]byte, error)
	Unsubscribe(streams []string, id int) ([]byte, error)
	// Decode 把一条原始消息解成零到多个事件；订阅回执、心跳等控制消息返回 Control 事件。
	Decode(raw []byte, receivedAt time.Time) ([]Event, error)
	// Keepalive 说明保活方式：服务端 ping（回 pong）或客户端定时发送的报文。
	Keepalive() Keepalive
}

// Provider 由数据源适配器实现：返回某个市场类型的 WS 来源。stream 依赖 marketdata，数据源依赖 stream，反过来不依赖。
type Provider interface {
	StreamSource(marketType string) (Source, error)
}

// Limits 是一个来源的连接与订阅限额。
type Limits struct {
	StreamsPerConn    int           // 每条连接的流数上限（币安 1024，按 1000 用）
	StreamsPerMessage int           // 一条订阅报文的流数（币安 100）
	MessagesPerSecond int           // 入站限速（订阅、pong 等）
	MaxConnAge        time.Duration // 交易所强制断开的寿命
	ConnectsPerWindow int           // 建连限速（每 ConnectWindow 次数），只做保护
	ConnectWindow     time.Duration
}

// Keepalive 是保活方式。
type Keepalive struct {
	ServerPing  bool          // 收到 ping 回 pong
	ClientPing  []byte        // 客户端定时发送的报文
	Interval    time.Duration // 客户端发送间隔
	ReadTimeout time.Duration // 这么久没有任何消息就判定连接失效并重连
}

// EventKind 是解码后消息的类型。
type EventKind int

const (
	EventKlineUpdate EventKind = iota + 1
	EventKlineFinal
	// EventControl 是订阅回执等控制消息：ID 是请求号，Err 非空表示该请求被拒绝。
	EventControl
	// EventStatus 是标的状态变化（例如合约下架），Trading 为假表示已不在交易。
	EventStatus
	EventError
)

// Event 是解码后的一条消息。只有 KlineFinal 进周期闸门。
type Event struct {
	Kind EventKind
	// Symbol 是订阅符号（大写），与 Source.Stream 的入参一致。
	Symbol  string
	Kline   marketdata.NormalizedKline
	EventAt time.Time
	ID      int
	Err     string
	Trading bool
}
