package scfinvoker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"

	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestIsDeployedRequiresPostDeployMarkerWhenDeploymentIDIsEmpty(t *testing.T) {
	if isDeployed(&cloudnodepb.CloudNode{PackageId: "pkg-1"}) {
		t.Fatal("package-only node must not be invokable before deploy")
	}
	if !isDeployed(&cloudnodepb.CloudNode{PackageId: "pkg-1", DeploymentId: "dep-1"}) {
		t.Fatal("node with deployment id should be invokable")
	}
	metadata, err := structpb.NewStruct(map[string]any{"deployment_ready": true})
	if err != nil {
		t.Fatal(err)
	}
	if !isDeployed(&cloudnodepb.CloudNode{PackageId: "pkg-1", Metadata: metadata}) {
		t.Fatal("node with local deployment marker should be invokable")
	}
}

func TestCollectorPublishLeaseCallsUseCollectorGatewayIdentity(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		require.Equal(t, "collector", r.Header.Get("X-Moox-Caller"))
		require.Equal(t, "crypto", r.Header.Get("X-Space-Id"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/service/publishlease/AcquireCollectorPublishLease", "/api/service/publishlease/RenewCollectorPublishLease":
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"space_id":"crypto","lease_id":"lease-1","fencing_token":"7","expires_at":"2026-10-03T12:02:00Z"}`))
		case "/api/service/publishlease/ReleaseCollectorPublishLease":
			_, _ = w.Write([]byte(`{"ret_info":{"code":0},"released":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := &Client{target: server.URL, auth: runtimeapp.AuthConfig{AccessKey: "collector", SecretKey: "secret", Caller: "collector", TargetNode: "gateway"}, http: server.Client()}

	lease, err := client.AcquireCollectorPublishLease(context.Background(), "crypto", "holder-1")
	require.NoError(t, err)
	require.EqualValues(t, 7, lease.FencingToken)
	renewed, err := client.RenewCollectorPublishLease(context.Background(), lease)
	require.NoError(t, err)
	require.Equal(t, lease.LeaseID, renewed.LeaseID)
	require.NoError(t, client.ReleaseCollectorPublishLease(context.Background(), renewed))
	require.Equal(t, []string{
		"/api/service/publishlease/AcquireCollectorPublishLease",
		"/api/service/publishlease/RenewCollectorPublishLease",
		"/api/service/publishlease/ReleaseCollectorPublishLease",
	}, calls)
}

func TestParsePublishFencingToken(t *testing.T) {
	for _, raw := range []string{`7`, `"7"`} {
		token, err := parsePublishFencingToken([]byte(raw))
		require.NoError(t, err)
		require.EqualValues(t, 7, token)
	}
	_, err := parsePublishFencingToken([]byte(`0`))
	require.Error(t, err)
}

func TestSubmitRuntimeConfigsRejectsMissingJobIdentityAsAmbiguous(t *testing.T) {
	for _, body := range []string{"", `{"ret_info":{"code":0}}`, `{"ret_info":{"code":0},"job_id":"  "}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			client := &Client{target: server.URL, auth: runtimeapp.AuthConfig{AccessKey: "collector", SecretKey: "secret", Caller: "collector", TargetNode: "gateway"}, http: server.Client()}

			jobID, err := client.SubmitRuntimeConfigs(context.Background(), "crypto", []*cloudnodepb.NodeRuntimeConfigPatch{{NodeId: "timer-1"}})
			require.ErrorIs(t, err, ErrRuntimeConfigSubmissionUnknown)
			require.Empty(t, jobID)
		})
	}
}

func TestSubmitRuntimeConfigsClassifiesPostSendUnknownOutcomes(t *testing.T) {
	for name, response := range map[string]struct {
		status int
		body   string
	}{
		"server error":      {status: http.StatusBadGateway, body: `upstream unavailable`},
		"malformed success": {status: http.StatusOK, body: `{"ret_info":{"code":0},`},
		"empty success":     {status: http.StatusOK, body: ""},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(response.status)
				_, _ = w.Write([]byte(response.body))
			}))
			defer server.Close()
			client := &Client{target: server.URL, auth: runtimeapp.AuthConfig{AccessKey: "collector", SecretKey: "secret", Caller: "collector", TargetNode: "gateway"}, http: server.Client()}

			_, err := client.SubmitRuntimeConfigs(context.Background(), "crypto", []*cloudnodepb.NodeRuntimeConfigPatch{{NodeId: "timer-1"}})
			require.ErrorIs(t, err, ErrRuntimeConfigSubmissionUnknown)
		})
	}
}

func TestSubmitRuntimeConfigsClassifiesConnectionLossAfterRequestAsUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "HTTP test server does not support hijacking", http.StatusInternalServerError)
			return
		}
		connection, _, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack connection: %v", err)
			return
		}
		_ = connection.Close()
	}))
	defer server.Close()
	client := &Client{target: server.URL, auth: runtimeapp.AuthConfig{AccessKey: "collector", SecretKey: "secret", Caller: "collector", TargetNode: "gateway"}, http: server.Client()}

	_, err := client.SubmitRuntimeConfigs(context.Background(), "crypto", []*cloudnodepb.NodeRuntimeConfigPatch{{NodeId: "timer-1"}})
	require.ErrorIs(t, err, ErrRuntimeConfigSubmissionUnknown)
}

func TestSubmitRuntimeConfigsKeepsLocalAndExplicitBusinessErrorsDefinite(t *testing.T) {
	t.Run("local configuration", func(t *testing.T) {
		client := &Client{}
		_, err := client.SubmitRuntimeConfigs(context.Background(), "crypto", []*cloudnodepb.NodeRuntimeConfigPatch{{NodeId: "timer-1"}})
		require.Error(t, err)
		require.False(t, errors.Is(err, ErrRuntimeConfigSubmissionUnknown))
	})
	t.Run("business rejection", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ret_info":{"code":14,"msg":"publish lease conflict"}}`))
		}))
		defer server.Close()
		client := &Client{target: server.URL, auth: runtimeapp.AuthConfig{AccessKey: "collector", SecretKey: "secret", Caller: "collector", TargetNode: "gateway"}, http: server.Client()}
		_, err := client.SubmitRuntimeConfigs(context.Background(), "crypto", []*cloudnodepb.NodeRuntimeConfigPatch{{NodeId: "timer-1"}})
		require.Error(t, err)
		require.False(t, errors.Is(err, ErrRuntimeConfigSubmissionUnknown))
	})
}

func TestRenewCollectorPublishLeaseClassifiesConflictAsStale(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ret_info":{"code":14,"msg":"collector publish lease is expired or fenced"}}`))
	}))
	defer server.Close()
	client := &Client{target: server.URL, auth: runtimeapp.AuthConfig{AccessKey: "collector", SecretKey: "secret", Caller: "collector", TargetNode: "gateway"}, http: server.Client()}

	_, err := client.RenewCollectorPublishLease(context.Background(), &CollectorPublishLease{
		SpaceID: "crypto", LeaseID: "lease-1", FencingToken: 7,
	})
	require.ErrorIs(t, err, ErrCollectorPublishLeaseStale)
}

func TestRenewCollectorPublishLeaseTransportFailureIsNotStale(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client := &Client{target: server.URL, auth: runtimeapp.AuthConfig{AccessKey: "collector", SecretKey: "secret", Caller: "collector", TargetNode: "gateway"}, http: server.Client()}

	_, err := client.RenewCollectorPublishLease(context.Background(), &CollectorPublishLease{
		SpaceID: "crypto", LeaseID: "lease-1", FencingToken: 7,
	})
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrCollectorPublishLeaseStale))
}
