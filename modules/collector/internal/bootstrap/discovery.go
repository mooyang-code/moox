// Package discovery resolves Collector runtime dependencies from SysDeploy.
package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
)

// Dependencies contains the service endpoints used by CollectMgr.
type Dependencies struct {
	AdminGatewayURL         string
	ServiceGatewayTarget    string
	ServiceAuth             ServiceAuthConfig
	StorageRPCGatewayTarget string
}

type retInfo struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

type endpoint struct {
	ServiceName string `json:"service_name"`
	ServiceKind string `json:"service_kind"`
	Protocol    string `json:"protocol"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	BaseURL     string `json:"base_url"`
	RPCAddress  string `json:"rpc_address"`
	GatewayPath string `json:"gateway_path"`
	Scope       string `json:"scope"`
	Status      string `json:"status"`
}

type activeDeploymentsRsp struct {
	RetInfo       *retInfo            `json:"ret_info"`
	DeploymentMap map[string]endpoint `json:"deployment_map"`
}

// Resolve returns Collector dependency endpoints.
//
// Local config remains the fallback so a developer can run collector without admin.
// When sysdeploy.admin_gateway_url and service auth are configured, active
// deployment records from t_service_deployments override local defaults.
func Resolve(ctx context.Context, cfg *Config) (Dependencies, error) {
	deps := Dependencies{
		AdminGatewayURL:         defaultAdminGatewayURL(cfg.SysDeploy.AdminGatewayURL),
		ServiceGatewayTarget:    defaultAdminGatewayURL(cfg.SysDeploy.AdminGatewayURL),
		ServiceAuth:             cfg.SysDeploy.ServiceAuth,
		StorageRPCGatewayTarget: cfg.Storage.GatewayTarget,
	}
	if strings.TrimSpace(cfg.SysDeploy.AdminGatewayURL) == "" {
		return deps, nil
	}
	active, err := fetchActiveDeployments(ctx, cfg)
	if err != nil {
		return deps, err
	}
	if v := endpointAddress(active, "moox_cloudnode", "cloudnode"); v != "" {
		_ = v // cloudnode RPC address is resolved for runtime deployments; control plane uses admin gateway.
	}
	if v := endpointGatewayTarget(active, "service_gateway"); v != "" {
		deps.ServiceGatewayTarget = v
	}
	// Storage clients use the native tRPC listener, never the public HTTP
	// gateway endpoint. A deployment without a native target leaves the local
	// gateway configuration intact rather than silently falling back to a
	// physical Storage listener.
	if v := endpointTRPCTarget(active, "service_gateway_internal", "service_gateway"); v != "" {
		deps.StorageRPCGatewayTarget = v
	}
	return deps, nil
}

func defaultAdminGatewayURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw != "" {
		return normalizeBaseURL(raw)
	}
	return "http://127.0.0.1:11002"
}

func fetchActiveDeployments(ctx context.Context, cfg *Config) (map[string]endpoint, error) {
	body := []byte("{}")
	url := normalizeBaseURL(cfg.SysDeploy.AdminGatewayURL) + "/api/service/sysdeploy/ListActiveServiceDeployments"
	auth := runtimeapp.AuthConfig{
		AccessKey:   cfg.SysDeploy.ServiceAuth.AccessKey,
		SecretKey:   cfg.SysDeploy.ServiceAuth.SecretKey,
		TargetNode:  cfg.SysDeploy.ServiceAuth.TargetNode,
		CAFile:      cfg.SysDeploy.ServiceAuth.CAFile,
		CAPEMBase64: cfg.SysDeploy.ServiceAuth.CAPEMBase64,
		ExpireSec:   cfg.SysDeploy.ServiceAuth.ExpireSeconds,
	}
	req, err := runtimeapp.NewSignedRequestWithContext(ctx, http.MethodPost, url, body, auth)
	if err != nil {
		return nil, err
	}
	client, err := runtimeapp.NewGatewayHTTPClient(5*time.Second, auth)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sysdeploy status=%d body=%s", resp.StatusCode, string(raw))
	}
	var out activeDeploymentsRsp
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if out.RetInfo == nil {
		return nil, fmt.Errorf("sysdeploy empty ret_info")
	}
	if out.RetInfo.Code != 0 {
		return nil, fmt.Errorf("sysdeploy: %s", out.RetInfo.Msg)
	}
	return out.DeploymentMap, nil
}

func endpointAddress(items map[string]endpoint, names ...string) string {
	for _, name := range names {
		item, ok := findEndpoint(items, name)
		if !ok {
			continue
		}
		if strings.TrimSpace(item.RPCAddress) != "" {
			return item.RPCAddress
		}
		if strings.TrimSpace(item.BaseURL) != "" {
			return item.BaseURL
		}
		if strings.TrimSpace(item.Host) != "" && item.Port > 0 {
			return fmt.Sprintf("%s:%d", item.Host, item.Port)
		}
	}
	return ""
}

func endpointGatewayTarget(items map[string]endpoint, names ...string) string {
	for _, name := range names {
		item, ok := findEndpoint(items, name)
		if !ok {
			continue
		}
		if value := strings.TrimSpace(item.BaseURL); value != "" {
			return strings.TrimRight(value, "/")
		}
		if strings.TrimSpace(item.Host) == "" || item.Port <= 0 {
			continue
		}
		protocol := strings.TrimSpace(item.Protocol)
		if protocol == "" {
			protocol = "http"
		}
		return fmt.Sprintf("%s://%s:%d", protocol, item.Host, item.Port)
	}
	return ""
}

func endpointTRPCTarget(items map[string]endpoint, names ...string) string {
	for _, name := range names {
		item, ok := findEndpoint(items, name)
		if !ok {
			continue
		}
		if !isHTTPProtocol(item.Protocol) {
			if value := trimNonHTTP(item.RPCAddress); value != "" {
				return value
			}
		}
		if strings.TrimSpace(item.Host) != "" && item.Port > 0 && !isHTTPProtocol(item.Protocol) {
			return fmt.Sprintf("%s:%d", strings.TrimSpace(item.Host), item.Port)
		}
	}
	return ""
}

func findEndpoint(items map[string]endpoint, name string) (endpoint, bool) {
	if item, ok := items[name]; ok {
		return item, true
	}
	normalized := normalizeEndpointName(name)
	for key, item := range items {
		if normalizeEndpointName(key) == normalized ||
			normalizeEndpointName(item.ServiceName) == normalized ||
			normalizeEndpointName(item.ServiceKind) == normalized {
			return item, true
		}
	}
	return endpoint{}, false
}

func normalizeEndpointName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	name = strings.ReplaceAll(name, "-", "_")
	return name
}

func trimNonHTTP(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" || isHTTPURL(raw) {
		return ""
	}
	return raw
}

func isHTTPProtocol(protocol string) bool {
	protocol = strings.TrimSpace(strings.ToLower(protocol))
	return protocol == "http" || protocol == "https"
}

func isHTTPURL(raw string) bool {
	raw = strings.TrimSpace(strings.ToLower(raw))
	return strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://")
}

func normalizeBaseURL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	return "http://" + raw
}
