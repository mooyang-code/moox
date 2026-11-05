// Package eventconsumer 把事件中心可订阅的事件从 EventBus 取下来：每个事件一个固定的 durable，
// 连接不上时每隔一段时间重试，EventBus 比事件中心晚启动也能自动连上。
package eventconsumer

import (
	"context"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/catalog"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/dispatch"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/telemetry"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"trpc.group/trpc-go/trpc-go/log"
)

// Processor 处理一条原始事件（dispatch.Intake 实现它）。
type Processor interface {
	Process(ctx context.Context, raw []byte, subject, messageID, contentType string) (dispatch.Outcome, error)
}

// Handler 把投递交给 Processor，并把处理结果翻译成确认、重投或终止；确认策略只在这一层决定。
type Handler struct {
	Processor Processor
}

// Handle 实现 jetstream.DeliveryHandler。
func (h Handler) Handle(ctx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
	if delivery == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: jetstream.ErrInvalidDelivery}
	}
	if delivery.DecodeError != nil {
		telemetry.EventErrors.WithLabelValues("transport").Inc()
		log.WarnContextf(ctx, "事件传输校验失败，已终止: %v", delivery.DecodeError)
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: delivery.DecodeError}
	}
	outcome, err := h.Processor.Process(ctx, delivery.RawData, delivery.Subject, delivery.RawMessageID, delivery.ContentType)
	switch outcome {
	case dispatch.Accepted:
		return jetstream.HandlerResult{Decision: jetstream.ACK}
	case dispatch.RetryLater:
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: dispatch.RetryDelay, Err: err}
	}
	return jetstream.HandlerResult{Decision: jetstream.TERM, Err: err}
}

// Supervisor 维护到 EventBus 的连接和各个事件的消费者。
type Supervisor struct {
	// Config 返回本次连接用的配置；每次重连都重新取，凭据文件被更新后无需重启。
	Config   func() (jetstream.Config, error)
	Registry *events.Registry
	Handler  jetstream.DeliveryHandler
	// SetConnected 在连接状态变化时被调用（健康检查里展示）。
	SetConnected func(bool)
	// ReconnectInterval 是连接失败后的重试间隔。
	ReconnectInterval time.Duration
	// FetchMaxWait 是一次拉取最长等待的时间。
	FetchMaxWait time.Duration
	// RetryInterval 是消费者创建或运行失败后重新创建前的等待。
	RetryInterval time.Duration
}

func (s *Supervisor) setConnected(connected bool) {
	if s.SetConnected != nil {
		s.SetConnected(connected)
	}
}

// Run 一直维护到 ctx 结束。
func (s *Supervisor) Run(ctx context.Context) error {
	defer s.setConnected(false)
	for ctx.Err() == nil {
		client, err := s.connect(ctx)
		if err != nil {
			log.WarnContextf(ctx, "连接 EventBus 失败，%s 后重试: %v", s.ReconnectInterval, err)
			if !sleep(ctx, s.ReconnectInterval) {
				return ctx.Err()
			}
			continue
		}
		s.setConnected(true)
		s.runAll(ctx, client)
		s.setConnected(false)
		_ = client.Close()
		if !sleep(ctx, s.RetryInterval) {
			return ctx.Err()
		}
	}
	return ctx.Err()
}

func (s *Supervisor) connect(ctx context.Context) (*jetstream.Client, error) {
	cfg, err := s.Config()
	if err != nil {
		return nil, err
	}
	return jetstream.Connect(ctx, cfg)
}

func (s *Supervisor) runAll(ctx context.Context, client *jetstream.Client) {
	var group sync.WaitGroup
	for _, entry := range catalog.Subscribable() {
		group.Add(1)
		go func() {
			defer group.Done()
			s.runEvent(ctx, client, entry)
		}()
	}
	group.Wait()
}

// runEvent 为一个事件维护消费者：创建、运行，失败后等一会儿重新创建。
func (s *Supervisor) runEvent(ctx context.Context, client *jetstream.Client, entry catalog.Entry) {
	name := events.CallbackDispatcherConsumerName(entry.Event)
	failures := 0
	for ctx.Err() == nil {
		consumer, err := events.NewConsumer(ctx, client, s.Registry, events.ConsumerConfig{
			Name: name, Event: entry.Event,
			// 写库失败不能丢事件，所以不限制投递次数。
			AckWait: time.Minute, MaxDeliver: -1, MaxAckPending: 64,
			FetchMaxWait: s.FetchMaxWait, DeliverPolicy: entry.DeliverPolicy,
			DeliverDecodeErrors: true,
		})
		if err != nil {
			failures++
			log.WarnContextf(ctx, "创建事件 %s 的消费者 %s 失败: %v", entry.Name(), name, err)
			// 连接可能已经失效（EventBus 重启后订阅一直无效），连续失败几次就整体重连。
			if failures >= 3 {
				if err := client.Reconnect(ctx); err != nil {
					log.WarnContextf(ctx, "重新连接 EventBus 失败: %v", err)
				}
				failures = 0
			}
			if !sleep(ctx, s.RetryInterval) {
				return
			}
			continue
		}
		failures = 0
		runner := jetstream.NewRunner(consumer, s.Handler, jetstream.RunnerConfig{
			BatchSize: 16,
			ErrorReporter: jetstream.ErrorReporterFunc(func(err error) {
				log.WarnContextf(ctx, "事件 %s 的投递出错: %v", entry.Name(), err)
			}),
		})
		runErr := runner.Run(ctx)
		_ = consumer.Close()
		if ctx.Err() != nil {
			return
		}
		if runErr != nil {
			log.WarnContextf(ctx, "事件 %s 的消费者停止: %v", entry.Name(), runErr)
		}
		if !sleep(ctx, s.RetryInterval) {
			return
		}
	}
}

func sleep(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
