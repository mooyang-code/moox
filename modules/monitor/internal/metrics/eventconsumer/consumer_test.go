package eventconsumer

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/packages/jetstream"
)

func TestHandleDeliveryAppliesTermAndReturnsBusinessError(t *testing.T) {
	err := (&Consumer{}).HandleDelivery(context.Background(), nil)
	if err == nil || !errors.Is(err, jetstream.ErrInvalidDelivery) || err.Error() == "empty metric delivery" {
		t.Fatalf("HandleDelivery() error = %v, want business and transport errors", err)
	}
}

func TestRunWhenReadyRequiresStorage(t *testing.T) {
	if err := RunWhenReady(context.Background(), ConsumerOptions{}); err == nil {
		t.Fatal("expected missing storage error")
	}
}
