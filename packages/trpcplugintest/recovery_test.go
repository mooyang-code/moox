package trpcplugintest

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/filter"
	trpchttp "trpc.group/trpc-go/trpc-go/http"
)

func TestRecoveryFilterConvertsPanicsWithoutLeakingDetails(t *testing.T) {
	if filter.GetServer("recovery") == nil {
		t.Fatal("MooX recovery filter is not registered")
	}

	port := freePort(t)
	configPath := filepath.Join(t.TempDir(), "trpc_go.yaml")
	config := fmt.Sprintf(`global:
  namespace: Development
  env_name: test
server:
  filter:
    - recovery
  service:
    - name: trpc.moox.test.Recovery
      ip: 127.0.0.1
      port: %d
      network: tcp
      protocol: http_no_protocol
`, port)
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatalf("write tRPC config: %v", err)
	}
	cfg, err := trpc.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("load tRPC config: %v", err)
	}
	server := trpc.NewServerWithConfig(cfg)
	mux := http.NewServeMux()
	mux.HandleFunc("/panic", func(http.ResponseWriter, *http.Request) { panic("recovery-test") })
	mux.HandleFunc("/ok", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("still-serving")) })
	trpchttp.RegisterNoProtocolServiceMux(server.Service("trpc.moox.test.Recovery"), mux)
	go func() { _ = server.Serve() }()
	t.Cleanup(func() { _ = server.Close(nil) })

	panicResponse := waitForResponse(t, fmt.Sprintf("http://127.0.0.1:%d/panic", port))
	if panicResponse.StatusCode < http.StatusInternalServerError {
		t.Fatalf("panic status = %d, want controlled server error", panicResponse.StatusCode)
	}
	if strings.Contains(panicResponse.Body, "recovery-test") || strings.Contains(panicResponse.Body, "goroutine") {
		t.Fatalf("panic details leaked in response: %q", panicResponse.Body)
	}

	okResponse := waitForResponse(t, fmt.Sprintf("http://127.0.0.1:%d/ok", port))
	if okResponse.StatusCode != http.StatusOK || okResponse.Body != "still-serving" {
		t.Fatalf("normal response = %d %q, want 200 still-serving", okResponse.StatusCode, okResponse.Body)
	}
}

type response struct {
	StatusCode int
	Body       string
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func waitForResponse(t *testing.T, url string) response {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := http.Get(url)
		if err == nil {
			defer resp.Body.Close()
			body, readErr := io.ReadAll(resp.Body)
			if readErr != nil {
				t.Fatalf("read response: %v", readErr)
			}
			return response{StatusCode: resp.StatusCode, Body: string(body)}
		}
		if time.Now().After(deadline) {
			t.Fatalf("request %s: %v", url, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
