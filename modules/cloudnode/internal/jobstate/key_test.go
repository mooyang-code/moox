package jobstate

import (
	"strings"
	"testing"
)

func TestJobKeyRoundTrip(t *testing.T) {
	key := JobKey("crypto", "collector:kline:BTC/USDT:1m")
	if !strings.HasPrefix(key, "job.") {
		t.Fatalf("key = %q, want job prefix", key)
	}
	spaceID, jobItemID, ok := ParseJobKey(key)
	if !ok {
		t.Fatalf("ParseJobKey(%q) failed", key)
	}
	if spaceID != "crypto" || jobItemID != "collector:kline:BTC/USDT:1m" {
		t.Fatalf("decoded = %q %q", spaceID, jobItemID)
	}
}

func TestSpacePrefix(t *testing.T) {
	prefix := SpacePrefix("crypto")
	if !strings.HasPrefix(prefix, "job.") || !strings.HasSuffix(prefix, ".") {
		t.Fatalf("prefix = %q", prefix)
	}
}
