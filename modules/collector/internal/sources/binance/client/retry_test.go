package binance

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
)

func TestRetryBinanceAttemptsTransientFailureThreeTimes(t *testing.T) {
	calls := 0
	err := retryBinance(context.Background(), func() error {
		calls++
		return &httpclient.StatusError{StatusCode: 503}
	})
	if err == nil {
		t.Fatal("retryBinance() returned nil")
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestRetryBinanceDoesNotRetryDecodeFailure(t *testing.T) {
	calls := 0
	err := retryBinance(context.Background(), func() error {
		calls++
		return errors.New("decode Binance JSON")
	})
	if err == nil {
		t.Fatal("retryBinance() returned nil")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestRetryBinanceDoesNotRetryPermanentResponse(t *testing.T) {
	calls := 0
	err := retryBinance(context.Background(), func() error {
		calls++
		return &httpclient.StatusError{StatusCode: 400}
	})
	if err == nil {
		t.Fatal("retryBinance() returned nil")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}
