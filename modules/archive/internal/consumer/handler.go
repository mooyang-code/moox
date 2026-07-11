package consumer

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	"github.com/mooyang-code/moox/modules/archive/internal/journal"
	"github.com/mooyang-code/moox/packages/messagepb"
)

type Delivery interface {
	Envelope() *messagepb.MooxMessage
	RawEnvelope() []byte
	MessageID() string
	Subject() string
	StreamSequence() uint64
	DeliveryCount() uint64
	DecodeError() error
	Ack(context.Context) error
	Nak(context.Context, time.Duration) error
	InProgress(context.Context) error
	Term(context.Context) error
}

type DecoderAPI interface {
	Decode(*messagepb.MooxMessage) (domain.EventBatch, Decision, error)
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
	if delivery == nil {
		return fmt.Errorf("delivery is nil")
	}
	if decodeErr := delivery.DecodeError(); decodeErr != nil {
		return h.reject(ctx, delivery, decodeErr)
	}
	batch, decision, decodeErr := h.decoder.Decode(delivery.Envelope())
	if decision == DecisionIgnore {
		return delivery.Ack(ctx)
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
		if nakErr := delivery.Nak(ctx, delay); nakErr != nil {
			return nakErr
		}
		return &RetryScheduledError{Delay: delay}
	}
	if !result.Duplicate && h.notifier != nil {
		h.notifier.Notify(result.Partitions)
	}
	if err := delivery.Ack(ctx); err != nil {
		return err
	}
	return nil
}

func (h *Handler) reject(ctx context.Context, delivery Delivery, reason error) error {
	err := h.journal.Quarantine(ctx, journal.QuarantineRecord{MessageID: delivery.MessageID(), Subject: delivery.Subject(), StreamSeq: delivery.StreamSequence(), Delivery: delivery.DeliveryCount(), Reason: reason.Error(), RawEnvelope: delivery.RawEnvelope()})
	if err != nil {
		delay := retryDelay(delivery.DeliveryCount())
		_ = delivery.Nak(ctx, delay)
		return &RetryScheduledError{Delay: delay}
	}
	return delivery.Term(ctx)
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
