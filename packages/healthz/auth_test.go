package healthz

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/requestauth"
)

func TestAuthenticatorRequiresValidFreshUniqueSignature(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a, err := NewAuthenticator(AuthConfig{Version: "moox-health-v1", AccessKey: "monitor", SecretKey: "secret", ClockSkew: time.Minute, NonceTTL: 2 * time.Minute, MaxNonces: 16})
	if err != nil {
		t.Fatal(err)
	}
	a.now = func() time.Time { return now }
	var called atomic.Int32
	handler := a.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called.Add(1); w.WriteHeader(http.StatusNoContent) }))

	nonce := strings.Repeat("a", 64)
	req := signedHealthRequest(t, http.MethodGet, "/readyz", now.Unix(), nonce, "monitor", "secret")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent || called.Load() != 1 {
		t.Fatalf("valid request = %d called=%d", rr.Code, called.Load())
	}

	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, signedHealthRequest(t, http.MethodGet, "/readyz", now.Unix(), nonce, "monitor", "secret"))
	if rr.Code != http.StatusUnauthorized || called.Load() != 1 {
		t.Fatalf("replay = %d called=%d", rr.Code, called.Load())
	}
}

func TestAuthenticatorRejectsInvalidRequests(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a, err := NewAuthenticator(AuthConfig{Version: "moox-health-v1", AccessKey: "monitor", SecretKey: "secret", ClockSkew: time.Minute, NonceTTL: 2 * time.Minute, MaxNonces: 16})
	if err != nil {
		t.Fatal(err)
	}
	a.now = func() time.Time { return now }
	handler := a.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	tests := []struct {
		name string
		req  *http.Request
	}{
		{name: "missing", req: httptest.NewRequest(http.MethodGet, "/healthz", nil)},
		{name: "expired", req: signedHealthRequest(t, http.MethodGet, "/healthz", now.Add(-2*time.Minute).Unix(), strings.Repeat("b", 64), "monitor", "secret")},
		{name: "future", req: signedHealthRequest(t, http.MethodGet, "/healthz", now.Add(2*time.Minute).Unix(), strings.Repeat("c", 64), "monitor", "secret")},
		{name: "wrong access key", req: signedHealthRequest(t, http.MethodGet, "/healthz", now.Unix(), strings.Repeat("d", 64), "other", "secret")},
		{name: "wrong path", req: func() *http.Request {
			r := signedHealthRequest(t, http.MethodGet, "/readyz", now.Unix(), strings.Repeat("e", 64), "monitor", "secret")
			r.URL.Path = "/healthz"
			return r
		}()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, tt.req)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d", rr.Code)
			}
		})
	}
}

func TestAuthenticatorConsumesConcurrentNonceOnce(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a, err := NewAuthenticator(AuthConfig{Version: "moox-health-v1", AccessKey: "monitor", SecretKey: "secret", ClockSkew: time.Minute, NonceTTL: 2 * time.Minute, MaxNonces: 16})
	if err != nil {
		t.Fatal(err)
	}
	a.now = func() time.Time { return now }
	handler := a.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	var success atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, signedHealthRequest(t, http.MethodGet, "/healthz", now.Unix(), strings.Repeat("f", 64), "monitor", "secret"))
			if rr.Code == http.StatusNoContent {
				success.Add(1)
			}
		}()
	}
	wg.Wait()
	if success.Load() != 1 {
		t.Fatalf("success = %d", success.Load())
	}
}

func TestAuthenticatorFailsClosedAtNonceCapacity(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a, err := NewAuthenticator(AuthConfig{Version: "moox-health-v1", AccessKey: "monitor", SecretKey: "secret", ClockSkew: time.Minute, NonceTTL: 2 * time.Minute, MaxNonces: 1})
	if err != nil {
		t.Fatal(err)
	}
	a.now = func() time.Time { return now }
	handler := a.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for i, nonce := range []string{strings.Repeat("1", 64), strings.Repeat("2", 64)} {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, signedHealthRequest(t, http.MethodGet, "/healthz", now.Unix(), nonce, "monitor", "secret"))
		want := http.StatusNoContent
		if i == 1 {
			want = http.StatusServiceUnavailable
		}
		if rr.Code != want {
			t.Fatalf("request %d status=%d want=%d", i, rr.Code, want)
		}
	}
}

func signedHealthRequest(t *testing.T, method, path string, timestamp int64, nonce, accessKey, secret string) *http.Request {
	t.Helper()
	signature, err := requestauth.Sign(secret, requestauth.Material{Method: method, Path: path, Timestamp: timestamp, Nonce: nonce})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-Moox-Health-Auth", "moox-health-v1/"+accessKey+"/"+strconv.FormatInt(timestamp, 10)+"/"+nonce+"/"+signature)
	return req
}
