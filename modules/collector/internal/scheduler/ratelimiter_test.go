package scheduler

import (
	"testing"
	"time"
)

func TestLimiter_Allow(t *testing.T) {
	limiter := NewLimiter(1, time.Minute)
	if !limiter.Allow("BINANCE") {
		t.Fatal("first Allow() = false, want true")
	}
	if limiter.Allow("BINANCE") {
		t.Fatal("second Allow() = true, want false")
	}
}
