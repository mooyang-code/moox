package engine

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const testEngineSecret = "0123456789abcdef0123456789abcdef"

func managerClientFor(t *testing.T, handler http.HandlerFunc) *ManagerClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	keyFile := filepath.Join(t.TempDir(), "gateway-factor-engine.key")
	require.NoError(t, os.WriteFile(keyFile, []byte(testEngineSecret+"\n"), 0o600))
	client, err := NewManagerClient(ManagerConfig{
		URL: server.URL, NodeID: "control", KeyID: "factor-engine", HMACKeyFile: keyFile, Timeout: 5 * time.Second,
	}, domain.EngineIdentity{EngineID: "factor-engine@mac", BootID: "boot", Version: "v1"})
	require.NoError(t, err)
	return client
}

func writeProto(t *testing.T, w http.ResponseWriter, message proto.Message) {
	t.Helper()
	raw, err := protojson.Marshal(message)
	require.NoError(t, err)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func TestManagerClientSignsRequests(t *testing.T) {
	var verified gatewayauth.Claims
	var path string
	client := managerClientFor(t, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		path = r.URL.Path
		verified, err = gatewayauth.Verify(gatewayauth.Credentials{KeyID: "factor-engine", Secret: testEngineSecret},
			gatewayauth.Request{Method: r.Method, Path: r.URL.EscapedPath(), TargetNode: "control", Body: body}, r.Header, time.Now())
		require.NoError(t, err)
		var req factorpb.SyncEngineCatalogReq
		require.NoError(t, protojson.Unmarshal(body, &req))
		require.Equal(t, "hash-0", req.GetKnownHash())
		require.Equal(t, "factor-engine@mac", req.GetEngine().GetEngineId())
		writeProto(t, w, &factorpb.SyncEngineCatalogRsp{
			RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, CatalogHash: "hash-1",
			Sets: []*factorpb.EngineSet{{FactorSet: &factorpb.FactorSet{SetId: "fset_a"}, ResultReady: true,
				Factors: []*factorpb.FactorDef{{FactorId: "bias", SourceCode: "src"}}}},
		})
	})

	snapshot, err := client.SyncCatalog(t.Context(), "hash-0")

	require.NoError(t, err)
	require.Equal(t, "/api/service/factormgr/SyncEngineCatalog", path)
	require.Equal(t, "factor-engine", verified.Caller)
	require.Equal(t, "hash-1", snapshot.Hash)
	require.Len(t, snapshot.Sets, 1)
	require.True(t, snapshot.Sets[0].ResultReady)
	require.Equal(t, "src", snapshot.Sets[0].Factors[0].SourceCode)
}

func TestManagerClientIgnoresProxyEnv(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")
	client := managerClientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		writeProto(t, w, &factorpb.EngineHeartbeatRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, LeaseTtlSeconds: 45})
	})

	ttl, err := client.Heartbeat(t.Context(), domain.EngineStatus{})

	require.NoError(t, err)
	require.Equal(t, 45*time.Second, ttl)
}

func TestManagerClientMapsConflict(t *testing.T) {
	client := managerClientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		writeProto(t, w, &factorpb.EngineHeartbeatRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_CONFLICT, Msg: "held by factor-engine@other"}})
	})

	_, err := client.Heartbeat(t.Context(), domain.EngineStatus{})

	require.ErrorIs(t, err, ErrLeaseConflict)
}

func TestManagerClientRejectsHTTPErrors(t *testing.T) {
	client := managerClientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})

	_, err := client.Heartbeat(t.Context(), domain.EngineStatus{})

	require.ErrorContains(t, err, "HTTP 403")
	require.NotErrorIs(t, err, ErrLeaseConflict)
}

func TestManagerClientPullDecodesWindow(t *testing.T) {
	client := managerClientFor(t, func(w http.ResponseWriter, _ *http.Request) {
		writeProto(t, w, &factorpb.PullRecalcJobRsp{
			RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, Found: true, LeaseToken: "token",
			Job: &factorpb.RecalcJob{JobId: "job-1", StartTime: "2026-10-04T00:00:00Z", EndTime: "2026-10-04T01:00:00Z",
				ProgressTime: "2026-10-04T00:30:00Z", Subjects: []string{"BTC"}},
			Set: &factorpb.EngineSet{FactorSet: &factorpb.FactorSet{SetId: "fset_a"}},
		})
	})

	job, found, err := client.PullRecalcJob(t.Context())

	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "token", job.LeaseToken)
	require.Equal(t, time.Date(2026, 10, 4, 0, 30, 0, 0, time.UTC), job.Window.Progress)
	require.Equal(t, []string{"BTC"}, job.Subjects)
	require.Equal(t, "fset_a", job.Set.Set.SetID)
}
