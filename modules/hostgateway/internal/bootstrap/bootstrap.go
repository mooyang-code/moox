// Package bootstrap 组装主机网关：快照同步、跨主机入口（TLS）、本机入口与服务目录、健康端口。
package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/config"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/controlplane"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/directory"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/health"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/router"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/snapshot"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/store"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/tlsconfig"
	"github.com/mooyang-code/moox/packages/gatewayroute/proto/directorypb"
	"github.com/mooyang-code/moox/packages/healthz"
	_ "github.com/mooyang-code/moox/packages/healthz/trpcrecovery"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/filter"
	trpcserver "trpc.group/trpc-go/trpc-go/server"
)

type snapshotStore interface {
	Load() (*adminpb.HostSnapshot, error)
	Save(*adminpb.HostSnapshot) error
}

type controlClient interface {
	Pull(context.Context, string) (*adminpb.HostSnapshot, bool, error)
	Report(context.Context, controlplane.Status) error
}

// Options 是快照同步的依赖。
type Options struct {
	HostID              string
	Store               snapshotStore
	Control             controlClient
	Health              *health.State
	CertificateNotAfter time.Time
	Now                 func() time.Time
	Warn                func(string)
	SyncWarningAfter    time.Duration
	SyncWarningInterval time.Duration
}

// Runtime 维护当前快照：启动时先加载缓存，之后每 15 秒拉取一次。
type Runtime struct {
	hostID        string
	store         snapshotStore
	control       controlClient
	health        *health.State
	current       snapshot.Current
	certNotAfter  time.Time
	mu            sync.Mutex
	now           func() time.Time
	warn          func(string)
	warnAfter     time.Duration
	warnEvery     time.Duration
	failureSince  time.Time
	failureActive bool
	lastWarning   time.Time
}

// New 创建快照同步。
func New(options Options) *Runtime {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	warn := options.Warn
	if warn == nil {
		warn = func(message string) { log.Print(message) }
	}
	warnAfter := options.SyncWarningAfter
	if warnAfter <= 0 {
		warnAfter = 10 * time.Minute
	}
	warnEvery := options.SyncWarningInterval
	if warnEvery <= 0 {
		warnEvery = 10 * time.Minute
	}
	if options.Health != nil {
		options.Health.SetClock(now)
	}
	return &Runtime{
		hostID: options.HostID, store: options.Store, control: options.Control, health: options.Health,
		certNotAfter: options.CertificateNotAfter, now: now, warn: warn, warnAfter: warnAfter, warnEvery: warnEvery,
	}
}

// Current 返回当前快照的持有者，转发入口和 Directory 服务从这里读取。
func (r *Runtime) Current() *snapshot.Current { return &r.current }

// Initialize 先加载缓存，再拉取一次；拉取失败且没有可用缓存时返回错误。
func (r *Runtime) Initialize(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	hasCache := false
	if cached, err := r.store.Load(); err != nil {
		// 缓存不存在是正常的（首次启动）；其它错误（损坏、权限不对）要让运维看到，否则只会看到"没有可用缓存"。
		if !errors.Is(err, os.ErrNotExist) {
			r.warn(fmt.Sprintf("读取主机网关快照缓存失败，忽略: %v", err))
		}
	} else if applied, err := snapshot.Validate(r.hostID, cached); err == nil {
		r.apply(applied)
		hasCache = true
	} else {
		r.health.RouteValidationFailed()
		r.warn(fmt.Sprintf("主机网关快照缓存无效，忽略: %v", err))
	}
	if err := r.sync(ctx); err != nil {
		if hasCache {
			return nil
		}
		return fmt.Errorf("首次拉取快照失败且没有可用缓存: %w", err)
	}
	return nil
}

// Refresh 拉取一次快照并上报心跳，由路由刷新定时器每 15 秒调用。
func (r *Runtime) Refresh(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sync(ctx)
}

func (r *Runtime) sync(ctx context.Context) error {
	err := r.pull(ctx)
	lastError := ""
	if err != nil {
		lastError = err.Error()
		r.health.RouteSyncFailed()
		r.noteSyncFailure()
	} else {
		r.resetSyncFailure()
		r.health.RouteSyncSucceeded(r.now())
	}
	if reportErr := r.report(ctx, lastError); reportErr != nil && err == nil {
		return reportErr
	}
	return err
}

func (r *Runtime) pull(ctx context.Context) error {
	next, changed, err := r.control.Pull(ctx, r.current.Hash())
	if err != nil {
		return err
	}
	if !changed {
		if r.current.Load() == nil {
			return errors.New("网关控制报告快照没有变化，但本机还没有任何快照")
		}
		return nil
	}
	applied, err := snapshot.Validate(r.hostID, next)
	if err != nil {
		r.health.RouteValidationFailed()
		return err
	}
	if err := r.store.Save(applied.Proto); err != nil {
		return fmt.Errorf("保存快照缓存: %w", err)
	}
	r.apply(applied)
	return nil
}

func (r *Runtime) apply(applied *snapshot.Applied) {
	r.current.Store(applied, r.now())
	r.health.ApplyRoutes(applied.Hash, applied.Routes, applied.Disabled)
}

func (r *Runtime) report(ctx context.Context, lastError string) error {
	hash, count := r.health.Current()
	err := r.control.Report(ctx, controlplane.Status{
		AppliedHash: hash, RouteCount: int32(count), LastError: lastError, CertificateNotAfter: r.certNotAfter,
	})
	if err != nil {
		r.health.RouteReportFailed()
		r.warn(fmt.Sprintf("主机网关心跳上报失败: host_id=%s: %v", r.hostID, err))
		return err
	}
	r.health.RouteReportSucceeded(r.now())
	return nil
}

func (r *Runtime) noteSyncFailure() {
	now := r.now()
	if !r.failureActive {
		r.failureSince = now
		r.failureActive = true
		return
	}
	if now.Sub(r.failureSince) < r.warnAfter {
		return
	}
	if !r.lastWarning.IsZero() && now.Sub(r.lastWarning) < r.warnEvery {
		return
	}
	r.lastWarning = now
	r.warn(fmt.Sprintf("主机网关快照同步持续失败: host_id=%s 已持续 %s；继续使用缓存的快照，就绪状态降级",
		r.hostID, now.Sub(r.failureSince).Truncate(time.Second)))
}

func (r *Runtime) resetSyncFailure() {
	r.failureSince = time.Time{}
	r.failureActive = false
	r.lastWarning = time.Time{}
}

// Gateway 是组装好的主机网关：快照同步已完成首次拉取，三个监听都已绑定端口。
type Gateway struct {
	Runtime        *Runtime
	State          *health.State
	remote         trpcserver.Service
	local          trpcserver.Service
	remoteListener *trackedListener
	localListener  *trackedListener
	healthServer   *http.Server
	healthListener net.Listener
	nonces         *store.Nonces
	closeOnce      sync.Once
}

// NewGateway 读取证书与密钥、首次同步快照，并绑定跨主机入口、本机入口和健康端口。
func NewGateway(ctx context.Context, cfg config.Config, version string) (*Gateway, error) {
	serverTLS, err := tlsconfig.LoadServer(cfg.TLS.CertFile, cfg.TLS.KeyFile, cfg.TLS.CAFile, cfg.Host.ID, time.Now())
	if err != nil {
		return nil, err
	}
	instanceID, err := newInstanceID()
	if err != nil {
		return nil, err
	}
	control, err := controlplane.New(controlplane.Options{Config: cfg, InstanceID: instanceID, Version: version})
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.Store.Path, 0o700); err != nil {
		return nil, fmt.Errorf("创建主机网关数据目录: %w", err)
	}
	nonces, err := store.OpenNonces(filepath.Join(cfg.Store.Path, "nonces"))
	if err != nil {
		return nil, err
	}
	gateway := &Gateway{State: health.NewState(), nonces: nonces}
	fail := func(err error) (*Gateway, error) {
		gateway.Close()
		return nil, err
	}
	snapshots := store.NewSnapshots(cfg.Store.Path)
	gateway.Runtime = New(Options{HostID: cfg.Host.ID, Store: snapshots, Control: control, Health: gateway.State, CertificateNotAfter: serverTLS.NotAfter})
	if err := gateway.Runtime.Initialize(ctx); err != nil {
		return fail(err)
	}
	gateway.State.SetStorageCheck(func() error {
		if err := snapshots.Check(); err != nil {
			return err
		}
		return nonces.Check()
	})
	nativeDesc, nativeImpl := router.ServiceDesc(router.Options{HostID: cfg.Host.ID, Snapshot: gateway.Runtime.Current(), Nonces: nonces, Metrics: gateway.State})
	remoteListener, err := tlsconfig.Listen(cfg.Server.RemoteAddr, serverTLS.Config)
	if err != nil {
		return fail(fmt.Errorf("监听跨主机入口 %s: %w", cfg.Server.RemoteAddr, err))
	}
	gateway.remoteListener = track(remoteListener)
	gateway.remote = newGatewayService(cfg.Server.RemoteAddr, gateway.remoteListener)
	if err := gateway.remote.Register(nativeDesc, nativeImpl); err != nil {
		return fail(fmt.Errorf("注册跨主机入口: %w", err))
	}
	localListener, err := net.Listen("tcp", cfg.Server.LocalAddr)
	if err != nil {
		return fail(fmt.Errorf("监听本机入口 %s: %w", cfg.Server.LocalAddr, err))
	}
	gateway.localListener = track(localListener)
	gateway.local = newGatewayService(cfg.Server.LocalAddr, gateway.localListener)
	if err := gateway.local.Register(directorypb.NoopServiceDesc(), directorypb.DirectoryService(directory.New(cfg.Host.ID, gateway.Runtime.Current()))); err != nil {
		return fail(fmt.Errorf("注册服务目录: %w", err))
	}
	if err := gateway.local.Register(nativeDesc, nativeImpl); err != nil {
		return fail(fmt.Errorf("注册本机入口: %w", err))
	}
	gateway.healthListener, err = net.Listen("tcp", cfg.Server.HealthAddr)
	if err != nil {
		return fail(fmt.Errorf("监听健康端口 %s: %w", cfg.Server.HealthAddr, err))
	}
	healthHandler, err := healthz.WrapFromEnv(gateway.State.Handler())
	if err != nil {
		return fail(fmt.Errorf("配置健康端口鉴权: %w", err))
	}
	gateway.healthServer = &http.Server{
		Handler: healthHandler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second,
	}
	return gateway, nil
}

// Serve 服务三个监听，直到 ctx 结束或任一监听退出。
func (g *Gateway) Serve(ctx context.Context) error {
	results := make(chan serverResult, 3)
	go func() { results <- serverResult{name: "跨主机入口", err: g.remote.Serve()} }()
	go func() { results <- serverResult{name: "本机入口", err: g.local.Serve()} }()
	go func() {
		err := g.healthServer.Serve(g.healthListener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		results <- serverResult{name: "健康端口", err: err}
	}()
	var firstErr error
	completed := 0
	select {
	case <-ctx.Done():
	case result := <-results:
		completed = 1
		if result.err != nil && !errors.Is(result.err, context.Canceled) {
			firstErr = fmt.Errorf("%s退出: %w", result.name, result.err)
		} else {
			firstErr = fmt.Errorf("%s意外退出", result.name)
		}
	}
	shutdownCtx, cancel := context.WithTimeout(trpc.BackgroundContext(), 5*time.Second)
	defer cancel()
	shutdownErr := g.healthServer.Shutdown(shutdownCtx)
	// tRPC 服务关闭时等待进行中的请求（上限 gatewayCloseWait），之后断开剩余的连接。
	_ = g.remote.Close(make(chan struct{}, 1))
	_ = g.local.Close(make(chan struct{}, 1))
	g.remoteListener.shutdown()
	g.localListener.shutdown()
	for completed < 3 {
		<-results
		completed++
	}
	return errors.Join(firstErr, shutdownErr)
}

// Close 关闭监听和连接并释放 nonce 存储，可以重复调用。Serve 返回后调用；NewGateway 失败时自动调用。
func (g *Gateway) Close() {
	g.closeOnce.Do(func() {
		for _, listener := range []*trackedListener{g.remoteListener, g.localListener} {
			if listener != nil {
				listener.shutdown()
			}
		}
		if g.healthListener != nil {
			_ = g.healthListener.Close()
		}
		if g.nonces != nil {
			_ = g.nonces.Close()
		}
	})
}

// Run 启动主机网关和它的定时器，直到 ctx 结束或任一监听退出。
func Run(ctx context.Context, cfg config.Config, version string) error {
	gateway, err := NewGateway(ctx, cfg, version)
	if err != nil {
		return err
	}
	defer gateway.Close()
	timerServer := trpc.NewServer()
	if err := registerRouteRefreshTimer(timerServer, gateway.Runtime); err != nil {
		return err
	}
	if err := registerMetricsReporter(timerServer); err != nil {
		return err
	}
	timerDone := make(chan error, 1)
	go func() { timerDone <- timerServer.Serve() }()
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		if err := <-timerDone; err != nil {
			log.Printf("主机网关定时器退出: %v", err)
		}
		cancel()
	}()
	err = gateway.Serve(serveCtx)
	_ = timerServer.Close(nil)
	return err
}

// gatewayCloseWait 是停止时等待进行中的请求完成的上限，必须小于运行脚本给主机网关的停止超时（30 秒）。
const gatewayCloseWait = 10 * time.Second

func newGatewayService(address string, listener net.Listener) trpcserver.Service {
	return trpcserver.New(
		trpcserver.WithAddress(address), trpcserver.WithListener(listener), trpcserver.WithNetwork("tcp"),
		trpcserver.WithProtocol("trpc"), trpcserver.WithCurrentSerializationType(codec.SerializationTypeNoop),
		// 不解压请求：帧头里声明的压缩方式由未认证的调用方决定，tRPC 会在验签之前先解压，一个几十 KB 的压缩帧
		// 就能让进程分配几百 MB 内存。网关本来就只转发原始字节，gatewayclient 也不压缩。
		trpcserver.WithCurrentCompressType(codec.CompressTypeNoop),
		// 停止时先等进行中的请求完成再断开连接，否则调用方只看到断连，上游却把请求处理完。
		trpcserver.WithMaxCloseWaitTime(gatewayCloseWait),
		// panic 不能带走整台主机唯一的网关。
		trpcserver.WithFilter(filter.GetServer("recovery")),
		trpcserver.WithServiceName(router.ServiceName),
	)
}

func newInstanceID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("生成主机网关实例 ID: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

type serverResult struct {
	name string
	err  error
}
