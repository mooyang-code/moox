package bootstrap

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayproxy"
)

func TestLogicalAccountOwnerUsesConfiguredSignedGateway(t *testing.T) {
	upstreamTarget, upstream := startLogicalAccountService(t)
	credentials := gatewayauth.Credentials{KeyID: "strategy-key", Caller: "strategy", Secret: "strategy-test-secret"}
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", credentials.KeyID)
	t.Setenv("MOOX_GATEWAY_CALLER", credentials.Caller)
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", credentials.Secret)
	// Other dependencies use the local native gateway. It must not override Trade.
	t.Setenv("MOOX_SERVICE_GATEWAY_TARGET", "ip://127.0.0.1:1")
	t.Setenv("MOOX_GATEWAY_TARGET_NODE", "wrong-local-node")
	var seen int
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		_, err = gatewayauth.Verify(credentials, gatewayauth.Request{Method: r.Method, Path: r.URL.EscapedPath(), TargetNode: "trade-node", Body: body}, r.Header, time.Now())
		if err != nil {
			t.Error(err)
			w.WriteHeader(401)
			return
		}
		if r.URL.Path != "/api/service/trade_owner/GetLogicalAccount" {
			t.Errorf("path = %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		seen++
		route := gatewayproxy.Route{ServiceID: "trade_console", ServicePath: "trpc.moox.trade.TradeConsoleService", Address: strings.TrimPrefix(upstreamTarget, "ip://"), AllowedMethods: []string{"GetLogicalAccount"}, AllowedCallers: []string{"strategy"}, TimeoutMS: 1000, MaxBodyBytes: 1048576}
		rsp, err := gatewayproxy.Forward(r.Context(), nil, route, "GetLogicalAccount", body, r.Header)
		if err != nil {
			t.Error(err)
			w.WriteHeader(502)
			return
		}
		for key, values := range rsp.Header {
			w.Header()[key] = values
		}
		w.WriteHeader(rsp.StatusCode)
		_, _ = w.Write(rsp.Body)
	}))
	defer gateway.Close()
	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte("trade:\n  gateway_url: "+gateway.URL+"\n  target_node: trade-node\n  timeout: 1s\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	owner := newRPCService(nil, cfg).LogicalAccounts
	if err := owner.Validate(context.Background(), "space-1", "logical-1"); err != nil {
		t.Fatal(err)
	}
	if seen != 1 {
		t.Fatalf("gateway calls = %d", seen)
	}
	upstream.mu.Lock()
	defer upstream.mu.Unlock()
	if len(upstream.spaces) != 1 || upstream.spaces[0] != "space-1" {
		t.Fatalf("upstream spaces = %v", upstream.spaces)
	}
}
