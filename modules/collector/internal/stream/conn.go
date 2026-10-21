package stream

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// defaultReadTimeout 是没有任何消息就判定连接失效的时间。
const defaultReadTimeout = 60 * time.Second

// subscribeAckTimeout 是等待订阅回执的时间。
const subscribeAckTimeout = 10 * time.Second

// Resolver 返回主机名的候选地址（DNS 快照）；没有地址时按主机名解析。拨号用地址，TLS 仍按原主机名校验。
type Resolver func(host string) []string

// pacer 限制向交易所发出的消息速率（订阅、退订、客户端 ping），一个来源共用一个。
type pacer struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func newPacer(perSecond int) *pacer {
	if perSecond <= 0 {
		perSecond = 5
	}
	return &pacer{interval: time.Second / time.Duration(perSecond)}
}

func (p *pacer) wait(ctx context.Context) error {
	p.mu.Lock()
	now := time.Now()
	at := p.next
	if at.Before(now) {
		at = now
	}
	p.next = at.Add(p.interval)
	p.mu.Unlock()
	if delay := time.Until(at); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// Conn 是一条 WS 连接：订阅批次与限速、回执校验、保活、读超时、事件分发。
type Conn struct {
	source   Source
	endpoint string
	ws       *websocket.Conn
	pacer    *pacer
	onEvent  func(Event)
	opened   time.Time

	writeMu     sync.Mutex
	lastMessage atomic.Int64
	messages    atomic.Int64
	// data 是收到的行情事件数（K 线），不含订阅回执等控制消息；轮换时据此确认新连接确实有行情。
	data   atomic.Int64
	nextID atomic.Int64

	mu      sync.Mutex
	pending map[int]chan string
	closed  chan struct{}
	once    sync.Once
	err     error
}

// dialConn 建立一条连接并开始读循环。
func dialConn(ctx context.Context, source Source, endpoint string, resolve Resolver, pace *pacer, onEvent func(Event)) (*Conn, error) {
	target, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second, Proxy: nil, EnableCompression: false}
	if resolve != nil {
		host := target.Hostname()
		dialer.NetDialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			_, port, splitErr := net.SplitHostPort(address)
			if splitErr != nil {
				return nil, splitErr
			}
			var lastErr error
			for _, ip := range resolve(host) {
				conn, dialErr := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip, port))
				if dialErr == nil {
					return conn, nil
				}
				lastErr = dialErr
			}
			conn, dialErr := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, address)
			if dialErr != nil {
				return nil, errors.Join(lastErr, dialErr)
			}
			return conn, nil
		}
	}
	ws, _, err := dialer.DialContext(ctx, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", endpoint, err)
	}
	conn := &Conn{source: source, endpoint: endpoint, ws: ws, pacer: pace, onEvent: onEvent, opened: time.Now(), pending: map[int]chan string{}, closed: make(chan struct{})}
	conn.lastMessage.Store(time.Now().UnixNano())
	keepalive := source.Keepalive()
	ws.SetPingHandler(func(data string) error {
		conn.lastMessage.Store(time.Now().UnixNano())
		conn.writeMu.Lock()
		defer conn.writeMu.Unlock()
		return ws.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(5*time.Second))
	})
	go conn.readLoop(keepalive)
	if len(keepalive.ClientPing) > 0 && keepalive.Interval > 0 {
		go conn.pingLoop(keepalive)
	}
	return conn, nil
}

func (c *Conn) readLoop(keepalive Keepalive) {
	timeout := keepalive.ReadTimeout
	if timeout <= 0 {
		timeout = defaultReadTimeout
	}
	for {
		_ = c.ws.SetReadDeadline(time.Now().Add(timeout))
		_, raw, err := c.ws.ReadMessage()
		if err != nil {
			c.close(fmt.Errorf("read: %w", err))
			return
		}
		received := time.Now()
		c.lastMessage.Store(received.UnixNano())
		c.messages.Add(1)
		events, err := c.source.Decode(raw, received)
		if err != nil {
			c.onEvent(Event{Kind: EventError, Err: err.Error()})
			continue
		}
		for _, event := range events {
			if event.Kind == EventControl {
				c.resolve(event.ID, event.Err)
				continue
			}
			if event.Kind == EventKlineUpdate || event.Kind == EventKlineFinal {
				c.data.Add(1)
			}
			c.onEvent(event)
		}
	}
}

func (c *Conn) pingLoop(keepalive Keepalive) {
	ticker := time.NewTicker(keepalive.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := c.write(context.Background(), keepalive.ClientPing); err != nil {
				c.close(err)
				return
			}
		case <-c.closed:
			return
		}
	}
}

func (c *Conn) resolve(id int, errText string) {
	c.mu.Lock()
	ch, ok := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if ok {
		ch <- errText
	}
}

func (c *Conn) write(ctx context.Context, payload []byte) error {
	if err := c.pacer.wait(ctx); err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return c.ws.WriteMessage(websocket.TextMessage, payload)
}

// request 按批发送订阅或退订，等待每批回执；返回被拒绝的流。
func (c *Conn) request(ctx context.Context, streams []string, subscribe bool) (map[string]string, error) {
	limit := c.source.Limits().StreamsPerMessage
	if limit <= 0 {
		limit = 100
	}
	rejected := map[string]string{}
	for start := 0; start < len(streams); start += limit {
		batch := streams[start:min(start+limit, len(streams))]
		id := int(c.nextID.Add(1))
		var payload []byte
		var err error
		if subscribe {
			payload, err = c.source.Subscribe(batch, id)
		} else {
			payload, err = c.source.Unsubscribe(batch, id)
		}
		if err != nil {
			return rejected, err
		}
		ack := make(chan string, 1)
		c.mu.Lock()
		c.pending[id] = ack
		c.mu.Unlock()
		if err := c.write(ctx, payload); err != nil {
			return rejected, err
		}
		timer := time.NewTimer(subscribeAckTimeout)
		select {
		case errText := <-ack:
			timer.Stop()
			if errText != "" {
				for _, stream := range batch {
					rejected[stream] = errText
				}
			}
		case <-timer.C:
			return rejected, fmt.Errorf("request %d was not acknowledged within %s", id, subscribeAckTimeout)
		case <-c.closed:
			timer.Stop()
			return rejected, c.closeErr()
		case <-ctx.Done():
			timer.Stop()
			return rejected, ctx.Err()
		}
	}
	return rejected, nil
}

// Subscribe 订阅一组流，返回被交易所拒绝的流（下次重连时重试）。
func (c *Conn) Subscribe(ctx context.Context, streams []string) (map[string]string, error) {
	return c.request(ctx, streams, true)
}

// Unsubscribe 退订一组流。
func (c *Conn) Unsubscribe(ctx context.Context, streams []string) error {
	_, err := c.request(ctx, streams, false)
	return err
}

// Done 在连接关闭时关闭。
func (c *Conn) Done() <-chan struct{} { return c.closed }

// Messages 返回已收到的消息数（轮换时用来确认新连接已有数据）。
func (c *Conn) Messages() int64 { return c.messages.Load() }

// DataMessages 返回已收到的行情事件数。
func (c *Conn) DataMessages() int64 { return c.data.Load() }

// LastMessage 返回最近一条消息的时间。
func (c *Conn) LastMessage() time.Time { return time.Unix(0, c.lastMessage.Load()) }

// Age 返回连接已存在的时长。
func (c *Conn) Age(now time.Time) time.Duration { return now.Sub(c.opened) }

func (c *Conn) closeErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *Conn) close(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		c.writeMu.Lock()
		_ = c.ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		c.writeMu.Unlock()
		_ = c.ws.Close()
		close(c.closed)
	})
}

// Close 关闭连接。
func (c *Conn) Close() { c.close(errors.New("closed")) }
