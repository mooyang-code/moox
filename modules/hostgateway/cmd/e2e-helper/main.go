// Command e2e-helper runs native cross-module test fixtures through the host
// gateway router. Its synthetic boundary adapts test callers pending stage D;
// production uses neither this boundary nor fixture upstream address overrides.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/directory"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/router"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/snapshot"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/store"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayroute"
	directorypb "github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gopkg.in/yaml.v3"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/filter"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

func main() {
	mode := flag.String("mode", "kline-native", "kline-native or collector-period-native")
	deploymentYAML := flag.String("deployment-yaml", "", "test deployment YAML")
	routeScope := flag.String("route-scope", "", "test route scope")
	nodeID := flag.String("node-id", "", "test host ID")
	upstream := flag.String("upstream-addr", "", "loopback fixture upstream")
	metadata := flag.String("metadata-upstream-addr", "", "optional metadata fixture upstream")
	address := flag.String("listen-addr", "127.0.0.1:0", "fixture listener")
	ready := flag.String("ready-file", "", "fixture readiness file")
	nonces := flag.String("nonce-dir", "", "fixture nonce directory")
	keyID := flag.String("key-id", "", "test caller key ID")
	flag.Parse()
	var err error
	switch *mode {
	case "kline-native":
		err = runKlineNative(*nodeID, *upstream, *address, *ready, *nonces, *keyID, os.Getenv("MOOX_GATEWAY_E2E_SERVICE_SECRET"))
	case "collector-period-native":
		err = runCollectorPeriodNative(*deploymentYAML, *routeScope, *upstream, *metadata, *nodeID, *address, *ready, *nonces, *keyID, os.Getenv("MOOX_GATEWAY_E2E_SERVICE_SECRET"))
	default:
		err = errors.New("unsupported native fixture mode")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runKlineNative(nodeID, upstreamAddress, listenAddress, readyFile, nonceDirectory, keyID, secret string) error {
	return runNativeRoutes(nodeID, []gatewayroute.Route{{ServiceID: "storage-primary", Address: upstreamAddress, ServicePath: "trpc.moox.storage.PrimaryStore",
		AllowedMethods: []string{"ReadTimeSeriesRows", "UpsertFields"}, AllowedCallers: []string{"moox-cli"}}}, "moox-cli", listenAddress, readyFile, nonceDirectory, keyID, secret)
}

type fixtureForwarder struct {
	upstream  *router.Upstream
	addresses map[string]string
}

func (f fixtureForwarder) Forward(ctx context.Context, route servicecatalog.Route, serialization int, body []byte, metadata codec.MetaData) ([]byte, error) {
	route.Address = f.addresses[route.ServicePath]
	return f.upstream.Forward(ctx, route, serialization, body, metadata)
}

func runNativeRoutes(hostID string, routes []gatewayroute.Route, caller, listenAddress, readyFile, nonceDirectory, keyID, secret string) error {
	incoming := gatewayauth.Credentials{Caller: caller, KeyID: keyID, Secret: secret}
	if !servicecatalog.ValidHostID(hostID) {
		return errors.New("native gateway identity: canonical host ID required")
	}
	if _, err := gatewayauth.Sign(incoming, gatewayauth.Request{Method: "POST", Path: "/", TargetNode: hostID}, time.Now()); err != nil {
		return fmt.Errorf("native gateway identity: %w", err)
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return err
	}
	internalCaller := caller
	externalPrincipal := ""

	// The legacy period harness uses collector credentials for its simulated
	// SCF worker. ClaimTimerBatch belongs to scf-collector's external policy;
	// this test boundary models Access without widening the production ACL.
	if caller == "collector" && len(routes) == 1 && routes[0].ServicePath == "trpc.moox.collector.MarketFetchRuntime" {
		internalCaller, externalPrincipal = "access", "scf-collector"
	}
	digest := sha256.Sum256([]byte("synthetic fixture/" + secret))
	internal := gatewayauth.Credentials{Caller: internalCaller, KeyID: keyID, Secret: hex.EncodeToString(digest[:])}
	dir := servicecatalog.Directory{Hosts: map[string]servicecatalog.DirectoryHost{hostID: {Address: "127.0.0.1"}}, Services: map[string][]string{}}
	raw := &pb.HostGatewaySnapshot{SchemaVersion: 1, HostId: hostID}
	addresses := map[string]string{}
	for _, selected := range routes {
		host, _, err := net.SplitHostPort(selected.Address)
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			return errors.New("fixture upstream must be literal loopback")
		}
		service, ok := catalog.Service(selected.ServicePath)
		if !ok {
			return errors.New("fixture service absent from catalog")
		}
		componentID := ""
		for _, c := range catalog.Components {
			for _, candidate := range c.Services {
				if candidate.Path == service.Path {
					componentID = c.ID
				}
			}
		}
		dir.Services[service.Path] = []string{hostID}
		addresses[service.Path] = selected.Address
		timeout, limit := service.TimeoutMS, service.MaxBodyBytes
		if timeout == 0 {
			timeout = servicecatalog.DefaultTimeoutMS
		}
		if limit == 0 {
			limit = servicecatalog.DefaultMaxBodyBytes
		}
		for _, method := range selected.AllowedMethods {
			if !catalog.Allowed(internalCaller, service.Path, method) {
				if externalPrincipal != "" {
					continue
				}
				return errors.New("fixture caller/method absent from catalog")
			}
			raw.Routes = append(raw.Routes, &pb.HostGatewayRoute{ComponentId: componentID, ServicePath: service.Path, Method: method,
				Address: net.JoinHostPort("127.0.0.1", strconv.Itoa(service.Port)), TimeoutMs: timeout, MaxBodyBytes: limit, ReadOnly: catalog.ReadOnly(service.Path, method), Callers: []string{internalCaller}})
		}
	}
	dir.Version, err = dir.VersionHash()
	if err != nil {
		return err
	}
	raw.Directory = &directorypb.ServiceDirectory{Version: dir.Version, Hosts: map[string]*directorypb.DirectoryHost{hostID: {Address: "127.0.0.1"}}, Services: map[string]*directorypb.ServiceHosts{}}
	for path, hosts := range dir.Services {
		raw.Directory.Services[path] = &directorypb.ServiceHosts{HostIds: hosts}
	}
	raw.VerificationKeys = []*pb.GatewayVerificationKey{{Caller: internalCaller, KeyId: keyID, Secret: []byte(internal.Secret)}}
	raw.Hash, err = pb.SnapshotHash(raw)
	if err != nil {
		return err
	}
	view, err := snapshot.Build(hostID, raw)
	if err != nil {
		return err
	}
	state := &snapshot.State{}
	state.Apply(view)
	nonces, err := store.OpenNonces(nonceDirectory)
	if err != nil {
		return err
	}
	defer nonces.Close()
	upstream := router.NewUpstream()
	defer upstream.Close()
	proxy, err := router.NewService(router.ServiceOptions{State: state, Nonces: nonces, Forwarder: fixtureForwarder{upstream: upstream, addresses: addresses}})
	if err != nil {
		return err
	}
	defer proxy.Close()
	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return err
	}
	defer listener.Close()
	boundary := func(ctx context.Context, req interface{}, next filter.ServerHandleFunc) (interface{}, error) {
		message := codec.Message(ctx)
		path := message.ServerRPCName()
		if caller == "moox-cli" && path == "/"+directory.Path+"/GetDirectory" {
			return next(ctx, req)
		}
		servicePath, method, ok := strings.Cut(strings.TrimPrefix(path, "/"), "/")
		if !ok {
			return nil, errors.New("invalid fixture RPC")
		}
		headers := http.Header{}
		for k, v := range message.ServerMetaData() {
			headers.Add(k, string(v))
		}
		body := req.(*codec.Body).Data
		if _, err := gatewayauth.Verify(incoming, gatewayauth.Request{Method: "POST", Path: path, TargetNode: hostID, Callee: servicePath, Func: method, Body: body}, headers, time.Now()); err != nil {
			return nil, err
		}
		if externalPrincipal != "" && !catalog.PrincipalAllowed(externalPrincipal, servicePath, method) {
			return nil, errors.New("caller is not allowed for service/method")
		}
		signed, err := gatewayauth.Sign(internal, gatewayauth.Request{Method: "POST", Path: path, TargetNode: hostID, Callee: servicePath, Func: method, Body: body}, time.Now())
		if err != nil {
			return nil, err
		}
		metadata := codec.MetaData{}
		for k, v := range message.ServerMetaData() {
			if !strings.HasPrefix(strings.ToLower(k), "x-moox-") {
				metadata[k] = slices.Clone(v)
			}
		}
		for k, v := range signed {
			metadata[k] = []byte(v[0])
		}
		message.WithServerMetaData(metadata)
		return next(ctx, req)
	}
	service := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(listener), server.WithAddress(listener.Addr().String()),
		server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithServiceName("trpc.moox.hostgateway.Fixture"),
		server.WithCurrentSerializationType(codec.SerializationTypeNoop), server.WithFilter(boundary))
	if err := proxy.Register(service); err != nil {
		return err
	}
	if caller == "moox-cli" {
		if err := directory.Register(service, state); err != nil {
			return err
		}
	}
	if err := writeReadyFile(readyFile, "ip://"+listener.Addr().String()); err != nil {
		return err
	}
	defer os.Remove(readyFile)
	ctx, stop := signal.NotifyContext(trpc.BackgroundContext(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- service.Serve() }()
	select {
	case <-ctx.Done():
		_ = service.Close(nil)
		return <-done
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
