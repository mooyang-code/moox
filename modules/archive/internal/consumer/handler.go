package consumer

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	"github.com/mooyang-code/moox/modules/archive/internal/journal"
	"github.com/mooyang-code/moox/packages/jetstream"
)

type Delivery interface {
	RawEnvelope() []byte
	MessageID() string
	Subject() string
	StreamSequence() uint64
	DeliveryCount() uint64
	Ack(context.Context) error
	Nak(context.Context, time.Duration) error
	InProgress(context.Context) error
	Term(context.Context) error
}

type DecoderAPI interface {
	DecodeEvent([]byte, string, string) (domain.EventBatch, Decision, error)
}
type Journal interface {
	Append(context.Context, domain.EventBatch) (journal.AppendResult, error)
	Quarantine(context.Context, journal.QuarantineRecord) error
}
type DirtyNotifier interface{ Notify([]domain.PartitionKey) }
type RetryScheduledError struct{ Delay time.Duration }

func (e *RetryScheduledError) Error() string {
	return fmt.Sprintf("archive delivery retry scheduled after %s", e.Delay)
}

type Handler struct {
	decoder  DecoderAPI
	journal  Journal
	notifier DirtyNotifier
	ackWait  time.Duration
}

func NewHandler(decoder DecoderAPI, store Journal, notifier DirtyNotifier) *Handler {
	return &Handler{decoder: decoder, journal: store, notifier: notifier, ackWait: 5 * time.Minute}
}

func (h *Handler) Handle(ctx context.Context, delivery Delivery) error {
	result := h.HandleDecision(ctx, delivery)
	if delivery == nil {
		return result.Err
	}
	var actionErr error
	switch result.Decision {
	case jetstream.ACK:
		actionErr = delivery.Ack(ctx)
	case jetstream.RETRY:
		actionErr = delivery.Nak(ctx, result.Delay)
	case jetstream.TERM:
		actionErr = delivery.Term(ctx)
	default:
		actionErr = fmt.Errorf("invalid archive handler decision %d", result.Decision)
	}
	return errors.Join(result.Err, actionErr)
}

// HandleDecision contains archive business processing only. The shared
// JetStream runner owns the resulting ACK/NAK/TERM action.
func (h *Handler) HandleDecision(ctx context.Context, delivery Delivery) jetstream.HandlerResult {
	if delivery == nil {
		return jetstream.HandlerResult{Decision: jetstream.TERM, Err: fmt.Errorf("delivery is nil")}
	}
	if delivery.RawEnvelope() == nil {
		return h.reject(ctx, delivery, errors.New("archive raw event is empty"))
	}
	batch, decision, decodeErr := h.decoder.DecodeEvent(delivery.RawEnvelope(), delivery.Subject(), delivery.MessageID())
	if decision == DecisionIgnore {
		return jetstream.HandlerResult{Decision: jetstream.ACK}
	}
	if decision == DecisionReject || decodeErr != nil {
		if decodeErr == nil {
			decodeErr = errors.New("archive event rejected")
		}
		return h.reject(ctx, delivery, decodeErr)
	}
	result, err := h.journal.Append(ctx, batch)
	if err != nil {
		delay := retryDelay(delivery.DeliveryCount())
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: delay, Err: err}
	}
	if !result.Duplicate && h.notifier != nil {
		h.notifier.Notify(result.Partitions)
	}
	return jetstream.HandlerResult{Decision: jetstream.ACK}
}

func (h *Handler) reject(ctx context.Context, delivery Delivery, reason error) jetstream.HandlerResult {
	err := h.journal.Quarantine(ctx, journal.QuarantineRecord{MessageID: delivery.MessageID(), Subject: delivery.Subject(), StreamSeq: delivery.StreamSequence(), Delivery: delivery.DeliveryCount(), Reason: reason.Error(), RawEnvelope: delivery.RawEnvelope()})
	if err != nil {
		delay := retryDelay(delivery.DeliveryCount())
		return jetstream.HandlerResult{Decision: jetstream.RETRY, Delay: delay, Err: err}
	}
	return jetstream.HandlerResult{Decision: jetstream.TERM}
}

func retryDelay(deliveries uint64) time.Duration {
	if deliveries == 0 {
		deliveries = 1
	}
	shift := deliveries - 1
	if shift > 5 {
		shift = 5
	}
	delay := time.Second * time.Duration(1<<shift)
	return time.Duration(math.Min(float64(delay), float64(30*time.Second)))
}
