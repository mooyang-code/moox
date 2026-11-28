// Command e2e-helper 给跨模块测试起一个只有路由层的主机网关：验签、防重放、调用名单、按部署算路由都走真实代码，
// 上游地址换成测试给的回环地址。测试调用方的公钥从环境变量 MOOX_GATEWAY_E2E_PUBLIC_KEY 读（base64）；
// moox-cli 与外部身份按非主机身份登记，其余调用方按“部署在本机、用本机的主机密钥签名”登记。
// entry-native 模式下本机登记成外部入口，监听上挂的是外部入口（生产里的 11004）。生产不用它。
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
	"syscall"
	"time"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/directory"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/router"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/snapshot"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

func main() {
	mode := flag.String("mode", "kline-native", "entry-native, kline-native, storage-native, doctor-native or admin-native")
	nodeID := flag.String("node-id", "", "test host ID")
	upstream := flag.String("upstream-addr", "", "loopback fixture upstream")
	address := flag.String("listen-addr", "127.0.0.1:0", "fixture listener")
	ready := flag.String("ready-file", "", "fixture readiness file")
	flag.Parse()
	// 每种模式是“本机部署了哪些组件”加“谁来调”：路由由网关按部署自己算。
	modes := map[string]struct {
		components []string
		caller     string
	}{
		"entry-native":   {[]string{"storage-primary"}, "scf-collector"},
		"kline-native":   {[]string{"storage-primary"}, servicecatalog.OperatorMooxCLI},
		"storage-native": {[]string{"storage-primary", "storage-view"}, servicecatalog.OperatorMooxCLI},
		"doctor-native":  {[]string{"admin", "monitor"}, servicecatalog.OperatorMooxCLI},
		"admin-native":   {[]string{"admin"}, servicecatalog.OperatorMooxCLI},
	}
	err := errors.New("unsupported native fixture mode")
	if selected, ok := modes[*mode]; ok {
		err = run(*nodeID, selected.components, selected.caller, *upstream, *address, *ready, os.Getenv("MOOX_GATEWAY_E2E_PUBLIC_KEY"))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type fixtureForwarder struct {
	upstream  *router.Upstream
	addresses map[string]string
}

func (f fixtureForwarder) Forward(ctx context.Context, route servicecatalog.Route, serialization int, body []byte, metadata codec.MetaData) ([]byte, error) {
	route.Address = f.addresses[route.ServicePath]
	return f.upstream.Forward(ctx, route, serialization, body, metadata)
}

func run(hostID string, components []string, caller, upstreamAddress, listenAddress, readyFile, publicKey string) error {
	if !servicecatalog.ValidHostID(hostID) {
		return errors.New("native gateway identity: canonical host ID required")
	}
	public, err := gatewayauth.ParsePublicKey(publicKey)
	if err != nil {
		return fmt.Errorf("native gateway identity: %w", err)
	}
	if host, _, err := net.SplitHostPort(upstreamAddress); err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("fixture upstream must be literal loopback")
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return err
	}
	raw := &pb.HostGatewaySnapshot{SchemaVersion: snapshot.SchemaVersion, Hosts: []*pb.SnapshotHost{{HostId: hostID, Address: "127.0.0.1", Reachability: servicecatalog.ReachabilityDirect, Enabled: true}}}
	addresses := map[string]string{}
	for _, id := range components {
		component, ok := catalog.Component(id)
		if !ok {
			return errors.New("fixture component absent from catalog")
		}
		raw.Deployments = append(raw.Deployments, &pb.SnapshotDeployment{HostId: hostID, ComponentId: id, Enabled: true})
		for _, service := range component.Services {
			addresses[service.Path] = upstreamAddress
		}
	}
	external := catalog.ExternalIdentity(caller)
	raw.Hosts[0].ExternalEntry = external
	if caller == servicecatalog.OperatorMooxCLI || external {
		raw.Identities = []*pb.SnapshotIdentity{{Identity: caller, SignPublicKey: public}}
	} else {
		// 调用方也算部署在本机，用本机的主机密钥签名。
		raw.Hosts[0].SignPublicKey = public
		raw.Deployments = append(raw.Deployments, &pb.SnapshotDeployment{HostId: hostID, ComponentId: caller, Enabled: true})
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
	register := proxy.Register
	if external {
		register = proxy.RegisterExternal
	}
	if err := register(service); err != nil {
		return err
	}
	if err := directory.Register(service, state); err != nil {
		return err
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
