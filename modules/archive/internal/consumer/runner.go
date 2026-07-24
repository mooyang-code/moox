package consumer

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/packages/jetstream"
)

type PullConsumer interface {
	Fetch(context.Context, int) ([]*jetstream.Delivery, error)
	Close() error
}

type Runner struct {
	shared *jetstream.Runner
	batch  int
}

func NewRunner(consumer PullConsumer, handler *Handler, batch int) *Runner {
	if batch <= 0 {
		batch = 1
	}
	return &Runner{shared: jetstream.NewRunner(rawPullAdapter{consumer}, decisionHandler{handler: handler}, jetstream.RunnerConfig{BatchSize: batch}), batch: batch}
}

type rawPullAdapter struct{ PullConsumer }

func (a rawPullAdapter) Fetch(ctx context.Context, batch int) ([]*jetstream.Delivery, error) {
	return a.PullConsumer.Fetch(ctx, batch)
}

func (r *Runner) Run(ctx context.Context) error {
	if r == nil || r.shared == nil {
		return jetstream.ErrInvalidConsumer
	}
	return r.shared.Run(ctx)
}

// Kept for callers of the old archive runner helper; retry pacing now belongs
// to JetStream's NAK delay and is not used by the shared transport loop.
func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type deliveryAdapter struct{ *jetstream.Delivery }

func (d deliveryAdapter) RawEnvelope() []byte { return d.RawData }
func (d deliveryAdapter) MessageID() string {
	return d.RawMessageID
}
func (d deliveryAdapter) Subject() string        { return d.Delivery.Subject }
func (d deliveryAdapter) StreamSequence() uint64 { return d.Delivery.StreamSeq }
func (d deliveryAdapter) DeliveryCount() uint64  { return d.Delivery.DeliveryCount }

type decisionHandler struct{ handler *Handler }

func (h decisionHandler) Handle(ctx context.Context, delivery *jetstream.Delivery) jetstream.HandlerResult {
	if h.handler == nil || delivery == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: jetstream.ErrInvalidDelivery}
	}
	return h.handler.HandleDecision(ctx, deliveryAdapter{delivery})
}
