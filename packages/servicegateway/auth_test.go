package servicegateway

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestBuildAndVerifyHeader(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cfg := AuthConfig{AccessKey: "service", SecretKey: "secret", ExpireSeconds: 60}
	req := AuthRequest{Method: "POST", Path: "/api/service/test/Call", Body: []byte(`{"ok":true}`)}

	header, err := BuildHeader(cfg, req, now)
	if err != nil {
		t.Fatalf("BuildHeader() error = %v", err)
	}
	if _, err := VerifyHeader(cfg, req, header, now); err != nil {
		t.Fatalf("VerifyHeader() error = %v", err)
	}
}

func TestBuildAndVerifyHeaderBindsRequestAndUses32ByteNonce(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	header, err := BuildHeader(AuthConfig{AccessKey: "ak", SecretKey: "sk", ExpireSeconds: 60}, AuthRequest{Method: http.MethodPost, Path: "/api/service/x/Do", Body: []byte(`{"x":1}`)}, now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(header, "/")
	if len(parts) != 6 || len(parts[4]) != 64 {
		t.Fatalf("header = %q", header)
	}
	if _, err := VerifyHeader(AuthConfig{AccessKey: "ak", SecretKey: "sk", ExpireSeconds: 60}, AuthRequest{Method: http.MethodPost, Path: "/api/service/x/Do", Body: []byte(`{"x":1}`)}, header, now); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyHeader(AuthConfig{AccessKey: "ak", SecretKey: "sk", ExpireSeconds: 60}, AuthRequest{Method: http.MethodGet, Path: "/api/service/x/Do", Body: []byte(`{"x":1}`)}, header, now); err == nil {
		t.Fatal("changed method accepted")
	}
}

func TestVerifyHeaderRejectsTamperedExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	config := AuthConfig{AccessKey: "ak", SecretKey: "sk", ExpireSeconds: 60}
	request := AuthRequest{Method: http.MethodPost, Path: "/api/service/x/Do", Body: []byte(`{"x":1}`)}
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

func TestVerifyHeaderRejectsTamperedRoutingHeaders(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	config := AuthConfig{AccessKey: "ak", SecretKey: "sk", ExpireSeconds: 60}
	request := AuthRequest{
		Method: http.MethodPost,
		Path:   "/api/service/x/Do",
		Body:   []byte(`{"x":1}`),
		Headers: map[string]string{
			"X-Space-Id": "space-1",
			"X-App-Id":   "app-1",
			"X-App-Key":  "key-1",
		},
	}
	header, err := BuildHeader(config, request, now)
	if err != nil {
		t.Fatal(err)
	}
	request.Headers["X-Space-Id"] = "space-2"
	if _, err := VerifyHeader(config, request, header, now); err == nil {
		t.Fatal("tampered routing header accepted")
	}
}
