package outbox

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type Connector func(context.Context) (JetStreamClient, error)

type RuntimeConfig struct {
	Connector         Connector
	Probe             func(context.Context, JetStreamClient) error
	Store             ResultStore
	InstanceID        string
	RelayInterval     time.Duration
	ReconnectInterval time.Duration
	BatchSize         int
	// PublishTimeout 是单次发布等待确认的上限；0 表示 DefaultPublishTimeout。
	PublishTimeout time.Duration
	// Logf 记录连接、探针与投递的异常；同一条错误连续出现只记一次，恢复后再记一条。
	Logf func(format string, args ...any)
}

type Runtime struct {
	cfg       RuntimeConfig
	lastIssue string
	// failingSince 是发布持续失败的起点（Unix 纳秒，0 表示没有在失败）：新结果会替代、过期取消旧的待投递结果，
	// 最老待投递结果的年龄不会超过一根 bar，停滞只能看发布本身失败了多久。
	failingSince atomic.Int64
	mu           sync.RWMutex
	client       JetStreamClient
	cancel       context.CancelFunc
	done         chan struct{}
	started      bool
	closed       bool
	validated    bool
}

func NewRuntime(cfg RuntimeConfig) (*Runtime, error) {
	if cfg.Connector == nil || cfg.Probe == nil || cfg.Store == nil {
		return nil, errors.New("策略 EventBus 运行时需要连接器、探针与存储")
	}
	if cfg.RelayInterval <= 0 || cfg.ReconnectInterval <= 0 || cfg.BatchSize <= 0 {
		return nil, errors.New("策略 EventBus 运行时的间隔与批量必须大于 0")
	}
	return &Runtime{cfg: cfg, done: make(chan struct{})}, nil
}

func (r *Runtime) Start(parent context.Context) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return errors.New("策略 EventBus 运行时已关闭")
	}
	if r.started {
		r.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	r.cancel = cancel
	r.started = true
	r.mu.Unlock()
	go r.run(ctx)
	return nil
}

func (r *Runtime) run(ctx context.Context) {
	defer close(r.done)
	for {
		if ctx.Err() != nil {
			return
		}
		client := r.currentClient()
		if client == nil || !client.Ready() {
			r.dropClient(client)
			connected, err := r.cfg.Connector(ctx)
			if err != nil {
				r.issue("策略出站连接 EventBus 失败，稍后重试：%v", err)
				if !waitFor(ctx, r.cfg.ReconnectInterval) {
					return
				}
				continue
			}
			r.setClient(connected)
			client = connected
		}
		if !r.isValidated() {
			if err := r.cfg.Probe(ctx, client); err != nil {
				r.issue("策略出站的发布探针失败，稍后重试：%v", err)
				r.dropClient(client)
				if !waitFor(ctx, r.cfg.ReconnectInterval) {
					return
				}
				continue
			}
			r.setValidated(true)
		}
		eventPublisher := client.EventPublisher()
		if eventPublisher == nil {
			r.dropClient(client)
			if !waitFor(ctx, r.cfg.ReconnectInterval) {
				return
			}
			continue
		}
		relay := &Relay{Store: r.cfg.Store, Publisher: &JetStreamPublisher{Publisher: eventPublisher, InstanceID: r.cfg.InstanceID, Timeout: r.cfg.PublishTimeout}}
		if err := relay.PublishPending(ctx, r.cfg.BatchSize); err != nil {
			var publishFailure *PublishFailure
			if errors.As(err, &publishFailure) {
				r.failingSince.CompareAndSwap(0, time.Now().UnixNano())
				r.issue("策略目标事件发布失败，断开重连：%v", err)
				r.dropClient(client)
				if !waitFor(ctx, r.cfg.ReconnectInterval) {
					return
				}
				continue
			}
			// 永久失败的结果已被取消（目标不会到达 Trade），存储错误则保持连接等下一轮重试：都要留下记录。
			r.issue("策略目标投递异常：%v", err)
			if !waitFor(ctx, r.cfg.RelayInterval) {
				return
			}
			continue
		}
		r.failingSince.Store(0)
		r.recovered()
		if !waitFor(ctx, r.cfg.RelayInterval) {
			return
		}
	}
}

// issue 记录一条投递异常；与上一条相同则不重复记录。
func (r *Runtime) issue(format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	r.mu.Lock()
	repeated := message == r.lastIssue
	r.lastIssue = message
	r.mu.Unlock()
	if !repeated && r.cfg.Logf != nil {
		r.cfg.Logf("%s", message)
	}
}

// recovered 在一轮投递正常完成后记录恢复。
func (r *Runtime) recovered() {
	r.mu.Lock()
	had := r.lastIssue != ""
	r.lastIssue = ""
	r.mu.Unlock()
	if had && r.cfg.Logf != nil {
		r.cfg.Logf("策略目标投递已恢复")
	}
}

// PublishFailingFor 返回到 now 为止发布已持续失败的时长；没有在失败时返回 0。
func (r *Runtime) PublishFailingFor(now time.Time) time.Duration {
	if r == nil {
		return 0
	}
	since := r.failingSince.Load()
	if since == 0 {
		return 0
	}
	if failing := now.Sub(time.Unix(0, since)); failing > 0 {
		return failing
	}
	return 0
}

func (r *Runtime) Connected() bool {
	client := r.currentClient()
	return client != nil && client.Ready() && r.isValidated()
}

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		started := r.started
		done := r.done
		r.mu.Unlock()
		if started {
			<-done
		}
		return nil
	}
	r.closed = true
	started := r.started
	cancel := r.cancel
	done := r.done
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if started {
		<-done
	}
	client := r.currentClient()
	if client != nil {
		return client.Close()
	}
	return nil
}

func (r *Runtime) currentClient() JetStreamClient {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.client
}

func (r *Runtime) setClient(client JetStreamClient) {
	r.mu.Lock()
	old := r.client
	r.client = client
	r.validated = false
	r.mu.Unlock()
	if old != nil && old != client {
		_ = old.Close()
	}
}

func (r *Runtime) dropClient(expected JetStreamClient) {
	r.mu.Lock()
	client := r.client
	if expected == nil || client == expected {
		r.client = nil
		r.validated = false
	}
	r.mu.Unlock()
	if client != nil && (expected == nil || client == expected) {
		_ = client.Close()
	}
}

func (r *Runtime) isValidated() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.validated
}

func (r *Runtime) setValidated(validated bool) {
	r.mu.Lock()
	r.validated = validated
	r.mu.Unlock()
}

func waitFor(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
