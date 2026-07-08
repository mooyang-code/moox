package probe

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
)

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
