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
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/filter"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

func main() {
	mode := flag.String("mode", "kline-native", "kline-native, admin-native, doctor-native, cloudnode-native or collector-period-native")
	routeScope := flag.String("route-scope", "", "test route scope")
	nodeID := flag.String("node-id", "", "test host ID")
	upstream := flag.String("upstream-addr", "", "loopback fixture upstream")
	metadata := flag.String("metadata-upstream-addr", "", "optional metadata fixture upstream")
	runtimeAddress := flag.String("runtime-upstream-addr", "", "optional Collector runtime fixture upstream")
	address := flag.String("listen-addr", "127.0.0.1:0", "fixture listener")
	ready := flag.String("ready-file", "", "fixture readiness file")
	nonces := flag.String("nonce-dir", "", "fixture nonce directory")
	keyID := flag.String("key-id", "", "test caller key ID")
	flag.Parse()
	var err error
	switch *mode {
	case "access-native":
		err = runAccessNative(*nodeID, *upstream, *runtimeAddress, *address, *ready, *nonces, *keyID, os.Getenv("MOOX_GATEWAY_E2E_SERVICE_SECRET"))
	case "kline-native":
		err = runKlineNative(*nodeID, *upstream, *address, *ready, *nonces, *keyID, os.Getenv("MOOX_GATEWAY_E2E_SERVICE_SECRET"))
	case "storage-native":
		err = runComponentsNative([]string{"storage-primary", "storage-view"}, *nodeID, *upstream, *address, *ready, *nonces, *keyID, os.Getenv("MOOX_GATEWAY_E2E_SERVICE_SECRET"))
	case "egress-native":
		err = runComponentsNative([]string{"egress-proxy"}, *nodeID, *upstream, *address, *ready, *nonces, *keyID, os.Getenv("MOOX_GATEWAY_E2E_SERVICE_SECRET"))
	case "doctor-native":
		err = runComponentsNative([]string{"admin", "monitor"}, *nodeID, *upstream, *address, *ready, *nonces, *keyID, os.Getenv("MOOX_GATEWAY_E2E_SERVICE_SECRET"))
	case "admin-native":
		err = runAdminNative(*nodeID, *upstream, *address, *ready, *nonces, *keyID, os.Getenv("MOOX_GATEWAY_E2E_SERVICE_SECRET"))
	case "cloudnode-native":
		err = runCloudNodeNative(*nodeID, *upstream, *address, *ready, *nonces, *keyID, os.Getenv("MOOX_GATEWAY_E2E_SERVICE_SECRET"))
	case "collector-period-native":
		err = runCollectorPeriodNative(*routeScope, *upstream, *metadata, *nodeID, *address, *ready, *nonces, *keyID, os.Getenv("MOOX_GATEWAY_E2E_SERVICE_SECRET"))
	default:
		err = errors.New("unsupported native fixture mode")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runAccessNative uses the real router verification boundary. Access supplies
// its own internal signature; no synthetic caller or signing adapter is used.
func runAccessNative(hostID, storageAddress, runtimeAddress, listenAddress, readyFile, nonceDirectory, keyID, secret string) error {
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return err
	}
	var routes []gatewayroute.Route
	for _, selected := range []struct{ id, path, address string }{
		{"storage-primary", "trpc.moox.storage.PrimaryStore", storageAddress},
		{"collector", "trpc.moox.collector.MarketFetchRuntime", runtimeAddress},
	} {
		if selected.address == "" {
			continue
		}
		spec, _ := catalog.Service(selected.path)
		var methods []string
		for _, method := range spec.Methods {
			if catalog.Allowed("access", selected.path, method) {
				methods = append(methods, method)
			}
		}
		routes = append(routes, gatewayroute.Route{ServiceID: selected.id, ServicePath: selected.path, Address: selected.address, AllowedMethods: methods, AllowedCallers: []string{"access"}})
	}
	if len(routes) == 0 {
		return errors.New("Access fixture requires at least one upstream")
	}
	return runNativeRoutes(hostID, routes, "access", listenAddress, readyFile, nonceDirectory, keyID, secret)
}

func runKlineNative(nodeID, upstreamAddress, listenAddress, readyFile, nonceDirectory, keyID, secret string) error {
	return runNativeRoutes(nodeID, []gatewayroute.Route{{ServiceID: "storage-primary", Address: upstreamAddress, ServicePath: "trpc.moox.storage.PrimaryStore",
		AllowedMethods: []string{"ReadTimeSeriesRows", "UpsertFields"}, AllowedCallers: []string{"moox-cli"}}}, "moox-cli", listenAddress, readyFile, nonceDirectory, keyID, secret)
}

func runCloudNodeNative(nodeID, upstreamAddress, listenAddress, readyFile, nonceDirectory, keyID, secret string) error {
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return err
	}
	spec, _ := catalog.Service("trpc.moox.cloudnode.CloudNodeMgr")
	var methods []string
	for _, method := range spec.Methods {
		if catalog.Allowed("moox-cli", spec.Path, method) {
			methods = append(methods, method)
		}
	}
	return runNativeRoutes(nodeID, []gatewayroute.Route{{ServiceID: "cloudnode", Address: upstreamAddress, ServicePath: spec.Path, AllowedMethods: methods, AllowedCallers: []string{"moox-cli"}}}, "moox-cli", listenAddress, readyFile, nonceDirectory, keyID, secret)
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
	if caller == "access" {
		internal = incoming
	}
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
		if caller == "access" {
			return next(ctx, req)
		}
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
	if caller == "moox-cli" || caller == "access" {
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

func runCollectorPeriodNative(scope, upstreamAddress, metadataUpstreamAddress, nodeID, listenAddress, readyFile, nonceDirectory, keyID, secret string) error {
	routes, err := loadCollectorPeriodRoutesWithMetadata(scope, upstreamAddress, metadataUpstreamAddress)
	if err != nil {
		return err
	}
	return runNativeRoutes(nodeID, routes, "collector", listenAddress, readyFile, nonceDirectory, keyID, secret)
}

func loadCollectorPeriodRoutesWithMetadata(scope, upstreamAddress, metadataUpstreamAddress string) ([]gatewayroute.Route, error) {
	routes, err := loadCollectorPeriodRoutes(scope, upstreamAddress)
	if err != nil {
		return nil, err
	}
	if metadataUpstreamAddress == "" {
		return routes, nil
	}
	if scope != "storage-period" {
		return nil, errors.New("metadata-upstream-addr requires storage-period route-scope")
	}
	metadata, err := loadCollectorPeriodRoutes("storage-metadata", metadataUpstreamAddress)
	if err != nil {
		return nil, err
	}
	return append(routes, metadata...), nil
}

// Fixture routes use the current catalog and retain only the methods exercised
// by this scenario. There is no deployment seed or alternate ACL source.
func loadCollectorPeriodRoutes(scope, upstreamAddress string) ([]gatewayroute.Route, error) {
	host, port, err := net.SplitHostPort(upstreamAddress)
	number, portErr := strconv.Atoi(port)
	if err != nil || (host != "127.0.0.1" && host != "::1") || portErr != nil || number < 1 || number > 65535 {
		return nil, errors.New("upstream-addr must be a loopback host:port")
	}
	component, path := "storage-primary", "trpc.moox.storage.PrimaryStore"
	required := []string{"EnsureDatasetPeriod", "CommitTimeSeriesBatch", "RecordDatasetPeriodFailures", "GetDatasetPeriodStatus"}
	switch scope {
	case "storage-period":
	case "storage-metadata":
		path = "trpc.moox.storage.Metadata"
		required = []string{"ApplyTagSnapshot", "GetTag", "ListSubjects", "ResolveSubjects"}
	case "collector-runtime":
		component, path = "collector", "trpc.moox.collector.MarketFetchRuntime"
		required = []string{"ClaimTimerBatch"}
	default:
		return nil, errors.New("unsupported route-scope")
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return nil, err
	}
	service, ok := catalog.Service(path)
	if !ok {
		return nil, errors.New("period fixture service is missing from the catalog")
	}
	for _, method := range required {
		allowed := catalog.Allowed("collector", path, method)
		if scope == "collector-runtime" {
			allowed = catalog.PrincipalAllowed("scf-collector", path, method)
		}
		if !slices.Contains(service.Methods, method) || !allowed {
			return nil, errors.New("period fixture method is not allowed by the catalog")
		}
	}
	route := gatewayroute.Route{ServiceID: component, ServicePath: path, Address: upstreamAddress, AllowedMethods: required, AllowedCallers: []string{"collector"}, TimeoutMS: service.TimeoutMS, MaxBodyBytes: service.MaxBodyBytes}
	if _, err := gatewayroute.NormalizeAndHash("period-fixture", []gatewayroute.Route{route}); err != nil {
		return nil, err
	}
	return []gatewayroute.Route{route}, nil
}

func writeReadyFile(path, value string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(value), 0o600)
}

func runAdminNative(nodeID, upstreamAddress, listenAddress, readyFile, nonceDirectory, keyID, secret string) error {
	return runComponentsNative([]string{"admin"}, nodeID, upstreamAddress, listenAddress, readyFile, nonceDirectory, keyID, secret)
}

func runComponentsNative(components []string, nodeID, upstreamAddress, listenAddress, readyFile, nonceDirectory, keyID, secret string) error {
	caller := "moox-cli"
	if slices.Contains(components, "egress-proxy") {
		caller = "collector"
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return err
	}
	var routes []gatewayroute.Route
	for _, component := range catalog.Components {
		if !slices.Contains(components, component.ID) {
			continue
		}
		for _, spec := range component.Services {
			var methods []string
			for _, method := range spec.Methods {
				if catalog.Allowed(caller, spec.Path, method) {
					methods = append(methods, method)
				}
			}
			if len(methods) > 0 {
				routes = append(routes, gatewayroute.Route{ServiceID: component.ID, Address: upstreamAddress, ServicePath: spec.Path, AllowedMethods: methods, AllowedCallers: []string{caller}})
			}
		}
	}
	return runNativeRoutes(nodeID, routes, caller, listenAddress, readyFile, nonceDirectory, keyID, secret)
}
