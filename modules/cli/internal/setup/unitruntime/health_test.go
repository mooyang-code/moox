package unitruntime

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
)

// 健康端口只听本机、不验签：运行助手直接请求 127.0.0.1，并核对应答的是本次拉起的进程。
func TestRuntimeHealthBindsResponseToRunningBinary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Contains(t, []string{"/healthz", "/readyz"}, r.URL.Path)
		for name := range r.Header {
			require.False(t, strings.HasPrefix(name, "X-Moox-"), "健康探测不带签名头：%s", name)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ready": true, "binary_sha256": "running-binary", "boot_id": "current-boot"})
	}))
	defer server.Close()
	_, rawPort, err := net.SplitHostPort(server.Listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(rawPort)
	require.NoError(t, err)
	state := runtimeState{catalog: servicecatalog.Catalog{Components: []servicecatalog.Component{{ID: "monitor", Health: servicecatalog.Health{Port: port}}}}}
	component := Component{ID: "monitor"}
	for _, readiness := range []bool{false, true} {
		ready, err := state.probe(context.Background(), component, pidRecord{BinarySHA256: "running-binary", BootID: "current-boot"}, readiness)
		require.NoError(t, err)
		require.True(t, ready)
	}
	_, err = state.probe(context.Background(), component, pidRecord{BinarySHA256: "different-binary"}, true)
	require.ErrorIs(t, err, errHealthIdentity)
	_, err = state.probe(context.Background(), component, pidRecord{BinarySHA256: "running-binary", BootID: "previous-boot"}, true)
	require.ErrorIs(t, err, errHealthIdentity)
}

func TestNotReadyReasonsTakeNamesOnly(t *testing.T) {
	raw := []byte(`{"ready":false,"details":{"evaluation_ready":false,"database_ready":true,"deployment_enabled":false,"metrics_reporter_error":"password=abc","missing_services":["trpc.moox.archive.ArchiveWorker"]}}`)
	require.Equal(t, []string{"evaluation_ready", "trpc.moox.archive.ArchiveWorker"}, notReadyReasons(raw))
	require.Empty(t, notReadyReasons([]byte("not json")))
}
