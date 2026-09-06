package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	"github.com/mooyang-code/moox/packages/gatewayproxy"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

const maxServiceDeploymentSeedBytes = 4 << 20

var serviceDeploymentNodeIDPattern = regexp.MustCompile(`^[a-z0-9_-]+$`)

type serviceDeploymentSeed struct {
	Version  int                      `yaml:"version"`
	Node     serviceDeploymentNode    `yaml:"node"`
	Services []serviceDeploymentEntry `yaml:"services"`
}

type serviceDeploymentNode struct {
	ID            string `yaml:"id"`
	Name          string `yaml:"name"`
	PublicAddress string `yaml:"public_address"`
	Status        string `yaml:"status"`
}

type serviceDeploymentEntry struct {
	Name           string         `yaml:"name"`
	Kind           string         `yaml:"kind"`
	Protocol       string         `yaml:"protocol"`
	Host           string         `yaml:"host"`
	Port           int32          `yaml:"port"`
	GatewayPath    string         `yaml:"gateway_path"`
	GatewayService string         `yaml:"gateway_service_id"`
	GatewayEnabled bool           `yaml:"gateway_enabled"`
	Scope          string         `yaml:"scope"`
	Status         string         `yaml:"status"`
	Description    string         `yaml:"description"`
	DeploymentMode string         `yaml:"deployment_mode"`
	ExtraConfig    map[string]any `yaml:"extra_config"`
}

func isServiceDeploymentsCommand(args []string) bool {
	return len(args) > 1 && args[1] == "service-deployments"
}

func runServiceDeploymentsCommand(args []string, stdout io.Writer, stderr io.Writer) error {
	if len(args) < 2 || args[0] != "service-deployments" {
		return errors.New("expected service-deployments import subcommand")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if args[1] != "import" {
		return fmt.Errorf("unknown service-deployments subcommand %q", args[1])
	}

	fs := flag.NewFlagSet("service-deployments import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath, seedPath := defaultInitDBPath, ""
	nodeID := ""
	eventBusNATSURL := ""
	publicHost := ""
	withStorageShard := false
	disableStorageShard := false
	disabledServices := ""
	onlyServices := ""
	tradeConsoleHost := ""
	tradeConsolePort := 11200
	tradeGatewayURL := ""
	tradeGatewayNode := ""
	fs.StringVar(&dbPath, "db-path", dbPath, "SQLite database path")
	fs.StringVar(&seedPath, "file", seedPath, "service deployment seed YAML")
	fs.StringVar(&nodeID, "node-id", nodeID, "override the seed node ID")
	fs.StringVar(&eventBusNATSURL, "eventbus-nats-url", "", "EventBus client URL for this node")
	fs.StringVar(&publicHost, "public-host", "", "public IP or DNS name for public endpoints")
	fs.BoolVar(&withStorageShard, "with-storage-shard", false, "enable the independent DataShard route")
	fs.BoolVar(&disableStorageShard, "disable-storage-shard", false, "disable the independent DataShard route")
	fs.StringVar(&disabledServices, "disabled-services", "", "comma-separated services omitted from this deployment")
	fs.StringVar(&onlyServices, "only-services", "", "comma-separated services to import; all other seed services are ignored")
	fs.StringVar(&tradeConsoleHost, "trade-console-host", "", "TradeConsole browser endpoint host for a dedicated Trade node")
	fs.IntVar(&tradeConsolePort, "trade-console-port", 11200, "TradeConsole browser endpoint port")
	fs.StringVar(&tradeGatewayURL, "trade-gateway-url", "", "authenticated Trade Gateway HTTPS origin for the Admin BFF")
	fs.StringVar(&tradeGatewayNode, "trade-gateway-node", "", "authenticated Trade Gateway node ID for the Admin BFF")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected service-deployments arguments: %s", strings.Join(fs.Args(), " "))
	}
	if strings.TrimSpace(seedPath) == "" {
		return errors.New("--file is required")
	}
	if _, err := validateEventBusNATSURL(eventBusNATSURL); err != nil {
		return err
	}
	if withStorageShard && disableStorageShard {
		return errors.New("--with-storage-shard and --disable-storage-shard are mutually exclusive")
	}
	seed, err := loadServiceDeploymentSeed(seedPath)
	if err != nil {
		return err
	}
	if nodeID != "" {
		if nodeID != strings.TrimSpace(nodeID) || strings.TrimSpace(nodeID) == "" {
			return errors.New("--node-id must not contain leading or trailing whitespace")
		}
		seed.Node.ID = nodeID
		if strings.TrimSpace(onlyServices) != "" {
			// A scoped import may target a node absent from the seed file. Use
			// the requested ID for newly-created metadata, while existing node
			// name/status are preserved by importServiceDeploymentSeed.
			seed.Node.Name = nodeID
		}
	}
	if strings.TrimSpace(onlyServices) != "" {
		if err := selectSeedServices(&seed, onlyServices); err != nil {
			return err
		}
		if err := enableScopedTradeConsoleRoute(&seed); err != nil {
			return err
		}
	}
	if err := validateServiceDeploymentSeed(seed); err != nil {
		return err
	}
	if hasSeedService(seed, "eventbus") {
		if err := setSeedEventBusNATSURL(&seed, eventBusNATSURL); err != nil {
			return err
		}
	}
	if err := setSeedPublicHost(&seed, publicHost); err != nil {
		return err
	}
	if err := setSeedTradeConsoleEndpoint(&seed, tradeConsoleHost, tradeConsolePort); err != nil {
		return err
	}
	if err := setSeedTradeGatewayPlacement(&seed, tradeGatewayURL, tradeGatewayNode); err != nil {
		return err
	}
	if strings.TrimSpace(onlyServices) != "" {
		// A scoped import must not mutate unrelated optional routes.
	} else if withStorageShard {
		if err := enableOptionalStorageShard(&seed); err != nil {
			return err
		}
	} else if disableStorageShard {
		if err := disableOptionalStorageShard(&seed); err != nil {
			return err
		}
	}
	if strings.TrimSpace(onlyServices) == "" {
		if err := disableSeedServices(&seed, disabledServices); err != nil {
			return err
		}
	}
	if strings.TrimSpace(onlyServices) != "" && strings.TrimSpace(disabledServices) != "" {
		return errors.New("--only-services and --disabled-services are mutually exclusive")
	}
	if err := validateServiceDeploymentSeed(seed); err != nil {
		return err
	}
	if err := ensureAdminSchema(dbPath); err != nil {
		return err
	}
	db, err := openAdminCLIDB(dbPath)
	if err != nil {
		return err
	}
	defer closeAdminCLIDB(db)
	created, updated, err := importServiceDeploymentSeed(db, seed, strings.TrimSpace(onlyServices) != "")
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(map[string]any{
		"status": "ok", "command": "service-deployments.import", "node_id": seed.Node.ID,
		"created": created, "updated": updated, "services": len(seed.Services),
	})
}

func hasSeedService(seed serviceDeploymentSeed, name string) bool {
	for _, item := range seed.Services {
		if item.Name == name {
			return true
		}
	}
	return false
}

func selectSeedServices(seed *serviceDeploymentSeed, raw string) error {
	if seed == nil {
		return errors.New("service deployment seed is required")
	}
	wanted := make(map[string]struct{})
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			return errors.New("--only-services contains an empty service name")
		}
		wanted[name] = struct{}{}
	}
	selected := make([]serviceDeploymentEntry, 0, len(wanted))
	for _, item := range seed.Services {
		if _, ok := wanted[item.Name]; ok {
			selected = append(selected, item)
			delete(wanted, item.Name)
		}
	}
	if len(wanted) > 0 {
		names := make([]string, 0, len(wanted))
		for name := range wanted {
			names = append(names, name)
		}
		sort.Strings(names)
		return fmt.Errorf("--only-services contains unknown services: %s", strings.Join(names, ","))
	}
	seed.Services = selected
	return nil
}

func disableSeedServices(seed *serviceDeploymentSeed, raw string) error {
	if seed == nil {
		return errors.New("service deployment seed is required")
	}
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	requested := make(map[string]bool)
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			return errors.New("--disabled-services contains an empty service name")
		}
		requested[name] = true
	}
	for i := range seed.Services {
		if requested[seed.Services[i].Name] {
			seed.Services[i].Status = "disabled"
			delete(requested, seed.Services[i].Name)
		}
	}
	if len(requested) > 0 {
		names := make([]string, 0, len(requested))
		for name := range requested {
			names = append(names, name)
		}
		sort.Strings(names)
		return fmt.Errorf("--disabled-services contains unknown services: %s", strings.Join(names, ","))
	}
	return nil
}

func validateEventBusNATSURL(raw string) (*url.URL, error) {
	if raw == "" || raw != strings.TrimSpace(raw) {
		return nil, errors.New("--eventbus-nats-url is required and must not contain surrounding whitespace")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "tls" || parsed.Hostname() == "" || parsed.Port() == "" {
		return nil, errors.New("--eventbus-nats-url must be a tls URL with host and port")
	}
	if parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("--eventbus-nats-url must contain only scheme, host, and port")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("--eventbus-nats-url port must be between 1 and 65535")
	}
	return parsed, nil
}

func setSeedEventBusNATSURL(seed *serviceDeploymentSeed, raw string) error {
	if seed == nil {
		return errors.New("service deployment seed is required")
	}
	found := 0
	for i := range seed.Services {
		if seed.Services[i].Name != "eventbus" {
			continue
		}
		found++
		if seed.Services[i].ExtraConfig == nil {
			seed.Services[i].ExtraConfig = map[string]any{}
		}
		seed.Services[i].ExtraConfig["nats_url"] = raw
	}
	if found != 1 {
		return fmt.Errorf("service deployment seed must contain exactly one eventbus service, got %d", found)
	}
	return nil
}

func setSeedPublicHost(seed *serviceDeploymentSeed, raw string) error {
	if seed == nil {
		return errors.New("service deployment seed is required")
	}
	if raw == "" || raw != strings.TrimSpace(raw) {
		return errors.New("--public-host is required and must not contain surrounding whitespace")
	}
	parsed, err := url.Parse("https://" + raw)
	if err != nil || parsed.Hostname() == "" || parsed.Port() != "" ||
		parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("--public-host must be an IP address or DNS name without scheme or port")
	}
	seed.Node.PublicAddress = "https://" + parsed.Host
	for i := range seed.Services {
		if seed.Services[i].Scope == "public" && !seed.Services[i].GatewayEnabled {
			seed.Services[i].Host = parsed.Hostname()
		}
	}
	return nil
}

// setSeedTradeConsoleEndpoint overrides only the browser-facing TradeConsole
// route. The Trade process may run on another node, while Admin still resolves
// the canonical trade_console record from the control node.
func setSeedTradeConsoleEndpoint(seed *serviceDeploymentSeed, rawHost string, port int) error {
	if seed == nil {
		return errors.New("service deployment seed is required")
	}
	host := strings.TrimSpace(rawHost)
	if host == "" {
		if port != 11200 {
			return errors.New("--trade-console-port requires --trade-console-host")
		}
		return nil
	}
	if rawHost != host {
		return errors.New("--trade-console-host must not contain surrounding whitespace")
	}
	if !validTradeConsoleHost(host) {
		return errors.New("--trade-console-host must be a routable IP address or DNS name")
	}
	if port < 1 || port > 65535 {
		return errors.New("--trade-console-port must be between 1 and 65535")
	}
	found := false
	for i := range seed.Services {
		if seed.Services[i].Name != "trade_console" {
			continue
		}
		found = true
		seed.Services[i].Host = host
		seed.Services[i].Port = int32(port)
		seed.Services[i].Protocol = "http"
		seed.Services[i].Status = "active"
	}
	if !found {
		return errors.New("service deployment seed is missing trade_console")
	}
	return nil
}

func validTradeConsoleHost(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsLoopback() && !ip.IsUnspecified() && !ip.IsMulticast() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast()
	}
	if len(host) == 0 || len(host) > 253 || strings.Contains(host, "..") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-') {
				return false
			}
		}
	}
	return true
}

// enableScopedTradeConsoleRoute turns the otherwise control-only browser row
// into an authenticated route when a deployment seed is intentionally scoped
// to a remote Trade node. The full control seed remains non-gateway-enabled.
func enableScopedTradeConsoleRoute(seed *serviceDeploymentSeed) error {
	if seed == nil || seed.Node.ID == "control" {
		return nil
	}
	if !hasSeedService(*seed, "trade_console") {
		return nil
	}
	for i := range seed.Services {
		if seed.Services[i].Name != "trade_console" {
			continue
		}
		seed.Services[i].GatewayEnabled = true
		seed.Services[i].GatewayService = "trade_console"
		seed.Services[i].Protocol = "http"
		seed.Services[i].Host = "127.0.0.1"
		seed.Services[i].Port = 11200
		seed.Services[i].Status = "active"
		seed.Services[i].ExtraConfig = map[string]any{
			"gateway_methods": []any{
				"CreateTradingAccount", "UpdateTradingAccount", "GetTradingAccount", "ListTradingAccounts",
				"SetLeverage", "SyncTradingAccount", "CreateLogicalAccount", "GetLogicalAccount",
				"ListLogicalAccounts", "UpdateLogicalAccount", "AddLogicalAccountMember", "RemoveLogicalAccountMember",
				"PauseLogicalAccount", "ResumeLogicalAccount", "FlattenLogicalAccount", "PlaceManualOrder",
				"SubmitOrder", "CancelOrder", "GetOperatorAction", "GetLogicalAccountTarget", "GetOrder",
				"ListOrders", "ListFills", "ListPositions", "CreatePaperSimulation", "ClosePaperSimulation",
				"GetExecutionCapabilities", "QueryEquityCurve", "ListHoldings",
			},
			"gateway_callers": []any{"admin-gateway"},
			"monitor_enabled": false,
			"managed_by":      "moox-cli",
		}
		return nil
	}
	return errors.New("service deployment seed is missing trade_console")
}

func setSeedTradeGatewayPlacement(seed *serviceDeploymentSeed, rawURL, rawNode string) error {
	if seed == nil {
		return errors.New("service deployment seed is required")
	}
	rawURL, rawNode = strings.TrimSpace(rawURL), strings.TrimSpace(rawNode)
	if rawURL == "" && rawNode == "" {
		return nil
	}
	if rawURL == "" || rawNode == "" || !serviceDeploymentNodeIDPattern.MatchString(rawNode) {
		return errors.New("--trade-gateway-url and --trade-gateway-node are required together")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("--trade-gateway-url must be an origin without credentials, path, query, or fragment")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "http" {
		return errors.New("--trade-gateway-url must use HTTPS")
	}
	if scheme == "http" {
		host := strings.ToLower(parsed.Hostname())
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return errors.New("--trade-gateway-url must use HTTPS")
		}
	}
	for i := range seed.Services {
		if seed.Services[i].Name != "trade_console" {
			continue
		}
		if seed.Services[i].ExtraConfig == nil {
			seed.Services[i].ExtraConfig = map[string]any{}
		}
		seed.Services[i].ExtraConfig["gateway_url"] = rawURL
		seed.Services[i].ExtraConfig["gateway_node"] = rawNode
		return nil
	}
	return errors.New("service deployment seed is missing trade_console")
}

func disableOptionalStorageShard(seed *serviceDeploymentSeed) error {
	if seed == nil {
		return errors.New("service deployment seed is required")
	}
	for _, item := range seed.Services {
		if item.Name == "storage-shard" {
			return errors.New("service deployment seed already contains storage-shard")
		}
	}
	seed.Services = append(seed.Services, serviceDeploymentEntry{
		Name:           "storage-shard",
		Kind:           "storage",
		Protocol:       "http",
		Host:           "127.0.0.1",
		Port:           20107,
		GatewayPath:    "trpc.moox.storage.DataShard",
		Scope:          "internal",
		Status:         "disabled",
		Description:    "独立 DataShard tRPC 服务（未启用）",
		DeploymentMode: "process",
		ExtraConfig:    map[string]any{},
	})
	return validateServiceDeploymentSeed(*seed)
}

func enableOptionalStorageShard(seed *serviceDeploymentSeed) error {
	if seed == nil {
		return errors.New("service deployment seed is required")
	}
	for _, item := range seed.Services {
		if item.Name == "storage-shard" {
			return errors.New("service deployment seed already contains storage-shard")
		}
	}
	var primary *serviceDeploymentEntry
	for i := range seed.Services {
		if seed.Services[i].Name == "storage-primary" {
			primary = &seed.Services[i]
			break
		}
	}
	if primary == nil {
		return errors.New("service deployment seed is missing storage-primary")
	}
	rawRoutes, ok := primary.ExtraConfig["gateway_routes"].([]any)
	if !ok {
		return errors.New("storage-primary gateway_routes must be an array")
	}
	filtered := make([]any, 0, len(rawRoutes))
	for _, rawRoute := range rawRoutes {
		route, ok := rawRoute.(map[string]any)
		if !ok {
			return errors.New("storage-primary gateway_routes must contain objects")
		}
		if route["service_path"] == "trpc.moox.storage.DataShard" {
			continue
		}
		filtered = append(filtered, route)
	}
	primary.ExtraConfig["gateway_routes"] = filtered
	seed.Services = append(seed.Services, serviceDeploymentEntry{
		Name:           "storage-shard",
		Kind:           "storage",
		Protocol:       "http",
		Host:           "127.0.0.1",
		Port:           20107,
		GatewayPath:    "trpc.moox.storage.DataShard",
		GatewayService: "storage-shard",
		GatewayEnabled: true,
		Scope:          "internal",
		Status:         "active",
		Description:    "独立 DataShard tRPC 服务，仅允许 storage-primary Caller",
		DeploymentMode: "process",
		ExtraConfig: map[string]any{
			"health_url":      "http://127.0.0.1:20212/readyz",
			"health_kind":     "readiness",
			"monitor_enabled": true,
			"gateway_methods": []any{"MergeRows", "ReadRows", "ScanRows", "DeleteRows", "GetShardState"},
			"gateway_callers": []any{"storage-primary"},
		},
	})
	return validateServiceDeploymentSeed(*seed)
}

func loadServiceDeploymentSeed(path string) (serviceDeploymentSeed, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return serviceDeploymentSeed{}, fmt.Errorf("read service deployment seed: %w", err)
	}
	if len(raw) > maxServiceDeploymentSeedBytes {
		return serviceDeploymentSeed{}, fmt.Errorf("service deployment seed exceeds %d bytes", maxServiceDeploymentSeedBytes)
	}
	var seed serviceDeploymentSeed
	if err := yaml.Unmarshal(raw, &seed); err != nil {
		return serviceDeploymentSeed{}, fmt.Errorf("parse service deployment seed: %w", err)
	}
	if err := validateServiceDeploymentSeed(seed); err != nil {
		return serviceDeploymentSeed{}, err
	}
	return seed, nil
}

func validateServiceDeploymentSeed(seed serviceDeploymentSeed) error {
	if seed.Version != 1 {
		return fmt.Errorf("service deployment seed version must be 1")
	}
	nodeID := strings.TrimSpace(seed.Node.ID)
	if nodeID == "" || strings.TrimSpace(seed.Node.Name) == "" {
		return errors.New("service deployment seed node id and name are required")
	}
	if seed.Node.ID != nodeID {
		return errors.New("service deployment seed node id must not contain leading or trailing whitespace")
	}
	if !serviceDeploymentNodeIDPattern.MatchString(nodeID) {
		return errors.New("service deployment seed node id must use lowercase letters, digits, dash, or underscore")
	}
	if strings.TrimSpace(seed.Node.PublicAddress) == "" {
		return errors.New("service deployment seed node public_address is required")
	}
	if seed.Node.Status != "enabled" && seed.Node.Status != "disabled" {
		return fmt.Errorf("invalid node status %q", seed.Node.Status)
	}
	if len(seed.Services) == 0 {
		return errors.New("service deployment seed must contain at least one service")
	}
	seen := make(map[string]struct{}, len(seed.Services))
	gatewayRoutes := make([]gatewayproxy.Route, 0)
	for _, item := range seed.Services {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			return errors.New("service deployment name is required")
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("duplicate service deployment %q", name)
		}
		seen[name] = struct{}{}
		if item.Kind == "" || item.Host == "" || item.Port < 1 || item.Port > 65535 {
			return fmt.Errorf("invalid service deployment %q", name)
		}
		if item.Protocol != "http" && item.Protocol != "https" && item.Protocol != "trpc" {
			return fmt.Errorf("invalid protocol for %q", name)
		}
		if item.Scope != "public" && item.Scope != "internal" {
			return fmt.Errorf("invalid scope for %q", name)
		}
		if item.Status == "" || (item.Status != "active" && item.Status != "disabled") {
			return fmt.Errorf("invalid status for %q", name)
		}
		if item.DeploymentMode != "process" && item.DeploymentMode != "endpoint" {
			return fmt.Errorf("invalid deployment_mode for %q", name)
		}
		if item.GatewayEnabled && strings.TrimSpace(item.GatewayService) == "" {
			return fmt.Errorf("gateway_service_id is required for %q", name)
		}
		if item.GatewayEnabled && item.Protocol != "http" {
			return fmt.Errorf("gateway-enabled protocol for %q must be http", name)
		}
		if item.GatewayEnabled {
			if err := validateSeedGatewayConfig(item); err != nil {
				return fmt.Errorf("invalid gateway config for %q: %w", name, err)
			}
			routes, err := seedGatewayRoutes(item)
			if err != nil {
				return fmt.Errorf("invalid gateway config for %q: %w", name, err)
			}
			gatewayRoutes = append(gatewayRoutes, routes...)
		}
	}
	if _, err := gatewayproxy.NormalizeAndHash(seed.Node.ID, gatewayRoutes); err != nil {
		return fmt.Errorf("invalid gateway route set: %w", err)
	}
	return nil
}

func validateSeedGatewayConfig(item serviceDeploymentEntry) error {
	if strings.TrimSpace(item.GatewayPath) == "" {
		return errors.New("gateway_path is required")
	}
	methods, err := validateSeedACL(item.ExtraConfig, "gateway_methods")
	if err != nil {
		return err
	}
	callers, err := validateSeedACL(item.ExtraConfig, "gateway_callers")
	if err != nil {
		return err
	}
	baseRoute := gatewayproxy.Route{
		ServiceID:      item.GatewayService,
		Address:        net.JoinHostPort(item.Host, strconv.Itoa(int(item.Port))),
		ServicePath:    item.GatewayPath,
		AllowedMethods: methods,
		AllowedCallers: callers,
	}
	if err := gatewayproxy.ValidateRoute(baseRoute); err != nil {
		return err
	}
	rawRoutes, ok := item.ExtraConfig["gateway_routes"]
	if !ok {
		return nil
	}
	routes, ok := rawRoutes.([]any)
	if !ok {
		return errors.New("gateway_routes must be an array")
	}
	for _, rawRoute := range routes {
		route, ok := rawRoute.(map[string]any)
		if !ok {
			return errors.New("gateway_routes must contain objects")
		}
		servicePath, _ := route["service_path"].(string)
		if strings.TrimSpace(servicePath) == "" {
			return errors.New("gateway route service_path is required")
		}
		port, ok := route["port"].(int)
		if !ok || port < 1 || port > 65535 {
			return errors.New("gateway route port must be between 1 and 65535")
		}
		methods, err := validateSeedACL(route, "gateway_methods")
		if err != nil {
			return err
		}
		callers, err := validateSeedACL(route, "gateway_callers")
		if err != nil {
			return err
		}
		nestedRoute := gatewayproxy.Route{
			ServiceID:      item.GatewayService,
			Address:        net.JoinHostPort(item.Host, strconv.Itoa(port)),
			ServicePath:    servicePath,
			AllowedMethods: methods,
			AllowedCallers: callers,
		}
		if err := gatewayproxy.ValidateRoute(nestedRoute); err != nil {
			return err
		}
	}
	return nil
}

func validateSeedACL(values map[string]any, key string) ([]string, error) {
	raw, ok := values[key]
	if !ok {
		return nil, fmt.Errorf("%s must be a nonempty array", key)
	}
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil, fmt.Errorf("%s must be a nonempty array", key)
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		value, ok := item.(string)
		if !ok || strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("%s must contain nonempty strings", key)
		}
		result = append(result, value)
	}
	return result, nil
}

func seedGatewayRoutes(item serviceDeploymentEntry) ([]gatewayproxy.Route, error) {
	methods, err := validateSeedACL(item.ExtraConfig, "gateway_methods")
	if err != nil {
		return nil, err
	}
	callers, err := validateSeedACL(item.ExtraConfig, "gateway_callers")
	if err != nil {
		return nil, err
	}
	timeoutMS, err := seedOptionalInt(item.ExtraConfig, "timeout_ms")
	if err != nil {
		return nil, err
	}
	maxBodyBytes, err := seedOptionalInt(item.ExtraConfig, "max_body_bytes")
	if err != nil {
		return nil, err
	}
	routes := []gatewayproxy.Route{{
		ServiceID:      item.GatewayService,
		Address:        net.JoinHostPort(item.Host, strconv.Itoa(int(item.Port))),
		ServicePath:    item.GatewayPath,
		AllowedMethods: methods,
		AllowedCallers: callers,
		TimeoutMS:      timeoutMS,
		MaxBodyBytes:   maxBodyBytes,
	}}
	rawRoutes, ok := item.ExtraConfig["gateway_routes"]
	if !ok {
		return routes, nil
	}
	rawRouteList, ok := rawRoutes.([]any)
	if !ok {
		return nil, errors.New("gateway_routes must be an array")
	}
	for _, rawRoute := range rawRouteList {
		route, ok := rawRoute.(map[string]any)
		if !ok {
			return nil, errors.New("gateway_routes must contain objects")
		}
		servicePath, _ := route["service_path"].(string)
		port, ok := route["port"].(int)
		if !ok || port < 1 || port > 65535 {
			return nil, errors.New("gateway route port must be between 1 and 65535")
		}
		methods, err := validateSeedACL(route, "gateway_methods")
		if err != nil {
			return nil, err
		}
		callers, err := validateSeedACL(route, "gateway_callers")
		if err != nil {
			return nil, err
		}
		timeoutMS, err := seedOptionalInt(route, "timeout_ms")
		if err != nil {
			return nil, err
		}
		maxBodyBytes, err := seedOptionalInt(route, "max_body_bytes")
		if err != nil {
			return nil, err
		}
		routes = append(routes, gatewayproxy.Route{
			ServiceID:      item.GatewayService,
			Address:        net.JoinHostPort(item.Host, strconv.Itoa(port)),
			ServicePath:    servicePath,
			AllowedMethods: methods,
			AllowedCallers: callers,
			TimeoutMS:      timeoutMS,
			MaxBodyBytes:   maxBodyBytes,
		})
	}
	return routes, nil
}

func seedOptionalInt(values map[string]any, key string) (int64, error) {
	raw, ok := values[key]
	if !ok {
		return 0, nil
	}
	switch value := raw.(type) {
	case int:
		return int64(value), nil
	case int32:
		return int64(value), nil
	case int64:
		return value, nil
	case uint:
		return int64(value), nil
	case uint32:
		return int64(value), nil
	case uint64:
		if value > uint64(^uint64(0)>>1) {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		return int64(value), nil
	case float64:
		if value != float64(int64(value)) {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		return int64(value), nil
	default:
		return 0, fmt.Errorf("%s must be an integer", key)
	}
}

func importServiceDeploymentSeed(db *gorm.DB, seed serviceDeploymentSeed, preserveExistingNodeMetadata bool) (created, updated int, err error) {
	err = db.Transaction(func(tx *gorm.DB) error {
		var node sysdeploy.GatewayNode
		nodeResult := tx.Where("c_node_id = ?", seed.Node.ID).Find(&node)
		if nodeResult.Error != nil {
			return fmt.Errorf("find gateway node: %w", nodeResult.Error)
		}
		if nodeResult.RowsAffected > 0 {
			if !preserveExistingNodeMetadata {
				if err := tx.Model(&sysdeploy.GatewayNode{}).Where("c_node_id = ?", seed.Node.ID).Updates(map[string]any{
					"c_name": seed.Node.Name, "c_public_address": seed.Node.PublicAddress, "c_status": seed.Node.Status,
				}).Error; err != nil {
					return fmt.Errorf("update gateway node: %w", err)
				}
			}
		} else {
			if err := tx.Create(&sysdeploy.GatewayNode{NodeID: seed.Node.ID, Name: seed.Node.Name, PublicAddress: seed.Node.PublicAddress, Status: seed.Node.Status}).Error; err != nil {
				return fmt.Errorf("create gateway node: %w", err)
			}
		}

		for _, item := range seed.Services {
			extraConfig := item.ExtraConfig
			if extraConfig == nil {
				extraConfig = map[string]any{}
			}
			extra, marshalErr := json.Marshal(extraConfig)
			if marshalErr != nil {
				return fmt.Errorf("encode extra_config for %q: %w", item.Name, marshalErr)
			}
			deployment := &sysdeploy.Deployment{
				NodeID: seed.Node.ID, ServiceName: item.Name, ServiceKind: item.Kind, Protocol: item.Protocol,
				Host: item.Host, Port: item.Port, GatewayPath: item.GatewayPath, GatewayServiceID: item.GatewayService,
				GatewayEnabled: item.GatewayEnabled, Scope: item.Scope, Status: item.Status, Description: item.Description,
				ExtraConfig: string(extra),
			}
			var existing sysdeploy.Deployment
			findResult := tx.Where("c_node_id = ? AND c_service_name = ?", seed.Node.ID, item.Name).Find(&existing)
			if findResult.Error != nil {
				return fmt.Errorf("find service deployment %q: %w", item.Name, findResult.Error)
			}
			if findResult.RowsAffected > 0 {
				if err := tx.Model(&existing).Updates(map[string]any{
					"c_service_kind": deployment.ServiceKind, "c_protocol": deployment.Protocol, "c_host": deployment.Host,
					"c_port": deployment.Port, "c_gateway_path": deployment.GatewayPath, "c_gateway_service_id": deployment.GatewayServiceID,
					"c_gateway_enabled": deployment.GatewayEnabled, "c_scope": deployment.Scope, "c_status": deployment.Status,
					"c_description": deployment.Description, "c_extra_config": deployment.ExtraConfig,
				}).Error; err != nil {
					return fmt.Errorf("update service deployment %q: %w", item.Name, err)
				}
				updated++
			} else {
				if err := tx.Create(deployment).Error; err != nil {
					return fmt.Errorf("create service deployment %q: %w", item.Name, err)
				}
				created++
			}
		}
		return nil
	})
	return created, updated, err
}
