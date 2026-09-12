// Package discovery resolves Collector runtime dependencies from SysDeploy.
package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
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
	// InvokeStorageRPCGatewayTarget is the address sent to SCF invoke
	// payloads. Overseas functions cannot reach a mainland private IP, so this
	// stays on the discovered public native gateway when an explicit private
	// target is used for Collector's own Storage RPC.
	InvokeStorageRPCGatewayTarget string
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
// deployment records from t_service_deployments supply the public native
// gateway used for SCF invoke payloads. An explicit non-loopback
// storage.gateway_target (including a private IP from runtime.env) is kept for
// Collector's own Storage RPC.
func Resolve(ctx context.Context, cfg *Config) (Dependencies, error) {
	deps := Dependencies{
		AdminGatewayURL:               defaultAdminGatewayURL(cfg.SysDeploy.AdminGatewayURL),
		ServiceGatewayTarget:          defaultAdminGatewayURL(cfg.SysDeploy.AdminGatewayURL),
		ServiceAuth:                   cfg.SysDeploy.ServiceAuth,
		StorageRPCGatewayTarget:       cfg.Storage.GatewayTarget,
		InvokeStorageRPCGatewayTarget: cfg.Storage.GatewayTarget,
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
		deps.ServiceGatewayTarget = preferLocalServiceGatewayTarget(v, cfg.SysDeploy.AdminGatewayURL)
	}
	// Storage clients use the native tRPC listener selected for the Storage
	// deployment. The active-deployment response contains endpoints from every
	// node, keyed as "node_id/service_name". Selecting the generic name here
	// makes the chosen gateway depend on map iteration order and can send the
	// Collector to a gateway that does not own the Storage routes.
	nativeGateway := "service_gateway_native"
	if nodeID := strings.TrimSpace(cfg.Storage.GatewayNodeID); nodeID != "" {
		nativeGateway = nodeID + "/" + nativeGateway
	}
	discovered := strings.TrimSpace(endpointTRPCTarget(active, nativeGateway))
	configured := strings.TrimSpace(cfg.Storage.GatewayTarget)
	local, invoke, err := selectStorageRPCTargets(configured, discovered)
	if err != nil {
		return deps, fmt.Errorf("active %s deployment has no native tRPC target", nativeGateway)
	}
	deps.StorageRPCGatewayTarget = local
	deps.InvokeStorageRPCGatewayTarget = invoke
	return deps, nil
}

func selectStorageRPCTargets(configured, discovered string) (local, invoke string, err error) {
	configured = strings.TrimSpace(configured)
	discovered = strings.TrimSpace(discovered)
	switch {
	case isUsableConfiguredStorageTarget(configured):
		local = configured
	case discovered != "":
		local = discovered
	default:
		return "", "", fmt.Errorf("storage rpc target missing")
	}
	if discovered != "" {
		return local, discovered, nil
	}
	return local, local, nil
}

// preferLocalServiceGatewayTarget avoids sending same-host control-plane
// calls through the public HTTPS edge. The public address is not hairpin-safe
// on the control machine, so a healthy local Gateway can otherwise appear as
// a timeout to Collector (and stall timer reconciliation and EventBus work).
// Keep remote deployments on the discovered public endpoint.
func preferLocalServiceGatewayTarget(discovered, adminGatewayURL string) string {
	discovered = strings.TrimRight(strings.TrimSpace(discovered), "/")
	admin := normalizeBaseURL(adminGatewayURL)
	parsed, err := url.Parse(admin)
	if err != nil || parsed.Hostname() == "" || parsed.Port() != "11002" {
		return discovered
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return discovered
	}
	return admin
}

func isUsableConfiguredStorageTarget(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "ip" || parsed.Hostname() == "" || parsed.Port() == "" {
		return false
	}
	port, portErr := strconv.Atoi(parsed.Port())
	if portErr != nil || port < 1 || port > 65535 {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "localhost" || host == "ip6-localhost" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return false
	}
	return true
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
