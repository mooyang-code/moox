package serviceauth

import (
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBuildAndVerifyHeaderBindsRequestAndUses32ByteNonce(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	header, err := BuildHeader(Config{AccessKey: "ak", SecretKey: "sk", ExpireSeconds: 60}, Request{Method: http.MethodPost, Path: "/api/service/x/Do", Body: []byte(`{"x":1}`)}, now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(header, "/")
	if len(parts) != 6 || len(parts[4]) != 64 {
		t.Fatalf("header = %q", header)
	}
	if _, err := VerifyHeader(Config{AccessKey: "ak", SecretKey: "sk", ExpireSeconds: 60}, Request{Method: http.MethodPost, Path: "/api/service/x/Do", Body: []byte(`{"x":1}`)}, header, now); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyHeader(Config{AccessKey: "ak", SecretKey: "sk", ExpireSeconds: 60}, Request{Method: http.MethodGet, Path: "/api/service/x/Do", Body: []byte(`{"x":1}`)}, header, now); err == nil {
		t.Fatal("changed method accepted")
	}
}

func TestVerifyHeaderRejectsTamperedExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	config := Config{AccessKey: "ak", SecretKey: "sk", ExpireSeconds: 60}
	request := Request{Method: http.MethodPost, Path: "/api/service/x/Do", Body: []byte(`{"x":1}`)}
	header, err := BuildHeader(config, request, now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(header, "/")
	parts[3] = "120"
	if _, err := VerifyHeader(config, request, strings.Join(parts, "/"), now); err == nil {
		t.Fatal("tampered expiry accepted")
	}
}

func TestNonceCacheConsumesOnceAtomicallyAndRemainsBounded(t *testing.T) {
	cache := NewNonceCache(8)
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if cache.Consume("ak", strings.Repeat("a", 64), time.Minute, time.Now()) {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("accepted = %d", accepted.Load())
	}
	for i := 0; i < 32; i++ {
		cache.Consume("ak", strings.Repeat(string(rune('b'+i)), 64), time.Minute, time.Now())
	}
	if cache.Len() > 8 {
		t.Fatalf("cache len = %d", cache.Len())
	}
}

func TestNonceCacheFailsClosedWhenFullWithoutEvictingLiveNonce(t *testing.T) {
	cache := NewNonceCache(1)
	now := time.Unix(1_700_000_000, 0)
	first := strings.Repeat("a", 64)
	if !cache.Consume("ak", first, time.Minute, now) {
		t.Fatal("first nonce rejected")
	}
	if cache.Consume("ak", strings.Repeat("b", 64), time.Minute, now) {
		t.Fatal("full cache accepted a new nonce")
	}
	if cache.Consume("ak", first, time.Minute, now) {
		t.Fatal("first nonce became replayable after cache filled")
	}
	if !cache.Consume("ak", strings.Repeat("b", 64), time.Minute, now.Add(time.Minute)) {
		t.Fatal("expired entry was not reclaimed")
	}
}
