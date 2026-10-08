// Command e2e-helper 在临时的本机端口上启动真实的主机网关转发入口，供跨模块的集成测试使用。
//
//	e2e-helper -host-id <主机> -route <service path>=<上游地址> [-route ...] -callers a,b [-methods m1,m2]
//	           -ready-file <文件> -nonce-dir <目录> [-listen-addr 127.0.0.1:0]
//
// 调用方密钥从环境变量 MOOX_GATEWAY_E2E_KEYS 读取，格式为 caller:key_id:secret，多个用逗号分隔。
// 没有给出 -methods 时，路由放行组件目录允许这些调用方调用的全部方法。
package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mooyang-code/moox/modules/hostgateway/internal/router"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/snapshot"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/store"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testsnapshot"
	"github.com/mooyang-code/moox/packages/gatewayroute"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
)

type routeFlags []string

func (r *routeFlags) String() string { return strings.Join(*r, ",") }

func (r *routeFlags) Set(value string) error {
	*r = append(*r, value)
	return nil
}

func main() {
	var routes routeFlags
	hostID := flag.String("host-id", "", "主机 ID")
	flag.Var(&routes, "route", "<service path>=<上游 host:port>，可重复")
	callers := flag.String("callers", "", "路由放行的调用方，逗号分隔")
	methods := flag.String("methods", "", "路由放行的方法，逗号分隔；默认取组件目录允许这些调用方调用的方法")
	listenAddress := flag.String("listen-addr", "127.0.0.1:0", "监听地址")
	readyFile := flag.String("ready-file", "", "就绪后写入 ip://<地址> 的文件")
	nonceDirectory := flag.String("nonce-dir", "", "nonce 存储目录")
	flag.Parse()
	if err := run(*hostID, routes, *callers, *methods, *listenAddress, *readyFile, *nonceDirectory, os.Getenv("MOOX_GATEWAY_E2E_KEYS")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(hostID string, routeSpecs []string, callerList, methodList, listenAddress, readyFile, nonceDirectory, keyList string) error {
	routes, services, err := buildRoutes(routeSpecs, splitList(callerList), splitList(methodList))
	if err != nil {
		return err
	}
	keys, err := parseKeys(keyList)
	if err != nil {
		return err
	}
	built, err := testsnapshot.Build(hostID, false, routes, keys, testsnapshot.Directory(hostID, services...))
	if err != nil {
		return err
	}
	applied, err := snapshot.Validate(hostID, built)
	if err != nil {
		return err
	}
	var current snapshot.Current
	current.Store(applied, time.Now())
	nonces, err := store.OpenNonces(nonceDirectory)
	if err != nil {
		return err
	}
	defer nonces.Close()
	desc, implementation := router.ServiceDesc(router.Options{HostID: hostID, Snapshot: &current, Nonces: nonces})
	listener, err := net.Listen("tcp", strings.TrimSpace(listenAddress))
	if err != nil {
		return err
	}
	service := server.New(
		server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithServiceName(router.ServiceName),
		server.WithListener(listener), server.WithCurrentSerializationType(codec.SerializationTypeNoop),
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
		_ = service.Close(nil)
		select {
		case err := <-done:
			return err
		case <-time.After(3 * time.Second):
			return errors.New("主机网关没有停止")
		}
	case err := <-done:
		return err
	}
}

func buildRoutes(specs, callers, methods []string) ([]gatewayroute.Route, []string, error) {
	if len(specs) == 0 {
		return nil, nil, errors.New("至少需要一个 -route")
	}
	if len(callers) == 0 {
		return nil, nil, errors.New("-callers 不能为空")
	}
	catalog := servicecatalog.Default()
	var routes []gatewayroute.Route
	var services []string
	for _, spec := range specs {
		path, address, ok := strings.Cut(strings.TrimSpace(spec), "=")
		if !ok {
			return nil, nil, fmt.Errorf("-route %q 必须是 <service path>=<host:port>", spec)
		}
		host, _, err := net.SplitHostPort(address)
		if err != nil || (host != "127.0.0.1" && host != "::1") {
			return nil, nil, fmt.Errorf("-route %q 的上游必须是本机回环地址 host:port", spec)
		}
		service, component, known := catalog.Service(path)
		if !known {
			return nil, nil, fmt.Errorf("服务 %s 不在组件目录中", path)
		}
		routeMethods := methods
		if len(routeMethods) == 0 {
			// 与网关控制编译路由的规则一致：只放行组件目录允许这些调用方调用的方法。
			for _, rpc := range service.RPCs {
				for _, caller := range callers {
					if catalog.Allowed(caller, path, rpc) {
						routeMethods = append(routeMethods, rpc)
						break
					}
				}
			}
			if len(routeMethods) == 0 {
				return nil, nil, fmt.Errorf("组件目录不允许 %s 调用 %s 的任何方法，请用 -methods 指定", strings.Join(callers, ","), path)
			}
		}
		routes = append(routes, gatewayroute.Route{
			ServiceID: component.ID, Address: address, ServicePath: path, TimeoutMS: service.TimeoutMS,
			MaxBodyBytes: service.MaxBodyBytes, AllowedMethods: routeMethods, AllowedCallers: callers,
		})
		services = append(services, path)
	}
	return routes, services, nil
}

func parseKeys(raw string) ([]gatewayroute.VerificationKey, error) {
	var keys []gatewayroute.VerificationKey
	for _, item := range splitList(raw) {
		parts := strings.SplitN(item, ":", 3)
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return nil, fmt.Errorf("MOOX_GATEWAY_E2E_KEYS 中的 %q 必须是 caller:key_id:secret", item)
		}
		keys = append(keys, gatewayroute.VerificationKey{Caller: parts[0], KeyID: parts[1], Secret: parts[2]})
	}
	if len(keys) == 0 {
		return nil, errors.New("MOOX_GATEWAY_E2E_KEYS 不能为空")
	}
	return keys, nil
}

func splitList(raw string) []string {
	var out []string
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func writeReadyFile(path, value string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(value), 0o600)
}
