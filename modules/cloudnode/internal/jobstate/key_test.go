package jobstate

import (
	"strings"
	"testing"
)

func TestJobKeyEncodesTrimmedSegments(t *testing.T) {
	key := JobKey("crypto", "collector:kline:BTC/USDT:1m")
	if !strings.HasPrefix(key, "job.") || strings.Contains(key, "BTC/USDT") {
		t.Fatalf("key = %q, want job prefix", key)
	}
	if key != JobKey(" crypto ", " collector:kline:BTC/USDT:1m ") {
		t.Fatalf("key does not normalize surrounding whitespace: %q", key)
	}
}
