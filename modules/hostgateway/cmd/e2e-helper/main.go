// Command e2e-helper 给跨模块测试起一个只有路由层的主机网关：验签、防重放、调用名单都走真实代码，
// 上游地址换成测试给的回环地址。测试调用方的公钥从环境变量 MOOX_GATEWAY_E2E_PUBLIC_KEY 读（base64）；
// moox-cli 按非主机身份登记，其余调用方按“部署在本机、用本机的主机密钥签名”登记。生产不用它。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"time"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/directory"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/router"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/snapshot"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayroute"
	directorypb "github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

func main() {
	mode := flag.String("mode", "kline-native", "access-native, kline-native, storage-native, egress-native, doctor-native or admin-native")
	nodeID := flag.String("node-id", "", "test host ID")
	upstream := flag.String("upstream-addr", "", "loopback fixture upstream")
	address := flag.String("listen-addr", "127.0.0.1:0", "fixture listener")
	ready := flag.String("ready-file", "", "fixture readiness file")
	flag.Parse()
	var err error
	switch *mode {
	case "access-native":
		err = runAccessNative(*nodeID, *upstream, *address, *ready, os.Getenv("MOOX_GATEWAY_E2E_PUBLIC_KEY"))
	case "kline-native":
		err = runKlineNative(*nodeID, *upstream, *address, *ready, os.Getenv("MOOX_GATEWAY_E2E_PUBLIC_KEY"))
	case "storage-native":
		err = runComponentsNative([]string{"storage-primary", "storage-view"}, *nodeID, *upstream, *address, *ready, os.Getenv("MOOX_GATEWAY_E2E_PUBLIC_KEY"))
	case "egress-native":
		err = runComponentsNative([]string{"egress-proxy"}, *nodeID, *upstream, *address, *ready, os.Getenv("MOOX_GATEWAY_E2E_PUBLIC_KEY"))
	case "doctor-native":
		err = runComponentsNative([]string{"admin", "monitor"}, *nodeID, *upstream, *address, *ready, os.Getenv("MOOX_GATEWAY_E2E_PUBLIC_KEY"))
	case "admin-native":
		err = runAdminNative(*nodeID, *upstream, *address, *ready, os.Getenv("MOOX_GATEWAY_E2E_PUBLIC_KEY"))
	default:
		err = errors.New("unsupported native fixture mode")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runAccessNative(hostID, storageAddress, listenAddress, readyFile, publicKey string) error {
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return err
	}
	var routes []gatewayroute.Route
	for _, selected := range []struct{ id, path, address string }{
		{"storage-primary", "trpc.moox.storage.PrimaryStore", storageAddress},
	} {
		if selected.address == "" {
			continue
		}
		spec, _ := catalog.Service(selected.path)
		var methods []string
		for _, method := range spec.MethodNames() {
			if catalog.Allowed("access", selected.path, method) {
				methods = append(methods, method)
			}
		}
		routes = append(routes, gatewayroute.Route{ServiceID: selected.id, ServicePath: selected.path, Address: selected.address, AllowedMethods: methods, AllowedCallers: []string{"access"}})
	}
	if len(routes) == 0 {
		return errors.New("Access fixture requires at least one upstream")
	}
	return runNativeRoutes(hostID, routes, "access", listenAddress, readyFile, publicKey)
}

func runKlineNative(nodeID, upstreamAddress, listenAddress, readyFile, publicKey string) error {
	return runNativeRoutes(nodeID, []gatewayroute.Route{{ServiceID: "storage-primary", Address: upstreamAddress, ServicePath: "trpc.moox.storage.PrimaryStore",
		AllowedMethods: []string{"ReadTimeSeriesRows", "UpsertFields"}, AllowedCallers: []string{"moox-cli"}}}, "moox-cli", listenAddress, readyFile, publicKey)
}

type fixtureForwarder struct {
	upstream  *router.Upstream
	addresses map[string]string
}

func (f fixtureForwarder) Forward(ctx context.Context, route servicecatalog.Route, serialization int, body []byte, metadata codec.MetaData) ([]byte, error) {
	route.Address = f.addresses[route.ServicePath]
	return f.upstream.Forward(ctx, route, serialization, body, metadata)
}

func runNativeRoutes(hostID string, routes []gatewayroute.Route, caller, listenAddress, readyFile, publicKey string) error {
	if !servicecatalog.ValidHostID(hostID) {
		return errors.New("native gateway identity: canonical host ID required")
	}
	public, err := gatewayauth.ParsePublicKey(publicKey)
	if err != nil {
		return fmt.Errorf("native gateway identity: %w", err)
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return err
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
		limit := service.MaxBodyBytes
		if limit == 0 {
			limit = servicecatalog.DefaultMaxBodyBytes
		}
		for _, method := range selected.AllowedMethods {
			if !catalog.Allowed(caller, service.Path, method) {
				return errors.New("fixture caller/method absent from catalog")
			}
			raw.Routes = append(raw.Routes, &pb.HostGatewayRoute{ComponentId: componentID, ServicePath: service.Path, Method: method,
				Address: net.JoinHostPort("127.0.0.1", strconv.Itoa(service.Port)), TimeoutMs: service.TimeoutFor(method), MaxBodyBytes: limit, ReadOnly: catalog.ReadOnly(service.Path, method), Callers: []string{caller}})
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
	if caller == servicecatalog.OperatorMooxCLI {
		raw.VerificationKeys = []*pb.GatewayVerificationKey{{Identity: caller, PublicKey: public}}
	} else {
		raw.VerificationKeys = []*pb.GatewayVerificationKey{{HostId: hostID, PublicKey: public, Callers: []string{caller}}}
	}
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
	// 测试夹具一启动就会收到请求：把“进程启动时间”往前放，不拒绝启动那一秒里签的请求（生产网关等过这一秒才监听）。
	upstream := router.NewUpstream()
	defer upstream.Close()
	proxy, err := router.NewService(router.ServiceOptions{State: state, Replay: gatewayauth.NewReplay(time.Now().Add(-time.Minute)), Forwarder: fixtureForwarder{upstream: upstream, addresses: addresses}})
	if err != nil {
		return err
	}
	defer proxy.Close()
	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return err
	}
	defer listener.Close()
	service := server.New(server.WithTransport(transport.NewServerTransport()), server.WithListener(listener), server.WithAddress(listener.Addr().String()),
		server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithServiceName("trpc.moox.hostgateway.Fixture"),
		server.WithCurrentSerializationType(codec.SerializationTypeNoop))
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

func writeReadyFile(path, value string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(value), 0o600)
}

func runAdminNative(nodeID, upstreamAddress, listenAddress, readyFile, publicKey string) error {
	return runComponentsNative([]string{"admin"}, nodeID, upstreamAddress, listenAddress, readyFile, publicKey)
}

func runComponentsNative(components []string, nodeID, upstreamAddress, listenAddress, readyFile, publicKey string) error {
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
			for _, method := range spec.MethodNames() {
				if catalog.Allowed(caller, spec.Path, method) {
					methods = append(methods, method)
				}
			}
			if len(methods) > 0 {
				routes = append(routes, gatewayroute.Route{ServiceID: component.ID, Address: upstreamAddress, ServicePath: spec.Path, AllowedMethods: methods, AllowedCallers: []string{caller}})
			}
		}
	}
	return runNativeRoutes(nodeID, routes, caller, listenAddress, readyFile, publicKey)
}
