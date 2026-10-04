package gateway

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/errs"
)

func TestWriteForwardResponseGzipsLargeJSONWhenAccepted(t *testing.T) {
	body := []byte(`{"rows":[` + strings.Repeat(`{"value":"1234567890"},`, 200) + `{}]}`)
	rr := httptest.NewRecorder()

	writeForwardResponse(rr, body, map[string]string{"accept_encoding": "gzip"})

	if got := rr.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := rr.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Fatalf("Vary = %q, want Accept-Encoding", got)
	}
	if rr.Body.Len() >= len(body) {
		t.Fatalf("compressed body len = %d, want less than original %d", rr.Body.Len(), len(body))
	}
	zr, err := gzip.NewReader(bytes.NewReader(rr.Body.Bytes()))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer zr.Close()
	decoded, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(decoded, body) {
		t.Fatal("decoded body differs from original")
	}
}

func TestWriteForwardResponseKeepsSmallOrUnsupportedResponsesPlain(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    []byte
		headers map[string]string
	}{
		{name: "small", body: []byte(`{"ok":true}`), headers: map[string]string{"accept_encoding": "gzip"}},
		{name: "unsupported", body: []byte(strings.Repeat("x", 2048)), headers: map[string]string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()

			writeForwardResponse(rr, tc.body, tc.headers)

			if got := rr.Header().Get("Content-Encoding"); got != "" {
				t.Fatalf("Content-Encoding = %q, want empty", got)
			}
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
			}
			if !bytes.Equal(rr.Body.Bytes(), tc.body) {
				t.Fatal("body differs from original")
			}
		})
	}
}

func TestBuildForwardHeaders_WithHeaders_ShouldMapToHTTPHeaders(t *testing.T) {
	reqHead := buildForwardHeaders(map[string]string{
		"client_ip":    "10.0.0.1",
		"trace_id":     "trace-1",
		"access_token": "token-1",
		"space_id":     "space-1",
	})
	require.NotNil(t, reqHead)
	assert.Equal(t, "10.0.0.1", reqHead.Header.Get("X-Client-Ip"))
	assert.Equal(t, "trace-1", reqHead.Header.Get("X-Trace-Id"))
	assert.Equal(t, "token-1", reqHead.Header.Get("X-Access-Token"))
	assert.Equal(t, "space-1", reqHead.Header.Get("X-Space-Id"))
}

func TestForwardTradeConsoleToGatewaySignsRemoteRequest(t *testing.T) {
	const secret = "trade-gateway-secret"
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "moox-gateway-service")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", secret)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Equal(t, "/api/service/trade_console/ListTradingAccounts", r.URL.Path)
		_, err = gatewayauth.Verify(gatewayauth.Credentials{KeyID: "moox-gateway-service", Secret: secret}, gatewayauth.Request{Method: http.MethodPost, Path: r.URL.EscapedPath(), TargetNode: "trade-node", Body: body}, r.Header, time.Now())
		require.NoError(t, err)
		require.Equal(t, "space-1", r.Header.Get("X-Space-Id"))
		_, _ = w.Write([]byte(`{"ret_info":{"code":0}}`))
	}))
	defer server.Close()
	body, err := forwardTradeConsoleToGateway(context.Background(), "ListTradingAccounts", ServiceDetail{GatewayURL: server.URL, GatewayNode: "trade-node"}, []byte(`{}`), map[string]string{"space_id": "space-1"})
	require.NoError(t, err)
	require.JSONEq(t, `{"ret_info":{"code":0}}`, string(body))
}

func TestWriteForwardError_ShouldWriteRetInfo(t *testing.T) {
	rr := httptest.NewRecorder()
	writeForwardError(context.Background(), rr, errs.New(10001, "forward failed"), map[string]string{"origin": "https://app.example.com"})
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "ret_info")
	assert.Equal(t, "10001", rr.Header().Get("trpc-ret"))
}

func TestAddVaryHeader_ExistingValue_ShouldAppend(t *testing.T) {
	rr := httptest.NewRecorder()
	rr.Header().Set("Vary", "Origin")
	addVaryHeader(rr, "Accept-Encoding")
	assert.Equal(t, "Origin, Accept-Encoding", rr.Header().Get("Vary"))
}

func TestAddVaryHeader_DuplicateValue_ShouldKeepSingle(t *testing.T) {
	rr := httptest.NewRecorder()
	rr.Header().Set("Vary", "Accept-Encoding")
	addVaryHeader(rr, "Accept-Encoding")
	assert.Equal(t, "Accept-Encoding", rr.Header().Get("Vary"))
}
