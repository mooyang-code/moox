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
		if got := r.Header.Get("X-Moox-Target-Node"); got != "gateway-gz-122" {
			t.Fatalf("X-Moox-Target-Node = %q, want gateway-gz-122", got)
		}
		if r.URL.Path != "/api/service/sysdeploy/ListActiveServiceDeployments" {
			t.Fatalf("path = %s, want sysdeploy active deployments", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ret_info": map[string]any{"code": 0, "msg": "ok"},
			"deployment_map": map[string]any{
				"service_gateway": map[string]any{
					"service_name": "service_gateway",
					"protocol":     "https",
					"host":         "gw.example.com",
					"port":         11001,
					"gateway_path": "/api/service",
					"scope":        "public",
					"status":       "active",
				},
				"service_gateway_native": map[string]any{
					"service_name": "service_gateway_native",
					"protocol":     "trpc",
					"host":         "gw.example.com",
					"port":         11003,
					"scope":        "public",
					"status":       "active",
				},
				"storage-primary": map[string]any{
					"service_name": "storage-primary",
					"protocol":     "trpc",
					"host":         "storage.example.com",
					"port":         20100,
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
	cfg.SysDeploy.ServiceAuth.TargetNode = "gateway-gz-122"
	cfg.Storage.GatewayTarget = "ip://127.0.0.1:11003"

	deps, err := Resolve(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if deps.ServiceGatewayTarget != "https://gw.example.com:11001" {
		t.Fatalf("ServiceGatewayTarget = %q, want service_gateway deployment target", deps.ServiceGatewayTarget)
	}
	if deps.StorageRPCGatewayTarget != "gw.example.com:11003" {
		t.Fatalf("StorageRPCGatewayTarget = %q, want native service gateway target", deps.StorageRPCGatewayTarget)
	}
}

func TestResolveUsesPublicGatewayEndpoints(t *testing.T) {
	items := map[string]endpoint{
		"service_gateway": {
			ServiceName: "service_gateway",
			Protocol:    "https",
			Host:        "106.53.107.122",
			Port:        11001,
			Scope:       "public",
		},
		"service_gateway_native": {
			ServiceName: "service_gateway_native",
			Protocol:    "trpc",
			Host:        "106.53.107.122",
			Port:        11003,
			Scope:       "public",
		},
	}

	assert.Equal(t, "https://106.53.107.122:11001", endpointGatewayTarget(items, "service_gateway"))
	assert.Equal(t, "106.53.107.122:11003", endpointTRPCTarget(items, "service_gateway_native"))
}

func TestResolveSelectsStorageGatewayNode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ret_info": map[string]any{"code": 0, "msg": "ok"},
			"deployment_map": map[string]any{
				"control/service_gateway_native": map[string]any{
					"service_name": "service_gateway_native", "protocol": "trpc", "host": "control.example.com", "port": 11003,
				},
				"compute-1/service_gateway_native": map[string]any{
					"service_name": "service_gateway_native", "protocol": "trpc", "host": "compute.example.com", "port": 11003,
				},
			},
		})
	}))
	defer server.Close()

	cfg := Default()
	cfg.SysDeploy.AdminGatewayURL = server.URL
	cfg.SysDeploy.ServiceAuth.AccessKey = "ak"
	cfg.SysDeploy.ServiceAuth.SecretKey = "sk"
	cfg.SysDeploy.ServiceAuth.TargetNode = "control"
	cfg.Storage.GatewayNodeID = "compute-1"

	deps, err := Resolve(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, "compute.example.com:11003", deps.StorageRPCGatewayTarget)
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
