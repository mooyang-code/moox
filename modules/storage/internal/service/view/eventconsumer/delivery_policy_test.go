package eventconsumer

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/packages/jetstream"
)

func TestProcessDeliveryUsesClientRetryCountWhenDeliveryCountDoesNotChange(t *testing.T) {
	consumer := &Consumer{config: Config{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var applies, progress, terms int
	delivery := &jetstream.Delivery{
		Subject:       "same",
		DeliveryCount: 1,
	}
	deliveryProgress := func(context.Context) error { progress++; return nil }
	deliveryTerm := func(context.Context) error { terms++; cancel(); return nil }
	// Keep the callbacks on the delivery rather than changing DeliveryCount;
	// this models client-side InProgress calls renewing one broker delivery.
	err := consumer.processDeliveryWithApplyAndActions(ctx, delivery, nil, 3, func(context.Context, *jetstream.Delivery) error {
		applies++
		return errors.New("temporary event apply failure")
	}, deliveryActions{
		ack:      func(context.Context) error { return errors.New("unexpected ack") },
		progress: deliveryProgress,
		term:     deliveryTerm,
	})
	if err == nil {
		t.Fatal("processDeliveryWithApply() error = nil, want retry exhaustion")
	}
	if applies != 3 || terms != 1 || progress != 2 {
		t.Fatalf("applies=%d terms=%d progress=%d, want applies=3 terms=1 progress=2", applies, terms, progress)
	}
}

func TestProcessDeliveryTermsAfterRetryExhaustion(t *testing.T) {
	consumer := &Consumer{config: Config{}}
	var applies, terms int
	delivery := &jetstream.Delivery{Subject: "same", DeliveryCount: 1}
	err := consumer.processDeliveryWithApplyAndActions(context.Background(), delivery, nil, 1, func(context.Context, *jetstream.Delivery) error {
		applies++
		return errors.New("temporary event apply failure")
	}, deliveryActions{
		ack:      func(context.Context) error { return errors.New("unexpected ack") },
		progress: func(context.Context) error { return nil },
		term:     func(context.Context) error { terms++; return nil },
	})
	if err == nil {
		t.Fatal("processDeliveryWithApplyAndActions() error = nil, want retry exhaustion")
	}
	if applies != 1 || terms != 1 {
		t.Fatalf("applies=%d terms=%d, want 1/1", applies, terms)
	}
}

func TestProcessDeliveryKeepsPermanentEventPendingByDefault(t *testing.T) {
	consumer := &Consumer{config: Config{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var terms, progress int
	delivery := &jetstream.Delivery{Subject: "poison", DeliveryCount: 1}
	err := consumer.processDeliveryWithApplyAndActions(ctx, delivery, nil, -1, func(context.Context, *jetstream.Delivery) error {
		return Permanent(errors.New("invalid event"))
	}, deliveryActions{
		ack:      func(context.Context) error { return errors.New("unexpected ack") },
		progress: func(context.Context) error { progress++; cancel(); return nil },
		term:     func(context.Context) error { terms++; return nil },
	})
	if err == nil {
		t.Fatal("poison delivery returned nil")
	}
	if terms != 0 || progress != 1 {
		t.Fatalf("terms=%d progress=%d, want 0/1", terms, progress)
	}
}
