package msgbox

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWeComSenderSendSuccess(t *testing.T) {
	var gotContentType string
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	t.Cleanup(server.Close)

	sender, err := NewWeComSender(server.URL, WithTestHTTPAllowed())
	if err != nil {
		t.Fatal(err)
	}
	err = sender.Send(context.Background(), Message{
		Key:      "collector-watermark",
		Severity: SeverityWarning,
		Title:    "Collector delayed",
		Body:     "BTC-USDT 1m is stale",
		Labels:   map[string]string{"module": "collector"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotContentType != "application/json" {
		t.Fatalf("Content-Type = %q", gotContentType)
	}
	for _, want := range []string{"Collector delayed", "BTC-USDT 1m is stale", "collector-watermark", "module", "collector"} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("request body %q does not contain %q", gotBody, want)
		}
	}
}

func TestWeComSenderRejectsNon2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal details", http.StatusBadGateway)
	}))
	t.Cleanup(server.Close)

	sender, err := NewWeComSender(server.URL, WithTestHTTPAllowed())
	if err != nil {
		t.Fatal(err)
	}
	err = sender.Send(context.Background(), validMessage())
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("Send() error = %v", err)
	}
}

func TestWeComSenderRejectsBusinessError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"errcode":93000,"errmsg":"invalid webhook"}`))
	}))
	t.Cleanup(server.Close)

	sender, err := NewWeComSender(server.URL, WithTestHTTPAllowed())
	if err != nil {
		t.Fatal(err)
	}
	err = sender.Send(context.Background(), validMessage())
	if err == nil || !strings.Contains(err.Error(), "errcode 93000") {
		t.Fatalf("Send() error = %v", err)
	}
}

func TestWeComSenderTimesOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	t.Cleanup(server.Close)

	sender, err := NewWeComSender(server.URL, WithTestHTTPAllowed(), WithTimeout(20*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	err = sender.Send(context.Background(), validMessage())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Send() error = %v, want context deadline exceeded", err)
	}
}

func TestWeComSenderLimitsResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxResponseBytes+1)))
	}))
	t.Cleanup(server.Close)

	sender, err := NewWeComSender(server.URL, WithTestHTTPAllowed())
	if err != nil {
		t.Fatal(err)
	}
	err = sender.Send(context.Background(), validMessage())
	if err == nil || !strings.Contains(err.Error(), "response body exceeds") {
		t.Fatalf("Send() error = %v", err)
	}
}

func TestWeComSenderValidatesMessageLimits(t *testing.T) {
	sender, err := NewWeComSender("http://example.test", WithTestHTTPAllowed())
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		message Message
	}{
		{name: "body", message: Message{Severity: SeverityInfo, Body: strings.Repeat("界", maxBodyCharacters+1)}},
		{name: "label count", message: Message{Severity: SeverityInfo, Labels: labels(17)}},
		{name: "label key", message: Message{Severity: SeverityInfo, Labels: map[string]string{strings.Repeat("k", maxLabelKeyCharacters+1): "value"}}},
		{name: "label value", message: Message{Severity: SeverityInfo, Labels: map[string]string{"key": strings.Repeat("值", maxLabelValueCharacters+1)}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := sender.Send(context.Background(), tt.message); err == nil {
				t.Fatal("Send() error = nil")
			}
		})
	}
}

func TestNewWeComSenderRequiresHTTPS(t *testing.T) {
	if _, err := NewWeComSender("http://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=secret"); err == nil {
		t.Fatal("NewWeComSender() error = nil")
	}
	if _, err := NewWeComSender("://not-a-url"); err == nil {
		t.Fatal("NewWeComSender() malformed URL error = nil")
	}
}

func TestWeComSenderErrorsDoNotLeakSecrets(t *testing.T) {
	const secret = "super-secret-webhook-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "response-secret-value", http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	sender, err := NewWeComSender(server.URL+"?key="+secret, WithTestHTTPAllowed())
	if err != nil {
		t.Fatal(err)
	}
	err = sender.Send(context.Background(), validMessage())
	if err == nil {
		t.Fatal("Send() error = nil")
	}
	for _, forbidden := range []string{secret, "response-secret-value", server.URL} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("Send() error %q leaks %q", err, forbidden)
		}
	}
}

func validMessage() Message {
	return Message{
		Key:      "monitor-ready",
		Severity: SeverityCritical,
		Title:    "Monitor unavailable",
		Body:     "The monitor readiness endpoint is unavailable.",
	}
}

func labels(count int) map[string]string {
	out := make(map[string]string, count)
	for i := 0; i < count; i++ {
		out[strings.Repeat("k", i+1)] = "value"
	}
	return out
}
