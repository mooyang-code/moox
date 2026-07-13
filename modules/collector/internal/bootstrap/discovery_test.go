package bootstrap

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/assert"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveUsesActiveServiceGatewayAndStorageTargets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/service/sysdeploy/ListActiveServiceDeployments" {
			t.Fatalf("path = %s, want sysdeploy active deployments", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ret_info": map[string]any{"code": 0, "msg": "ok"},
			"deployment_map": map[string]any{
				"service_gateway": map[string]any{
					"service_name": "service_gateway",
					"protocol":     "http",
					"host":         "gw.example.com",
					"port":         11000,
					"gateway_path": "/api/service",
					"scope":        "public",
					"status":       "active",
				},
				"storage_metadata_trpc": map[string]any{
					"service_name": "storage_metadata_trpc",
					"protocol":     "trpc",
					"host":         "storage.example.com",
					"port":         20100,
					"scope":        "public",
					"status":       "active",
				},
				"storage_access_trpc": map[string]any{
					"service_name": "storage_access_trpc",
					"protocol":     "trpc",
					"host":         "storage.example.com",
					"port":         20102,
					"scope":        "public",
					"status":       "active",
				},
			},
		})
	}))
	defer server.Close()

	cfg := Default()
	cfg.SysDeploy.AdminGatewayURL = server.URL
	cfg.SysDeploy.ServiceAuth.AccessKey = "ak"
	cfg.SysDeploy.ServiceAuth.SecretKey = "sk"
	cfg.Storage.MetadataTarget = "127.0.0.1:20100"
	cfg.Storage.AccessTarget = "127.0.0.1:20102"

	deps, err := Resolve(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if deps.ServiceGatewayTarget != "http://gw.example.com:11000" {
		t.Fatalf("ServiceGatewayTarget = %q, want service_gateway deployment target", deps.ServiceGatewayTarget)
	}
	if deps.StorageMetadataTarget != "storage.example.com:20100" {
		t.Fatalf("StorageMetadataTarget = %q, want storage.example.com:20100", deps.StorageMetadataTarget)
	}
	if deps.StorageAccessTarget != "storage.example.com:20102" {
		t.Fatalf("StorageAccessTarget = %q, want storage.example.com:20102", deps.StorageAccessTarget)
	}
}

func TestResolvePrefersInternalServiceGateway(t *testing.T) {
	items := map[string]endpoint{
		"service_gateway": {
			ServiceName: "service_gateway",
			Protocol:    "http",
			Host:        "106.53.107.122",
			Port:        11000,
			Scope:       "public",
		},
		"service_gateway_internal": {
			ServiceName: "service_gateway_internal",
			Protocol:    "http",
			Host:        "127.0.0.1",
			Port:        11002,
			Scope:       "internal",
		},
	}

	got := endpointGatewayTarget(items, "service_gateway_internal", "service_gateway")
	assert.Equal(t, "http://127.0.0.1:11002", got)
}

func TestIsHTTPURL(t *testing.T) {
	assert.True(t, isHTTPURL("http://example.com"))
	assert.True(t, isHTTPURL("HTTPS://example.com"))
	assert.False(t, isHTTPURL(""))
	assert.False(t, isHTTPURL("127.0.0.1:20100"))
	assert.False(t, isHTTPURL("/relative"))
}

func TestIsHTTPProtocol(t *testing.T) {
	assert.True(t, isHTTPProtocol("http"))
	assert.True(t, isHTTPProtocol("HTTPS"))
	assert.False(t, isHTTPProtocol("trpc"))
	assert.False(t, isHTTPProtocol(""))
}

func TestNormalizeBaseURL(t *testing.T) {
	assert.Equal(t, "", normalizeBaseURL(""))
	assert.Equal(t, "http://gw.example.com", normalizeBaseURL("http://gw.example.com/"))
	assert.Equal(t, "https://gw.example.com", normalizeBaseURL("https://gw.example.com"))
	assert.Equal(t, "http://gw.example.com:11000", normalizeBaseURL("gw.example.com:11000"))
}

func TestIsStorageTRPCTarget(t *testing.T) {
	assert.True(t, isStorageTRPCTarget("127.0.0.1:20100"))
	assert.False(t, isStorageTRPCTarget(""))
	assert.False(t, isStorageTRPCTarget("http://127.0.0.1:20100"))
	assert.False(t, isStorageTRPCTarget("https://127.0.0.1:20100"))
}
