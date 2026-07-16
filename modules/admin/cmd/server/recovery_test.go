package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/filter"
	trpchttp "trpc.group/trpc-go/trpc-go/http"
)

func TestRecoveryFilterConvertsPanicAndKeepsServing(t *testing.T) {
	recovery := filter.GetServer("recovery")
	if recovery == nil {
		t.Fatal("recovery filter is not registered")
	}

	_, err := recovery(context.Background(), nil, func(context.Context, interface{}) (interface{}, error) {
		panic("test panic")
	})
	if err == nil {
		t.Fatal("panic must be converted to a server error")
	}
	if err.Error() == "test panic" {
		t.Fatalf("panic details leaked in error: %v", err)
	}

	want := "still-serving"
	got, err := recovery(context.Background(), nil, func(context.Context, interface{}) (interface{}, error) {
		return want, nil
	})
	if err != nil {
		t.Fatalf("normal request returned error: %v", err)
	}
	if got != want {
		t.Fatalf("response = %v, want %q", got, want)
	}
}

func TestRecoveryFilterProtectsHTTPBoundary(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()

	configPath := filepath.Join(t.TempDir(), "trpc_go.yaml")
	config := fmt.Sprintf(`global:
  namespace: Development
  env_name: test
server:
  filter:
    - recovery
  service:
    - name: trpc.moox.admin.RecoveryBoundary
      ip: 127.0.0.1
      port: %d
      network: tcp
      protocol: http_no_protocol
`, port)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := trpc.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	server := trpc.NewServerWithConfig(cfg)
	mux := http.NewServeMux()
	mux.HandleFunc("/panic", func(http.ResponseWriter, *http.Request) { panic("recovery-boundary-secret") })
	mux.HandleFunc("/ok", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("still-serving")) })
	trpchttp.RegisterNoProtocolServiceMux(server.Service("trpc.moox.admin.RecoveryBoundary"), mux)
	go func() { _ = server.Serve() }()
	t.Cleanup(func() { _ = server.Close(nil) })

	panicStatus, panicBody := waitForRecoveryResponse(t, fmt.Sprintf("http://127.0.0.1:%d/panic", port))
	if panicStatus < http.StatusInternalServerError {
		t.Fatalf("panic status = %d, want server error", panicStatus)
	}
	if strings.Contains(panicBody, "recovery-boundary-secret") || strings.Contains(panicBody, "goroutine") {
		t.Fatalf("panic details leaked in response: %q", panicBody)
	}
	okStatus, okBody := waitForRecoveryResponse(t, fmt.Sprintf("http://127.0.0.1:%d/ok", port))
	if okStatus != http.StatusOK || okBody != "still-serving" {
		t.Fatalf("normal response = %d %q", okStatus, okBody)
	}
}

func waitForRecoveryResponse(t *testing.T, url string) (int, string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := http.Get(url)
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			return resp.StatusCode, string(body)
		}
		if time.Now().After(deadline) {
			t.Fatalf("request %s: %v", url, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
