package control

import (
	"context"
	"encoding/json"
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
