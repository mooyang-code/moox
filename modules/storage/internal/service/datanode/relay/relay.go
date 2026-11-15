// Package relay 把 storage-primary 的周期完成标记转发到 EventBus：按游标跟随变更日志里的完成标记条目，
// 发布成功才推进游标，游标持久化在 Pebble 里。
package relay

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	"github.com/mooyang-code/moox/packages/jetstream"

	"trpc.group/trpc-go/trpc-go/log"
)

type Publisher interface {
	PublishMessage(context.Context, []byte) error
}

// PublisherHealth 让转发协程上报 EventBus 连接状态；测试用的发布器可以不实现。
type PublisherHealth interface {
	Ready() bool
}

// PublishAckPublisher 是可选接口：基于 JetStream 的发布器用它带回“重复消息”的确认。
type PublishAckPublisher interface {
	PublishMessageWithAck(context.Context, []byte) (*jetstream.PublishAck, error)
}

// cursorSaveInterval 是只扫描、没有发布时游标落盘的最短间隔。发布过事件的那一轮结束时立即落盘。
const cursorSaveInterval = time.Minute

type RelayOptions struct {
	PollInterval   time.Duration
	BatchSize      int
	PublishTimeout time.Duration
	Metrics        *observability.ViewMetrics
	// ReconnectAfterFailures 是连续发布失败多少次后替换 EventBus 连接（发布器实现了 Reconnect 才生效），
	// EventBus 重启后不用重启进程。
	ReconnectAfterFailures int
	ReconnectCooldown      time.Duration
	ErrorReporter          func(error)
}

// Relay 单协程按变更日志的顺序转发完成标记：某一条发布失败就停在这条上，稍后从它重试，不越过它发后面的。
type Relay struct {
	store           *pebble.Store
	publisher       Publisher
	options         RelayOptions
	metrics         *observability.ViewMetrics
	stop            chan struct{}
	done            chan struct{}
	started         chan struct{}
	once            sync.Once
	startOnce       sync.Once
	publishFailures int
	lastReconnect   time.Time

	// cursor 是已处理到的变更序号（它之前的完成标记都已发布或跳过）；saved 是已经落盘的值。
	cursor       uint64
	saved        uint64
	savedAt      time.Time
	cursorLoaded bool
	// hasMore 表示上一轮扫描因为一页的上限停下，后面还有条目，立即再扫一轮。
	hasMore bool
}

func NewRelay(store *pebble.Store, publisher Publisher, opts RelayOptions) (*Relay, error) {
	if store == nil || publisher == nil {
		return nil, errors.New("store and publisher are required")
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = time.Second
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 500
	}
	if opts.PublishTimeout <= 0 {
		// 到 EventBus 的链路可能带宽很小：1 MiB 的事件正常也要几秒，5 秒超时会让发布无限重试，
		// broker 明明可达，事件却一直发不出去。
		opts.PublishTimeout = 30 * time.Second
	}
	if opts.ReconnectAfterFailures <= 0 {
		opts.ReconnectAfterFailures = 3
	}
	if opts.ReconnectCooldown <= 0 {
		opts.ReconnectCooldown = 5 * time.Second
	}
	if opts.Metrics == nil {
		opts.Metrics = observability.DefaultViewMetrics
	}
	relay := &Relay{store: store, publisher: publisher, options: opts, metrics: opts.Metrics, stop: make(chan struct{}), done: make(chan struct{}), started: make(chan struct{})}
	relay.observePublisherReady()
	return relay, nil
}

func (r *Relay) Start(ctx context.Context) {
	r.startOnce.Do(func() {
		close(r.started)
		go func() {
			defer close(r.done)
			ticker := time.NewTicker(r.options.PollInterval)
			defer ticker.Stop()
			for {
				err := r.flush(ctx)
				if err != nil {
					r.report(err)
				}
				if err == nil && r.hasMore {
					// 积压超过一页：连续转发，仍响应停止。
					select {
					case <-r.stop:
						return
					case <-ctx.Done():
						return
					default:
					}
					continue
				}
				select {
				case <-ticker.C:
					// 定时扫描兜底：发布失败后的重试，以及只有行写入时让游标跟上日志头部，不落到保留期之外。
				case <-r.store.MarkerWake():
					// 有新的完成标记提交：立即发布，不等定时扫描。失败后也会被它唤醒，重试的还是停住的那一条。
				case <-r.stop:
					return
				case <-ctx.Done():
					return
				}
			}
		}()
	})
}

func (r *Relay) report(err error) {
	if err == nil {
		return
	}
	if r.options.ErrorReporter != nil {
		r.options.ErrorReporter(err)
		return
	}
	// 偶发失败按警告记，连续失败到需要重连时升为错误；积压由 moox_storage_outbox_oldest_age_seconds 的检查告警。
	if r.publishFailures >= r.options.ReconnectAfterFailures {
		log.Errorf("storage 完成事件转发连续失败 %d 次: %v", r.publishFailures, err)
		return
	}
	log.Warnf("storage 完成事件转发失败: %v", err)
}

func (r *Relay) Close() {
	if r == nil {
		return
	}
	r.once.Do(func() { close(r.stop) })
	select {
	case <-r.started:
		<-r.done
	default:
	}
}

// flush 从游标之后扫一页变更日志，按序发布其中的完成标记。全部成功后游标推进到已扫描的位置；
// 某一条失败时游标停在它前面，返回错误，下一轮从它重试。
func (r *Relay) flush(ctx context.Context) error {
	r.hasMore = false
	r.observePublisherReady()
	if !r.cursorLoaded {
		cursor, err := r.store.MarkerRelayCursor()
		if err != nil {
			return err
		}
		r.cursor, r.saved, r.savedAt, r.cursorLoaded = cursor, cursor, time.Now(), true
	}
	page, err := r.store.ReadMarkerChanges(ctx, r.cursor, r.options.BatchSize)
	if err != nil {
		return err
	}
	if page.Skipped {
		// 转发停了超过变更日志保留期：中间的完成标记已随日志删除，发不出去了，从最早还在的条目接着发。
		log.Errorf("storage 完成事件转发的游标 %d 已落在变更日志保留期之外，跳到最早可用的条目；被清理掉的周期完成事件没有转发", r.cursor)
	}
	published := 0
	for i, marker := range page.Markers {
		// 关闭后不再发下一条；已发布的进度照常落盘。
		select {
		case <-r.stop:
			return r.saveCursor(true)
		default:
		}
		sent, err := r.relayMarker(ctx, marker)
		if err != nil {
			r.metrics.SetOutboxSnapshotAt(len(page.Markers)-i, marker.CreatedAt)
			return errors.Join(err, r.saveCursor(published > 0))
		}
		if sent {
			published++
		}
		r.cursor = marker.Sequence
	}
	r.cursor = page.Scanned
	r.hasMore = page.HasMore
	// 这一页全部发完就清零：后面还有积压时也一样，否则排空期间会一直挂着上次失败时的年龄。
	r.metrics.SetOutboxSnapshotAt(0, time.Time{})
	return r.saveCursor(published > 0)
}

// relayMarker 发布一条完成标记，返回是否真的发了。标记本体已经不在（数据集被删）或通不过契约校验时跳过。
func (r *Relay) relayMarker(ctx context.Context, marker pebble.MarkerChange) (bool, error) {
	publishable, err := r.store.MarkerPublishable(marker.Data)
	if errors.Is(err, pebble.ErrInvalidMarker) {
		// 永远发不出去：记错误后跳过，继续转发其后的事件。
		log.Errorf("storage 完成事件转发跳过变更序号 %d 的标记：%v", marker.Sequence, err)
		r.metrics.IncOutboxPublishError()
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !publishable {
		return false, nil
	}
	publishCtx, cancel := context.WithTimeout(ctx, r.options.PublishTimeout)
	defer cancel()
	ack, err := r.publish(publishCtx, marker.Data)
	if err != nil {
		r.metrics.IncOutboxPublishError()
		r.observePublisherReady()
		r.publishFailures++
		r.reconnectPublisher(ctx)
		return false, err
	}
	if ack != nil && ack.Duplicate {
		r.metrics.IncOutboxDuplicatePublish()
	}
	r.metrics.ObserveOutboxPublishSuccess()
	r.publishFailures = 0
	return true, nil
}

// saveCursor 把游标落盘。now 为假时（这一轮只扫描、没有发布）最多每 cursorSaveInterval 落盘一次：
// 行写入远多于完成标记，不必每扫过一批行条目就写一次 Pebble；重启后多扫一段行条目没有副作用。
func (r *Relay) saveCursor(now bool) error {
	if r.cursor == r.saved {
		return nil
	}
	if !now && time.Since(r.savedAt) < cursorSaveInterval {
		return nil
	}
	if err := r.store.SetMarkerRelayCursor(r.cursor); err != nil {
		return fmt.Errorf("保存完成事件转发游标: %w", err)
	}
	r.saved, r.savedAt = r.cursor, time.Now()
	return nil
}

// reconnectPublisher 尽力重连。连不上时游标不动，下一轮再重连、再发布，不丢事件。
func (r *Relay) reconnectPublisher(ctx context.Context) {
	if r.publishFailures < r.options.ReconnectAfterFailures {
		return
	}
	reconnecter, ok := r.publisher.(interface{ Reconnect(context.Context) error })
	if !ok {
		return
	}
	if !r.lastReconnect.IsZero() && time.Since(r.lastReconnect) < r.options.ReconnectCooldown {
		return
	}
	r.lastReconnect = time.Now()
	reconnectCtx, cancel := context.WithTimeout(ctx, r.options.PublishTimeout)
	defer cancel()
	if err := reconnecter.Reconnect(reconnectCtx); err != nil {
		r.metrics.ObserveOutboxReconnect(false)
		r.report(fmt.Errorf("reconnect storage eventbus publisher: %w", err))
		return
	}
	if health, ok := r.publisher.(PublisherHealth); ok && !health.Ready() {
		r.metrics.ObserveOutboxReconnect(false)
		r.report(errors.New("reconnect storage eventbus publisher: replacement connection is not ready"))
		return
	}
	r.metrics.ObserveOutboxReconnect(true)
	r.publishFailures = 0
}

func (r *Relay) observePublisherReady() {
	if health, ok := r.publisher.(PublisherHealth); ok {
		r.metrics.SetOutboxPublisherReady(health.Ready())
		return
	}
	// 没有连接状态的发布器（测试用）视为就绪；生产的 JetStream 发布器都实现了 PublisherHealth。
	r.metrics.SetOutboxPublisherReady(true)
}

func (r *Relay) publish(ctx context.Context, data []byte) (*jetstream.PublishAck, error) {
	if publisher, ok := r.publisher.(PublishAckPublisher); ok {
		return publisher.PublishMessageWithAck(ctx, data)
	}
	return nil, r.publisher.PublishMessage(ctx, data)
}
