package stream

import (
	"context"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-go/log"
)

const (
	backoffMin = time.Second
	backoffMax = 30 * time.Second
	// defaultRotateLead 是在交易所强制断开之前多久主动轮换。
	defaultRotateLead = 30 * time.Minute
	// rotationConfirm 是轮换时等待新连接收到第一条消息的时间。
	rotationConfirm       = 30 * time.Second
	defaultStreamsPerConn = 1000
)

// ConnStatus 是一条连接（分片）的状态。
type ConnStatus struct {
	Group       string
	Streams     int
	Subscribed  bool
	Endpoint    string
	LastMessage time.Time
	Reconnects  int64
	Rejected    int
}

// ManagerOptions 调整连接管理的时间参数（测试用小值）。
type ManagerOptions struct {
	Resolve    Resolver
	RotateLead time.Duration
	// RotationWindow 判断此刻是否允许主动轮换：默认避开整点前后，只在分钟第 20～40 秒。
	RotationWindow func(time.Time) bool
	CheckInterval  time.Duration
	OnReconnect    func(group string)
	// MaxConnAge 覆盖来源声明的连接寿命（测试用）。
	MaxConnAge time.Duration
}

// DefaultRotationWindow 是默认的轮换窗口：非整点前后（:59、:00 之外），分钟第 20～40 秒。
func DefaultRotationWindow(now time.Time) bool {
	now = now.UTC()
	if now.Minute() == 59 || now.Minute() == 0 {
		return false
	}
	return now.Second() >= 20 && now.Second() <= 40
}

// ConnManager 管理一个来源的全部连接：按流数分片、各频率分连接、差量订阅、退避重连与端点轮换、寿命前主动轮换。
type ConnManager struct {
	source  Source
	onEvent func(Event)
	opts    ManagerOptions
	pace    *pacer

	mu     sync.Mutex
	slots  []*connSlot
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type connSlot struct {
	group   string
	streams map[string]struct{}
	// wake 通知监管协程有订阅变化。
	wake chan struct{}

	mu         sync.Mutex
	conn       *Conn
	subscribed map[string]struct{}
	rejected   map[string]string
	reconnects int64
	endpoint   int
	retired    bool
}

// NewConnManager 构造连接管理器；onEvent 在连接的读协程里调用，必须很快返回（只投递到闸门通道）。
func NewConnManager(source Source, onEvent func(Event), opts ManagerOptions) *ConnManager {
	if opts.RotateLead <= 0 {
		opts.RotateLead = defaultRotateLead
	}
	if opts.RotationWindow == nil {
		opts.RotationWindow = DefaultRotationWindow
	}
	if opts.CheckInterval <= 0 {
		opts.CheckInterval = time.Second
	}
	return &ConnManager{source: source, onEvent: onEvent, opts: opts, pace: newPacer(source.Limits().MessagesPerSecond)}
}

// Start 启动管理器；Close 停止全部连接。
func (m *ConnManager) Start(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ctx, m.cancel = context.WithCancel(ctx)
}

// Close 关闭全部连接并等待监管协程退出。
func (m *ConnManager) Close() {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

func (m *ConnManager) streamsPerConn() int {
	if limit := m.source.Limits().StreamsPerConn; limit > 0 {
		return min(limit, defaultStreamsPerConn)
	}
	return defaultStreamsPerConn
}

// Apply 设置期望订阅：分组（频率）→ 流。已在某个分片里的流留在原分片，新增的流填入有空位的分片，没有空位就新开分片；
// 不再需要的流退订，空的分片关闭。
func (m *ConnManager) Apply(desired map[string][]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ctx == nil {
		return
	}
	capacity := m.streamsPerConn()
	want := map[string]map[string]struct{}{}
	for group, streams := range desired {
		set := map[string]struct{}{}
		for _, stream := range streams {
			set[stream] = struct{}{}
		}
		if len(set) > 0 {
			want[group] = set
		}
	}
	var kept []*connSlot
	for _, slot := range m.slots {
		set := want[slot.group]
		slot.mu.Lock()
		next := map[string]struct{}{}
		for stream := range slot.streams {
			if _, ok := set[stream]; ok {
				next[stream] = struct{}{}
				delete(set, stream)
			}
		}
		slot.streams = next
		empty := len(next) == 0
		if empty {
			slot.retired = true
		}
		slot.mu.Unlock()
		notify(slot.wake)
		if !empty {
			kept = append(kept, slot)
		}
	}
	m.slots = kept
	groups := make([]string, 0, len(want))
	for group := range want {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	for _, group := range groups {
		rest := make([]string, 0, len(want[group]))
		for stream := range want[group] {
			rest = append(rest, stream)
		}
		sort.Strings(rest)
		for _, slot := range m.slots {
			if slot.group != group || len(rest) == 0 {
				continue
			}
			slot.mu.Lock()
			for len(rest) > 0 && len(slot.streams) < capacity {
				slot.streams[rest[0]] = struct{}{}
				rest = rest[1:]
			}
			slot.mu.Unlock()
			notify(slot.wake)
		}
		for len(rest) > 0 {
			slot := &connSlot{group: group, streams: map[string]struct{}{}, wake: make(chan struct{}, 1), subscribed: map[string]struct{}{}, rejected: map[string]string{}}
			for len(rest) > 0 && len(slot.streams) < capacity {
				slot.streams[rest[0]] = struct{}{}
				rest = rest[1:]
			}
			m.slots = append(m.slots, slot)
			m.wg.Add(1)
			go m.supervise(slot)
		}
	}
}

func notify(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// Ready 判断全部分片都已连上并订阅完成。
func (m *ConnManager) Ready() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, slot := range m.slots {
		slot.mu.Lock()
		ready := slot.conn != nil && len(slot.subscribed)+len(slot.rejected) >= len(slot.streams)
		slot.mu.Unlock()
		if !ready {
			return false
		}
	}
	return true
}

// Status 返回各分片的状态。
func (m *ConnManager) Status() []ConnStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]ConnStatus, 0, len(m.slots))
	for _, slot := range m.slots {
		slot.mu.Lock()
		status := ConnStatus{Group: slot.group, Streams: len(slot.streams), Reconnects: slot.reconnects, Rejected: len(slot.rejected)}
		if slot.conn != nil {
			status.Endpoint = slot.conn.endpoint
			status.LastMessage = slot.conn.LastMessage()
			status.Subscribed = len(slot.subscribed)+len(slot.rejected) >= len(slot.streams)
		}
		slot.mu.Unlock()
		result = append(result, status)
	}
	return result
}

func (m *ConnManager) dial(ctx context.Context, slot *connSlot) (*Conn, error) {
	endpoints := m.source.Endpoints()
	slot.mu.Lock()
	index := slot.endpoint % max(len(endpoints), 1)
	slot.mu.Unlock()
	conn, err := dialConn(ctx, m.source, endpoints[index], m.opts.Resolve, m.pace, m.onEvent)
	if err != nil {
		slot.mu.Lock()
		slot.endpoint++
		slot.mu.Unlock()
	}
	return conn, err
}

// supervise 维持一个分片的连接：断线退避重连（按端点轮换），订阅随期望变化做差量，寿命将到时在窗口内主动轮换。
func (m *ConnManager) supervise(slot *connSlot) {
	defer m.wg.Done()
	ctx := m.ctx
	backoff := backoffMin
	for {
		slot.mu.Lock()
		retired := slot.retired
		slot.mu.Unlock()
		if retired || ctx.Err() != nil {
			m.detach(slot)
			return
		}
		conn, err := m.dial(ctx, slot)
		if err != nil {
			log.Warnf("stream %s/%s dial failed: %v", m.source.MarketType(), slot.group, err)
			if !sleepCtx(ctx, jitter(backoff)) {
				return
			}
			backoff = min(backoff*2, backoffMax)
			continue
		}
		slot.mu.Lock()
		slot.conn, slot.subscribed, slot.rejected = conn, map[string]struct{}{}, map[string]string{}
		slot.mu.Unlock()
		if err := m.sync(ctx, slot, conn); err != nil {
			log.Warnf("stream %s/%s subscribe failed: %v", m.source.MarketType(), slot.group, err)
			conn.Close()
		} else {
			backoff = backoffMin
			m.serve(ctx, slot, conn)
		}
		slot.mu.Lock()
		if slot.conn == conn {
			slot.conn = nil
		}
		retired = slot.retired
		if !retired && ctx.Err() == nil {
			slot.reconnects++
		}
		slot.mu.Unlock()
		if !retired && ctx.Err() == nil && m.opts.OnReconnect != nil {
			m.opts.OnReconnect(slot.group)
		}
		if retired || ctx.Err() != nil {
			continue
		}
		if !sleepCtx(ctx, jitter(backoff)) {
			return
		}
	}
}

func (m *ConnManager) detach(slot *connSlot) {
	slot.mu.Lock()
	conn := slot.conn
	slot.conn = nil
	slot.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
}

// serve 在连接存活期间处理订阅变化与主动轮换；返回时连接已关闭或被替换。
func (m *ConnManager) serve(ctx context.Context, slot *connSlot, conn *Conn) {
	ticker := time.NewTicker(m.opts.CheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			conn.Close()
			return
		case <-conn.Done():
			return
		case <-slot.wake:
			slot.mu.Lock()
			retired := slot.retired
			slot.mu.Unlock()
			if retired {
				conn.Close()
				return
			}
			if err := m.sync(ctx, slot, conn); err != nil {
				log.Warnf("stream %s/%s resubscribe failed: %v", m.source.MarketType(), slot.group, err)
				conn.Close()
				return
			}
		case <-ticker.C:
			maxAge := m.source.Limits().MaxConnAge
			if m.opts.MaxConnAge > 0 {
				maxAge = m.opts.MaxConnAge
			}
			now := time.Now()
			if maxAge > 0 && conn.Age(now) >= maxAge-m.opts.RotateLead && m.opts.RotationWindow(now) {
				if next := m.rotate(ctx, slot); next != nil {
					conn.Close()
					conn = next
				}
			}
		}
	}
}

// rotate 新建一条连接、订阅同一组流、收到消息后再替换旧连接；轮换期间两条连接的事件都交给闸门，由闸门去重。
func (m *ConnManager) rotate(ctx context.Context, slot *connSlot) *Conn {
	next, err := m.dial(ctx, slot)
	if err != nil {
		log.Warnf("stream %s/%s rotation dial failed: %v", m.source.MarketType(), slot.group, err)
		return nil
	}
	streams := slot.snapshot()
	rejected, err := next.Subscribe(ctx, streams)
	if err != nil {
		next.Close()
		return nil
	}
	deadline := time.Now().Add(rotationConfirm)
	for next.Messages() == 0 && time.Now().Before(deadline) {
		if !sleepCtx(ctx, 50*time.Millisecond) {
			next.Close()
			return nil
		}
	}
	if next.Messages() == 0 {
		next.Close()
		return nil
	}
	slot.mu.Lock()
	slot.conn = next
	slot.subscribed = map[string]struct{}{}
	for _, stream := range streams {
		if _, bad := rejected[stream]; !bad {
			slot.subscribed[stream] = struct{}{}
		}
	}
	slot.rejected = rejected
	slot.mu.Unlock()
	log.Infof("stream %s/%s rotated connection streams=%d", m.source.MarketType(), slot.group, len(streams))
	return next
}

func (s *connSlot) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	streams := make([]string, 0, len(s.streams))
	for stream := range s.streams {
		streams = append(streams, stream)
	}
	sort.Strings(streams)
	return streams
}

// sync 让连接上的订阅与分片的期望一致：退订多余的、订阅缺的（含上次被拒绝的）。
func (m *ConnManager) sync(ctx context.Context, slot *connSlot, conn *Conn) error {
	slot.mu.Lock()
	var add, remove []string
	for stream := range slot.streams {
		if _, ok := slot.subscribed[stream]; !ok {
			add = append(add, stream)
		}
	}
	for stream := range slot.subscribed {
		if _, ok := slot.streams[stream]; !ok {
			remove = append(remove, stream)
		}
	}
	slot.mu.Unlock()
	sort.Strings(add)
	sort.Strings(remove)
	if len(remove) > 0 {
		if err := conn.Unsubscribe(ctx, remove); err != nil {
			return err
		}
		slot.mu.Lock()
		for _, stream := range remove {
			delete(slot.subscribed, stream)
		}
		slot.mu.Unlock()
	}
	if len(add) == 0 {
		return nil
	}
	rejected, err := conn.Subscribe(ctx, add)
	slot.mu.Lock()
	defer slot.mu.Unlock()
	for _, stream := range add {
		if reason, bad := rejected[stream]; bad {
			slot.rejected[stream] = reason
			continue
		}
		if err == nil {
			slot.subscribed[stream] = struct{}{}
			delete(slot.rejected, stream)
		}
	}
	if len(rejected) > 0 {
		log.Warnf("stream %s/%s rejected streams=%d", m.source.MarketType(), slot.group, len(rejected))
	}
	return err
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func jitter(d time.Duration) time.Duration {
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}
