// Command e2e-helper starts the production Gateway service handler on an
// ephemeral loopback port for cross-module integration tests.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"

	"github.com/mooyang-code/moox/modules/gateway/internal/router"
	"github.com/mooyang-code/moox/modules/gateway/internal/store"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayroute"
	"gopkg.in/yaml.v3"
)

func main() {
	mode := flag.String("mode", "monitor-http", "helper mode: monitor-http, kline-native or collector-period-native")
	deploymentYAML := flag.String("deployment-yaml", "", "absolute production deployment YAML path")
	routeScope := flag.String("route-scope", "", "collector-period-native scope: storage-period, storage-metadata or collector-runtime")
	nodeID := flag.String("node-id", "", "target Gateway node ID")
	upstreamURL := flag.String("upstream-url", "", "loopback Monitor upstream URL")
	upstreamAddress := flag.String("upstream-addr", "", "loopback native tRPC upstream address")
	metadataUpstreamAddress := flag.String("metadata-upstream-addr", "", "optional loopback Metadata upstream for storage-period scope")
	listenAddress := flag.String("listen-addr", "127.0.0.1:0", "native gateway listen address")
	readyFile := flag.String("ready-file", "", "file receiving the service URL")
	nonceDirectory := flag.String("nonce-dir", "", "persistent nonce directory")
	keyID := flag.String("key-id", "", "service HMAC key ID")
	flag.Parse()
	if *mode == "collector-period-native" {
		err := runCollectorPeriodNative(*deploymentYAML, *routeScope, *upstreamAddress, *metadataUpstreamAddress, *nodeID, *listenAddress, *readyFile, *nonceDirectory, *keyID, os.Getenv("MOOX_GATEWAY_E2E_SERVICE_SECRET"))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := run(*mode, *nodeID, *upstreamURL, *upstreamAddress, *listenAddress, *readyFile, *nonceDirectory, *keyID, os.Getenv("MOOX_GATEWAY_E2E_SERVICE_SECRET")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(mode, nodeID, upstreamURL, upstreamAddress, listenAddress, readyFile, nonceDirectory, keyID, secret string) error {
	if mode == "kline-native" {
		return runKlineNative(nodeID, upstreamAddress, listenAddress, readyFile, nonceDirectory, keyID, secret)
	}
	if mode != "monitor-http" {
		return fmt.Errorf("unsupported mode %q", mode)
	}
	return runMonitorHTTP(nodeID, upstreamURL, readyFile, nonceDirectory, keyID, secret)
}

func runMonitorHTTP(nodeID, upstreamURL, readyFile, nonceDirectory, keyID, secret string) error {
	parsed, err := url.Parse(upstreamURL)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.Path != "" {
		return fmt.Errorf("upstream-url must be a loopback HTTP origin")
	}
	snapshot, err := gatewayroute.NormalizeAndHash(nodeID, []gatewayroute.Route{{
		ServiceID: "monitor", Address: parsed.Host, ServicePath: "trpc.moox.monitor.MonitorMgr",
		AllowedMethods: []string{"GetPeerSnapshot"},
		AllowedCallers: []string{"monitor"},
	}})
	if err != nil {
		return err
	}
	var table gatewayroute.Table
	if err := table.Replace(snapshot); err != nil {
		return err
	}
	nonces, err := store.OpenNonces(nonceDirectory)
	if err != nil {
		return err
	}
	defer nonces.Close()
	handler := router.New(router.Options{
		NodeID: nodeID, Credentials: gatewayauth.Credentials{KeyID: keyID, Secret: secret},
		MaxBodyBytes: 4 << 20, Table: &table, Nonces: nonces, Disabled: func() bool { return false },
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := os.MkdirAll(filepath.Dir(readyFile), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(readyFile, []byte("http://"+listener.Addr().String()), 0o600); err != nil {
		return err
	}
	defer os.Remove(readyFile)
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	ctx, stop := signal.NotifyContext(trpc.BackgroundContext(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(trpc.BackgroundContext(), 3*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-done:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func runKlineNative(nodeID, upstreamAddress, listenAddress, readyFile, nonceDirectory, keyID, secret string) error {
	host, _, err := net.SplitHostPort(strings.TrimSpace(upstreamAddress))
	if err != nil || (host != "127.0.0.1" && host != "::1") {
		return fmt.Errorf("upstream-addr must be a loopback host:port")
	}
	return runNativeRoutes(nodeID, []gatewayroute.Route{{
		ServiceID: "storage-primary", Address: upstreamAddress, ServicePath: "trpc.moox.storage.PrimaryStore",
		AllowedMethods: []string{"ReadTimeSeriesRows", "UpsertFields"}, AllowedCallers: []string{"moox-skill"},
	}}, "moox-skill", listenAddress, readyFile, nonceDirectory, keyID, secret)
}

func runNativeRoutes(nodeID string, routes []gatewayroute.Route, caller, listenAddress, readyFile, nonceDirectory, keyID, secret string) error {
	credentials := gatewayauth.Credentials{KeyID: keyID, Caller: caller, Secret: secret}
	if _, err := gatewayauth.Sign(credentials, gatewayauth.Request{Method: http.MethodPost, Path: "/", TargetNode: nodeID}, time.Now()); err != nil {
		return fmt.Errorf("native gateway identity: %w", err)
	}
	snapshot, err := gatewayroute.NormalizeAndHash(nodeID, routes)
	if err != nil {
		return err
	}
	var table gatewayroute.Table
	if err := table.Replace(snapshot); err != nil {
		return err
	}
	nonces, err := store.OpenNonces(nonceDirectory)
	if err != nil {
		return err
	}
	defer nonces.Close()
	desc, implementation := router.NativeServiceDesc(router.NativeOptions{
		NodeID:      nodeID,
		Credentials: credentials,
		Table:       &table, Nonces: nonces, Disabled: func() bool { return false },
	})
	listener, err := net.Listen("tcp", strings.TrimSpace(listenAddress))
	if err != nil {
		return err
	}
	service := server.New(
		server.WithNetwork("tcp"),
		server.WithProtocol("trpc"),
		server.WithServiceName("trpc.moox.gateway.ServiceGateway"),
		server.WithListener(listener),
		server.WithCurrentSerializationType(codec.SerializationTypeNoop),
	)
	if err := service.Register(desc, implementation); err != nil {
		listener.Close()
		return err
	}
	if err := writeReadyFile(readyFile, "ip://"+listener.Addr().String()); err != nil {
		listener.Close()
		return err
	}
	defer os.Remove(readyFile)
	ctx, stop := signal.NotifyContext(trpc.BackgroundContext(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- service.Serve() }()
	select {
	case <-ctx.Done():
		service.Close(nil)
		select {
		case err := <-done:
			return err
		case <-time.After(3 * time.Second):
			return fmt.Errorf("native gateway did not stop")
		}
	case err := <-done:
		return err
	}
}

type deploymentRoutePolicy struct {
	TimeoutMS      int64    `yaml:"timeout_ms"`
	MaxBodyBytes   int64    `yaml:"max_body_bytes"`
	GatewayMethods []string `yaml:"gateway_methods"`
	GatewayCallers []string `yaml:"gateway_callers"`
}

func runCollectorPeriodNative(path, scope, upstreamAddress, metadataUpstreamAddress, nodeID, listenAddress, readyFile, nonceDirectory, keyID, secret string) error {
	routes, err := loadCollectorPeriodRoutesWithMetadata(path, scope, upstreamAddress, metadataUpstreamAddress)
	if err != nil {
		return err
	}
	return runNativeRoutes(nodeID, routes, "collector", listenAddress, readyFile, nonceDirectory, keyID, secret)
}

func loadCollectorPeriodRoutesWithMetadata(path, scope, upstreamAddress, metadataUpstreamAddress string) ([]gatewayroute.Route, error) {
	routes, err := loadCollectorPeriodRoutes(path, scope, upstreamAddress)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(metadataUpstreamAddress) == "" {
		return routes, nil
	}
	if scope != "storage-period" {
		return nil, fmt.Errorf("metadata-upstream-addr requires storage-period route-scope")
	}
	metadataRoutes, err := loadCollectorPeriodRoutes(path, "storage-metadata", metadataUpstreamAddress)
	if err != nil {
		return nil, err
	}
	return append(routes, metadataRoutes...), nil
}

func loadCollectorPeriodRoutes(path, scope, upstreamAddress string) ([]gatewayroute.Route, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("deployment-yaml must be an absolute path")
	}
	host, port, err := net.SplitHostPort(upstreamAddress)
	if err != nil || (host != "127.0.0.1" && host != "::1") || port == "" {
		return nil, fmt.Errorf("upstream-addr must be a loopback host:port")
	}
	serviceID, servicePath := "storage-primary", "trpc.moox.storage.PrimaryStore"
	required := []string{"EnsureDatasetPeriod", "CommitTimeSeriesBatch", "RecordDatasetPeriodFailures", "GetDatasetPeriodStatus"}
	switch scope {
	case "storage-period":
	case "storage-metadata":
		servicePath = "trpc.moox.storage.Metadata"
		required = []string{"ApplyTagSnapshot", "GetTag", "ListSubjects", "ResolveSubjects"}
	case "collector-runtime":
		serviceID, servicePath = "collector-market-runtime", "trpc.moox.collector.MarketFetchRuntime"
		required = []string{"ClaimTimerBatch"}
	default:
		return nil, fmt.Errorf("unsupported route-scope %q", scope)
	}
	var seed struct {
		Services []struct {
			Name             string `yaml:"name"`
			Status           string `yaml:"status"`
			Host             string `yaml:"host"`
			Port             int    `yaml:"port"`
			GatewayPath      string `yaml:"gateway_path"`
			GatewayServiceID string `yaml:"gateway_service_id"`
			GatewayEnabled   bool   `yaml:"gateway_enabled"`
			ExtraConfig      struct {
				deploymentRoutePolicy `yaml:",inline"`
				GatewayRoutes         []struct {
					deploymentRoutePolicy `yaml:",inline"`
					ServicePath           string `yaml:"service_path"`
					Port                  int    `yaml:"port"`
				} `yaml:"gateway_routes"`
			} `yaml:"extra_config"`
		} `yaml:"services"`
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(raw, &seed); err != nil {
		return nil, fmt.Errorf("decode deployment-yaml: %w", err)
	}
	counts := make(map[string]int)
	var selected []gatewayroute.Route
	for _, service := range seed.Services {
		id := service.GatewayServiceID
		if id == "" && service.Name == serviceID {
			return nil, fmt.Errorf("required service %s needs explicit gateway_service_id", service.Name)
		}
		if id != serviceID {
			continue
		}
		if !service.GatewayEnabled {
			return nil, fmt.Errorf("required service %s is not gateway enabled", id)
		}
		if service.Status != "active" {
			return nil, fmt.Errorf("required service %s must have active status", id)
		}
		appendRoute := func(path string, port int, policy deploymentRoutePolicy) error {
			route := gatewayroute.Route{ServiceID: id, Address: net.JoinHostPort(service.Host, fmt.Sprint(port)), ServicePath: path,
				TimeoutMS: policy.TimeoutMS, MaxBodyBytes: policy.MaxBodyBytes,
				AllowedMethods: policy.GatewayMethods, AllowedCallers: policy.GatewayCallers}
			chosen := false
			for _, method := range required {
				if route.AllowsMethod(method) {
					counts[method]++
					chosen = true
				}
			}
			if !chosen {
				return nil
			}
			if path != servicePath || !route.AllowsCaller("collector") {
				return fmt.Errorf("required route %s must use %s and allow collector", id, servicePath)
			}
			if _, err := gatewayroute.NormalizeAndHash("validate-production-route", []gatewayroute.Route{route}); err != nil {
				return fmt.Errorf("invalid production route: %w", err)
			}
			route.Address = upstreamAddress
			selected = append(selected, route)
			return nil
		}
		if err := appendRoute(service.GatewayPath, service.Port, service.ExtraConfig.deploymentRoutePolicy); err != nil {
			return nil, err
		}
		for _, route := range service.ExtraConfig.GatewayRoutes {
			if route.ServicePath == "" || route.Port < 1 {
				return nil, fmt.Errorf("gateway_routes entries require service_path and positive port")
			}
			if err := appendRoute(route.ServicePath, route.Port, route.deploymentRoutePolicy); err != nil {
				return nil, err
			}
		}
	}
	for _, method := range required {
		if counts[method] != 1 {
			return nil, fmt.Errorf("required method %s must have exactly one route (got %d)", method, counts[method])
		}
	}
	if _, err := gatewayroute.NormalizeAndHash("validate-selected-routes", selected); err != nil {
		return nil, err
	}
	return selected, nil
}

func writeReadyFile(path, value string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(value), 0o600)
}
