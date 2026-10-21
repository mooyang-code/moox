package stream_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
	"github.com/mooyang-code/moox/modules/collector/internal/stream"
	"github.com/stretchr/testify/require"
)

// fakeExchange 是按币安组合流协议应答的本地 WS 服务：订阅回执、可拒绝的流、服务端 ping、推送与断线。
type fakeExchange struct {
	t        *testing.T
	server   *httptest.Server
	mu       sync.Mutex
	conns    map[*websocket.Conn]map[string]bool
	reject   map[string]bool
	requests []string
	opened   atomic.Int32
}

func newFakeExchange(t *testing.T) *fakeExchange {
	t.Helper()
	exchange := &fakeExchange{t: t, conns: map[*websocket.Conn]map[string]bool{}, reject: map[string]bool{}}
	upgrader := websocket.Upgrader{}
	exchange.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		exchange.opened.Add(1)
		exchange.mu.Lock()
		exchange.conns[conn] = map[string]bool{}
		exchange.mu.Unlock()
		_ = conn.WriteControl(websocket.PingMessage, []byte("p"), time.Now().Add(time.Second))
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				exchange.mu.Lock()
				delete(exchange.conns, conn)
				exchange.mu.Unlock()
				return
			}
			var request struct {
				Method string   `json:"method"`
				Params []string `json:"params"`
				ID     int      `json:"id"`
			}
			_ = json.Unmarshal(raw, &request)
			exchange.mu.Lock()
			exchange.requests = append(exchange.requests, request.Method+" "+strings.Join(request.Params, ","))
			rejected := false
			for _, stream := range request.Params {
				if exchange.reject[stream] {
					rejected = true
				}
			}
			reply := fmt.Sprintf(`{"result":null,"id":%d}`, request.ID)
			if rejected {
				reply = fmt.Sprintf(`{"error":{"code":2,"msg":"Invalid request"},"id":%d}`, request.ID)
			} else {
				for _, stream := range request.Params {
					exchange.conns[conn][stream] = request.Method == "SUBSCRIBE"
				}
			}
			_ = conn.WriteMessage(websocket.TextMessage, []byte(reply))
			exchange.mu.Unlock()
		}
	}))
	t.Cleanup(exchange.server.Close)
	return exchange
}

func (e *fakeExchange) url() string {
	return "ws" + strings.TrimPrefix(e.server.URL, "http") + "/stream"
}

// push 给订阅了 stream 的每条连接发一条收盘 K 线。
func (e *fakeExchange) push(stream, symbol string, openMS int64) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	message := fmt.Sprintf(`{"stream":"%s","data":{"e":"kline","E":%d,"s":"%s","k":{"t":%d,"T":%d,"s":"%s","i":"1m","f":1,"L":2,"o":"1","c":"1","h":"1","l":"1","v":"1","n":1,"x":true,"q":"1","V":"1","Q":"1","B":"0"}}}`,
		stream, openMS+60000, symbol, openMS, openMS+59999, symbol)
	sent := 0
	for conn, streams := range e.conns {
		if streams[stream] {
			if conn.WriteMessage(websocket.TextMessage, []byte(message)) == nil {
				sent++
			}
		}
	}
	return sent
}

func (e *fakeExchange) dropAll() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for conn := range e.conns {
		_ = conn.Close()
	}
}

func (e *fakeExchange) requestLog() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.requests...)
}

type eventSink struct {
	mu     sync.Mutex
	events []stream.Event
}

func (s *eventSink) add(event stream.Event) {
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
}

func (s *eventSink) finals() map[int64]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := map[int64]int{}
	for _, event := range s.events {
		if event.Kind == stream.EventKlineFinal {
			result[event.Kline.BarStart.UnixMilli()]++
		}
	}
	return result
}

func startManager(t *testing.T, exchange *fakeExchange, opts stream.ManagerOptions) (*stream.ConnManager, *eventSink) {
	t.Helper()
	source, err := binance.NewStreamSource("spot", binance.StreamConfig{SpotEndpoints: []string{exchange.url()}, MessagesPerSecond: 50})
	require.NoError(t, err)
	sink := &eventSink{}
	manager := stream.NewConnManager(source, sink.add, opts)
	ctx, cancel := context.WithCancel(context.Background())
	manager.Start(ctx)
	t.Cleanup(func() { cancel(); manager.Close() })
	return manager, sink
}

func TestConnManagerSubscribesReconnectsAndDiffs(t *testing.T) {
	exchange := newFakeExchange(t)
	manager, sink := startManager(t, exchange, stream.ManagerOptions{CheckInterval: 20 * time.Millisecond})
	manager.Apply(map[string][]string{"1m": {"btcusdt@kline_1m", "ethusdt@kline_1m"}})
	require.Eventually(t, manager.Ready, 3*time.Second, 10*time.Millisecond)
	require.Equal(t, 1, exchange.push("btcusdt@kline_1m", "BTCUSDT", 0))
	require.Eventually(t, func() bool { return sink.finals()[0] == 1 }, time.Second, 5*time.Millisecond)

	// 断线：退避重连后重新订阅全部流。
	exchange.dropAll()
	require.Eventually(t, func() bool { return exchange.opened.Load() == 2 && manager.Ready() }, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return exchange.push("ethusdt@kline_1m", "ETHUSDT", 60000) == 1 }, 3*time.Second, 20*time.Millisecond)
	require.Equal(t, int64(1), manager.Status()[0].Reconnects)

	// 差量订阅：不重连，只退订移除的、订阅新增的。
	manager.Apply(map[string][]string{"1m": {"btcusdt@kline_1m", "solusdt@kline_1m"}})
	require.Eventually(t, func() bool {
		log := exchange.requestLog()
		return len(log) >= 2 && log[len(log)-2] == "UNSUBSCRIBE ethusdt@kline_1m" && log[len(log)-1] == "SUBSCRIBE solusdt@kline_1m"
	}, 3*time.Second, 10*time.Millisecond)
	require.EqualValues(t, 2, exchange.opened.Load())

	// 订阅报错：记为被拒绝，下次同步时重试。
	exchange.mu.Lock()
	exchange.reject["xrpusdt@kline_1m"] = true
	exchange.mu.Unlock()
	manager.Apply(map[string][]string{"1m": {"btcusdt@kline_1m", "solusdt@kline_1m", "xrpusdt@kline_1m"}})
	require.Eventually(t, func() bool { return manager.Status()[0].Rejected == 1 }, 3*time.Second, 10*time.Millisecond)

	// 分组为空：关闭该分片。
	manager.Apply(map[string][]string{})
	require.Eventually(t, func() bool { return len(manager.Status()) == 0 }, time.Second, 10*time.Millisecond)
}

// 寿命将到时主动轮换：新连接订阅完成并收到消息后才关闭旧连接，期间持续推送的定稿不缺失。
func TestConnManagerRotatesWithoutGaps(t *testing.T) {
	exchange := newFakeExchange(t)
	manager, sink := startManager(t, exchange, stream.ManagerOptions{CheckInterval: 10 * time.Millisecond, MaxConnAge: 400 * time.Millisecond, RotateLead: 200 * time.Millisecond, RotationWindow: func(time.Time) bool { return true }})
	manager.Apply(map[string][]string{"1m": {"btcusdt@kline_1m"}})
	require.Eventually(t, manager.Ready, 3*time.Second, 10*time.Millisecond)
	var sent []int64
	for i := int64(0); i < 60; i++ {
		openMS := i * 60000
		if exchange.push("btcusdt@kline_1m", "BTCUSDT", openMS) > 0 {
			sent = append(sent, openMS)
		}
		time.Sleep(15 * time.Millisecond)
	}
	require.GreaterOrEqual(t, exchange.opened.Load(), int32(2), "至少轮换一次")
	time.Sleep(50 * time.Millisecond)
	finals := sink.finals()
	for _, openMS := range sent {
		require.GreaterOrEqual(t, finals[openMS], 1, "period %d missing", openMS)
	}
	require.Zero(t, manager.Status()[0].Reconnects, "主动轮换不算断线重连")
}

// 记录启动到全部订阅完成的耗时：每条连接约 1000 个流，每条报文 100 个流。
func TestConnManagerSubscribeLatency(t *testing.T) {
	exchange := newFakeExchange(t)
	source, err := binance.NewStreamSource("spot", binance.StreamConfig{SpotEndpoints: []string{exchange.url()}})
	require.NoError(t, err)
	manager := stream.NewConnManager(source, func(stream.Event) {}, stream.ManagerOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); manager.Close() }()
	manager.Start(ctx)
	streams := make([]string, 0, 1000)
	for i := 0; i < 1000; i++ {
		streams = append(streams, fmt.Sprintf("s%04dusdt@kline_1m", i))
	}
	started := time.Now()
	manager.Apply(map[string][]string{"1m": streams})
	require.Eventually(t, manager.Ready, 10*time.Second, 10*time.Millisecond)
	elapsed := time.Since(started)
	t.Logf("1000 个流在每秒 5 条报文下订阅完成耗时 %s", elapsed)
	require.Less(t, elapsed, 3*time.Second)
}
