package doctor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPProberRejectsPathsAndOversizedResponses(t *testing.T) {
	prober := HTTPProber{Auth: HealthAuth{AccessKey: "monitor", SecretKey: "secret"}}
	_, err := prober.Get(context.Background(), "http://localhost/admin")
	require.ErrorContains(t, err, "not allowed")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxProbeBytes+1)))
	}))
	defer server.Close()
	_, err = prober.Get(context.Background(), server.URL+"/metrics")
	require.ErrorContains(t, err, "exceeds")
}

func TestHTTPProberRejectsRedirectsAndNonLocalHosts(t *testing.T) {
	prober := HTTPProber{Auth: HealthAuth{AccessKey: "monitor", SecretKey: "secret"}}
	_, err := prober.Get(context.Background(), "http://example.com/healthz")
	require.ErrorContains(t, err, "host")

	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetCalls++
		_, _ = w.Write([]byte("ok"))
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/healthz", http.StatusFound)
	}))
	defer redirect.Close()
	_, err = prober.Get(context.Background(), redirect.URL+"/healthz")
	require.ErrorContains(t, err, "redirect")
	require.Zero(t, targetCalls)
}
