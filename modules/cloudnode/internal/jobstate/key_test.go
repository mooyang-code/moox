package jobstate

import (
	"github.com/stretchr/testify/assert"
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

func TestParseJobKeyRejectsMalformedKeys(t *testing.T) {
	for _, key := range []string{
		"",
		"job.only-two-parts",
		"task.Y3J5cHRv.aXRlbQ",
		"job.@@@.aXRlbQ",
		"job.Y3J5cHRv.@@@",
	} {
		spaceID, jobItemID, ok := ParseJobKey(key)
		assert.False(t, ok, key)
		assert.Empty(t, spaceID)
		assert.Empty(t, jobItemID)
	}
}

func TestJobKeyTrimsSegments(t *testing.T) {
	key := JobKey(" crypto ", " item-1 ")
	spaceID, jobItemID, ok := ParseJobKey(key)
	assert.True(t, ok)
	assert.Equal(t, "crypto", spaceID)
	assert.Equal(t, "item-1", jobItemID)
}
