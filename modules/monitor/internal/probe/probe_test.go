package probe

import (
	"context"
	"fmt"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPProbeSignsSysDeployHealthPathsWithFreshNonce(t *testing.T) {
	authenticator, err := healthz.NewAuthenticator(healthz.AuthConfig{Version: "moox-health-v1", AccessKey: "monitor", SecretKey: "secret"})
	require.NoError(t, err)
	var headers []string
	handler := authenticator.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = append(headers, r.Header.Get("X-Moox-Health-Auth"))
		require.Equal(t, "keep", r.Header.Get("X-Custom"))
		w.WriteHeader(http.StatusOK)
	}))
	srv := httptest.NewServer(handler)
	defer srv.Close()
	runner := HTTPRunner{HealthSigner: &HealthSigner{Version: "moox-health-v1", AccessKey: "monitor", SecretKey: "secret"}}

	for i := 0; i < 2; i++ {
		result := runner.Run(context.Background(), domain.Check{Kind: domain.CheckKindHTTP, Source: domain.CheckSourceSysDeploy, URL: srv.URL + "/readyz?full=1", Headers: `{"X-Custom":"keep"}`})
		require.True(t, result.Success, result.ErrorMessage)
	}
	require.Len(t, headers, 2)
	require.NotEqual(t, headers[0], headers[1])
}

func TestHTTPProbeLeavesManualAndNonHealthChecksUnsigned(t *testing.T) {
	var headers []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = append(headers, r.Header.Get("X-Moox-Health-Auth"))
	}))
	defer srv.Close()
	runner := HTTPRunner{HealthSigner: &HealthSigner{Version: "moox-health-v1", AccessKey: "monitor", SecretKey: "secret"}}

	runner.Run(context.Background(), domain.Check{Kind: domain.CheckKindHTTP, Source: domain.CheckSourceObservability, URL: srv.URL + "/readyz"})
	runner.Run(context.Background(), domain.Check{Kind: domain.CheckKindHTTP, Source: domain.CheckSourceSysDeploy, URL: srv.URL + "/status"})
	require.Equal(t, []string{"", ""}, headers)
}

func TestHTTPProbe(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Fatalf("method = %s", r.Method)
			}
			if r.Header.Get("X-Test") != "probe" {
				t.Fatalf("header X-Test = %q", r.Header.Get("X-Test"))
			}
			body, _ := io.ReadAll(r.Body)
			if string(body) != "ping" {
				t.Fatalf("body = %q", string(body))
			}
			_, _ = w.Write([]byte("service ready"))
		}))
		defer srv.Close()

		result := HTTPRunner{}.Run(context.Background(), domain.Check{
			SpaceID:        "space-a",
			CheckID:        "http-ok",
			Kind:           domain.CheckKindHTTP,
			URL:            srv.URL,
			Method:         http.MethodPost,
			Headers:        `{"X-Test":"probe"}`,
			Body:           "ping",
			TimeoutMS:      1000,
			ExpectedStatus: "200-299",
			BodyContains:   "ready",
		})
		if !result.Success || result.HTTPStatus != http.StatusOK || result.Status != domain.CheckStatusOK {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("status mismatch", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		result := HTTPRunner{}.Run(context.Background(), httpCheck(srv.URL))
		if result.Success || result.HTTPStatus != http.StatusInternalServerError {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("max latency", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(25 * time.Millisecond)
		}))
		defer srv.Close()

		check := httpCheck(srv.URL)
		check.MaxResponseMS = 1
		result := HTTPRunner{}.Run(context.Background(), check)
		if result.Success || result.ErrorMessage == "" {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("body contains missing", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("not ready"))
		}))
		defer srv.Close()

		check := httpCheck(srv.URL)
		check.BodyContains = "definitely-ready"
		result := HTTPRunner{}.Run(context.Background(), check)
		if result.Success || result.ErrorMessage == "" {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(100 * time.Millisecond)
		}))
		defer srv.Close()

		check := httpCheck(srv.URL)
		check.TimeoutMS = 10
		result := HTTPRunner{}.Run(context.Background(), check)
		if result.Success || result.ErrorMessage == "" {
			t.Fatalf("result = %+v", result)
		}
	})
}

func TestTCPProbe(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	host, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}
	var tcpPort int
	if _, err := fmt.Sscanf(port, "%d", &tcpPort); err != nil {
		t.Fatalf("parse port: %v", err)
	}

	result := TCPRunner{}.Run(context.Background(), domain.Check{
		SpaceID:   "space-a",
		CheckID:   "tcp-ok",
		Kind:      domain.CheckKindTCP,
		TCPHost:   host,
		TCPPort:   tcpPort,
		TimeoutMS: 1000,
	})
	if !result.Success || !result.Connected || result.Status != domain.CheckStatusOK {
		t.Fatalf("result = %+v", result)
	}

	refused := TCPRunner{}.Run(context.Background(), domain.Check{
		SpaceID:   "space-a",
		CheckID:   "tcp-bad",
		Kind:      domain.CheckKindTCP,
		TCPHost:   "127.0.0.1",
		TCPPort:   1,
		TimeoutMS: 50,
	})
	if refused.Success || refused.ErrorMessage == "" {
		t.Fatalf("refused = %+v", refused)
	}

	threshold := TCPRunner{}.Run(context.Background(), domain.Check{
		SpaceID:       "space-a",
		CheckID:       "tcp-slow",
		Kind:          domain.CheckKindTCP,
		TCPHost:       host,
		TCPPort:       tcpPort,
		TimeoutMS:     1000,
		MaxResponseMS: 1,
	})
	if threshold.Success && threshold.LatencyMS > 1 {
		t.Fatalf("threshold = %+v", threshold)
	}
}

func TestParseStatusExpectation(t *testing.T) {
	for _, tt := range []struct {
		raw  string
		code int
		want bool
	}{
		{raw: "200", code: 200, want: true},
		{raw: "200-299", code: 204, want: true},
		{raw: "200,204", code: 204, want: true},
		{raw: "200,204", code: 500, want: false},
	} {
		match, err := ParseStatusExpectation(tt.raw)
		if err != nil {
			t.Fatalf("ParseStatusExpectation(%q): %v", tt.raw, err)
		}
		if got := match(tt.code); got != tt.want {
			t.Fatalf("match(%q,%d)=%v want %v", tt.raw, tt.code, got, tt.want)
		}
	}
}

func httpCheck(url string) domain.Check {
	return domain.Check{
		SpaceID:        "space-a",
		CheckID:        "http-check",
		Kind:           domain.CheckKindHTTP,
		URL:            url,
		Method:         http.MethodGet,
		TimeoutMS:      1000,
		ExpectedStatus: "200-299",
	}
}

func TestMultiRunnerUnsupportedKind(t *testing.T) {
	r := DefaultRunner()
	got := r.Run(context.Background(), domain.Check{Kind: "udp", CheckID: "c1", SpaceID: "s1"})
	assert.False(t, got.Success)
	assert.Equal(t, domain.CheckStatusDown, got.Status)
	assert.Contains(t, got.ErrorMessage, "unsupported check kind")
	assert.NotEmpty(t, got.ResultID)
}

func TestCheckTimeoutDefaults(t *testing.T) {
	assert.Equal(t, 3*time.Second, checkTimeout(domain.Check{}))
	assert.Equal(t, 1500*time.Millisecond, checkTimeout(domain.Check{TimeoutMS: 1500}))
}

func TestFailResult(t *testing.T) {
	got := failResult(domain.Check{CheckID: "c", SpaceID: "s"}, 12*time.Millisecond, "boom")
	assert.False(t, got.Success)
	assert.Equal(t, "boom", got.ErrorMessage)
	assert.Equal(t, int64(12), got.LatencyMS)
	require.NotEmpty(t, got.ResultID)
}
