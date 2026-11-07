// Package eventbus 是 Collector 级的 EventBus 接入：一条共享连接（自带重试，不依赖 EventBus 先启动），
// 以及一个独立的发件箱中继循环。K 线合成、下架检测等所有事件生产者都往同一个发件箱写，由中继发布；
// 中继不依赖 K 线合成是否开启。
package eventbus

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/attention"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"trpc.group/trpc-go/trpc-go/log"
)

// Config 是 Collector 到 EventBus 的连接。凭据文件存放用户名与密码，权限必须是 0600。
type Config struct {
	URLs           []string `yaml:"urls"`
	CredentialFile string   `yaml:"credential_file"`
}

// Enabled 报告是否配置了事件总线。
func (c Config) Enabled() bool {
	return len(c.URLs) > 0 || c.CredentialFile != ""
}

// hook 是连接建立之后要做的一件事（例如启动 K 线合成的消费者）：失败只影响它自己，之后单独重试。
type hook struct {
	run         func(context.Context, *jetstream.Client) (func() error, error)
	closer      func() error
	done        bool
	lastFailure time.Time
}

// Hub 持有 Collector 与 EventBus 的共享连接。
type Hub struct {
	cfg      Config
	registry *events.Registry

	// RetryInterval 是连接或回调失败之后再次尝试的最短间隔。
	RetryInterval time.Duration
	// Now 可由测试替换。
	Now func() time.Time

	mu          sync.Mutex
	client      *jetstream.Client
	publisher   *events.Publisher
	lastFailure time.Time
	hooks       []*hook
}

// NewHub 创建连接管理器；Ensure 才真正去连，失败后一分钟内不再重试。
func NewHub(cfg Config, registry *events.Registry) *Hub {
	return &Hub{cfg: cfg, registry: registry, RetryInterval: time.Minute, Now: time.Now}
}

// OnConnected 注册连接建立之后要做的事（例如启动 K 线合成的消费者）。回调可以返回一个关闭函数，Hub 关闭时先于连接调用它。
// 回调失败不影响连接和发布：连接与发布器照样保留（下架事件等照常发布），失败的回调在之后的 Ensure 里单独重试。
func (h *Hub) OnConnected(callback func(context.Context, *jetstream.Client) (func() error, error)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hooks = append(h.hooks, &hook{run: callback})
}

var errNotYetRetry = errors.New("EventBus 还没连上，稍后重试")

// Ensure 保证已经连上：已连接就只重试还没成功的回调，否则尝试连接（失败后 RetryInterval 内直接返回错误）。
// 返回错误只表示连接没建立；回调的失败记在日志里，不影响返回值。连接建立之后的断线由 NATS 客户端自己重连。
func (h *Hub) Ensure(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.client == nil {
		if !h.lastFailure.IsZero() && h.Now().Sub(h.lastFailure) < h.RetryInterval {
			return errNotYetRetry
		}
		if err := h.connect(ctx); err != nil {
			h.lastFailure = h.Now()
			return err
		}
	}
	h.runHooks(ctx)
	return nil
}

func (h *Hub) connect(ctx context.Context) error {
	clientConfig := jetstream.ConfigFromEnv(append([]string(nil), h.cfg.URLs...), "moox-collector")
	if h.cfg.CredentialFile != "" {
		if err := clientConfig.ApplyCredentialFile(jetstream.ExpandCredentialPath(h.cfg.CredentialFile)); err != nil {
			return err
		}
	}
	client, err := jetstream.Connect(ctx, clientConfig)
	if err != nil {
		return err
	}
	publisher, err := events.NewPublisher(client, h.registry)
	if err != nil {
		_ = client.Close()
		return err
	}
	h.client, h.publisher = client, publisher
	return nil
}

// runHooks 尝试还没成功的回调；失败的回调 RetryInterval 之后再试。
func (h *Hub) runHooks(ctx context.Context) {
	for _, item := range h.hooks {
		if item.done {
			continue
		}
		if !item.lastFailure.IsZero() && h.Now().Sub(item.lastFailure) < h.RetryInterval {
			continue
		}
		closer, err := item.run(ctx, h.client)
		if err != nil {
			item.lastFailure = h.Now()
			log.WarnContextf(ctx, "[Collector] EventBus 连接后的初始化失败（连接与发布不受影响，%s 后重试）: %v", h.RetryInterval, err)
			continue
		}
		item.closer, item.done = closer, true
	}
}

// Client 返回共享连接；还没连上时为 nil。
func (h *Hub) Client() *jetstream.Client {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.client
}

// Publisher 返回共享连接上的事件发布者；还没连上时为 nil。
func (h *Hub) Publisher() *events.Publisher {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.publisher
}

// Close 先停掉依赖这条连接的消费者，再关闭连接。
func (h *Hub) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.client == nil {
		return nil
	}
	var err error
	for i := len(h.hooks) - 1; i >= 0; i-- {
		if h.hooks[i].done && h.hooks[i].closer != nil {
			err = errors.Join(err, h.hooks[i].closer())
		}
		h.hooks[i].done, h.hooks[i].closer = false, nil
	}
	err = errors.Join(err, h.client.Close())
	h.client, h.publisher = nil, nil
	return err
}

// Outbox 是本地发件箱（store.ResampleStateRepository 实现它）。
type Outbox interface {
	PendingEvents(ctx context.Context, limit int) ([]store.OutboxEvent, error)
	MarkPublished(ctx context.Context, id int64) error
	MarkDead(ctx context.Context, id int64) error
	DeadEvents(ctx context.Context) (int64, error)
}

// StuckAfter 是发件箱里最老的事件超过多久没发布出去就上报关注项。
const StuckAfter = 10 * time.Minute

// Relay 把本地发件箱里的事件发布到 JetStream：以事件 ID 作为 Msg-Id 去重，发布成功才标记已发布。
// 无法解码的事件标成死信，不堵住后面的事件。
type Relay struct {
	Outbox   Outbox
	Registry *events.Registry
	Hub      *Hub
	// Interval 是轮询发件箱、重试连接的间隔。
	Interval time.Duration
	// Attention 非空时上报发件箱积压与死信。
	Attention *attention.Attention
	Now       func() time.Time

	wake chan struct{}
	mu   sync.Mutex
}

// NewRelay 创建中继。
func NewRelay(outbox Outbox, registry *events.Registry, hub *Hub, interval time.Duration) *Relay {
	return &Relay{Outbox: outbox, Registry: registry, Hub: hub, Interval: interval, Now: time.Now, wake: make(chan struct{}, 1)}
}

// Wake 通知中继立即检查发件箱。
func (r *Relay) Wake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Run 一直运行到 ctx 结束：未连上时按间隔重试连接，连上后发布发件箱。
func (r *Relay) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.Interval)
	defer ticker.Stop()
	for {
		// 没有待发布的事件时也要保持连接：K 线合成的消费者靠它启动。
		if err := r.Hub.Ensure(ctx); err != nil && ctx.Err() == nil && !errors.Is(err, errNotYetRetry) {
			log.WarnContextf(ctx, "[Collector] 连接 EventBus 失败，%s 后重试: %v", r.Hub.RetryInterval, err)
		}
		if err := r.Flush(ctx); err != nil && ctx.Err() == nil && !errors.Is(err, errNotYetRetry) {
			log.WarnContextf(ctx, "[Collector] 事件中继发布失败，稍后重试: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		case <-r.wake:
		}
	}
}

// Flush 发布发件箱里全部待发布的事件。EventBus 没连上时先尝试连接，连不上就保留在发件箱里。
func (r *Relay) Flush(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	err := r.flush(ctx)
	r.reportAttention(ctx)
	return err
}

func (r *Relay) flush(ctx context.Context) error {
	pending, err := r.Outbox.PendingEvents(ctx, 100)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	if err := r.Hub.Ensure(ctx); err != nil {
		return fmt.Errorf("连接 EventBus: %w", err)
	}
	publisher := r.Hub.Publisher()
	if publisher == nil {
		return errNotYetRetry
	}
	var errs []error
	for _, event := range pending {
		message, err := r.Registry.UnmarshalMessage(event.Payload)
		if err != nil {
			// 无效事件重试也没用：标成死信（并上报关注项），不堵住后面的事件。
			log.ErrorContextf(ctx, "[Collector] 发件箱事件 %s 无效，已搁置: %v", event.EventID, err)
			if markErr := r.Outbox.MarkDead(ctx, event.ID); markErr != nil {
				errs = append(errs, markErr)
				break
			}
			continue
		}
		if _, err := publisher.PublishMessage(ctx, message); err != nil {
			errs = append(errs, fmt.Errorf("发布事件 %s: %w", event.EventID, err))
			break
		}
		if err := r.Outbox.MarkPublished(ctx, event.ID); err != nil {
			errs = append(errs, err)
			break
		}
	}
	return errors.Join(errs...)
}

// reportAttention 上报发件箱的状态：最老的待发布事件超过 StuckAfter，或者有被搁置的无效事件。
func (r *Relay) reportAttention(ctx context.Context) {
	if r.Attention == nil {
		return
	}
	var details []string
	if pending, err := r.Outbox.PendingEvents(ctx, 1); err == nil && len(pending) > 0 {
		if age := r.Now().Sub(pending[0].CreatedAt); age > StuckAfter {
			details = append(details, fmt.Sprintf("事件发件箱有事件 %d 分钟没发布出去（EventBus 连不上或发布被拒）", int(age/time.Minute)))
		}
	}
	if dead, err := r.Outbox.DeadEvents(ctx); err == nil && dead > 0 {
		details = append(details, fmt.Sprintf("事件发件箱有 %d 条无效事件被搁置，需要人工查看", dead))
	}
	r.Attention.Set("outbox", map[string][]string{attention.KindOutboxStuck: details})
}
