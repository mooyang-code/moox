// Package listener opens the host gateway's remote TLS, local tRPC and health
// sockets together. Failed startup closes all sockets opened so far.
package listener

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/hostgateway/internal/tlsconfig"
	"github.com/mooyang-code/moox/packages/gatewayclient/relay"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

const handshakeTimeout = 5 * time.Second

type Listeners struct {
	Remote net.Listener
	Local  net.Listener
	Health net.Listener
	cancel context.CancelFunc
}

func Open(ctx context.Context, cfg hostgatewayconfig.Config, material *tlsconfig.Material) (_ *Listeners, resultErr error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if material == nil || material.HostID() != cfg.Host.ID {
		return nil, errors.New("listeners require validated TLS material for this host")
	}
	ctx, cancel := context.WithCancel(ctx)
	listeners := &Listeners{cancel: cancel}
	defer func() {
		if resultErr != nil {
			_ = listeners.Close()
		}
	}()
	var lc net.ListenConfig
	for _, endpoint := range []struct {
		name, address string
		destination   *net.Listener
	}{
		{"remote TLS", cfg.Server.RemoteAddr, &listeners.Remote},
		{"local tRPC", cfg.Server.LocalAddr, &listeners.Local},
		{"health", cfg.Server.HealthAddr, &listeners.Health},
	} {
		opened, err := lc.Listen(ctx, "tcp", endpoint.address)
		if err != nil {
			return nil, fmt.Errorf("listen %s endpoint: %w", endpoint.name, err)
		}
		*endpoint.destination = opened
	}
	listeners.Remote = newRemoteListener(listeners.Remote, material.Server(), ctx)
	return listeners, nil
}

// DrainTimeout 是停止时等待在途请求完成的上限，需小于运行器的停止预算（默认 2 分钟）。
const DrainTimeout = 100 * time.Second

// SetRelay 让跨主机端口把反向连接交给 handler（control 网关）；其他主机不调用，反向连接会被直接关闭。
func (l *Listeners) SetRelay(handler RelayHandler) {
	if remote, ok := l.Remote.(*remoteListener); ok {
		remote.SetRelay(handler)
	}
}

func (l *Listeners) Close() error {
	if l.cancel != nil {
		l.cancel()
	}
	var result error
	for _, opened := range []net.Listener{l.Remote, l.Local, l.Health} {
		if opened != nil {
			if err := opened.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				result = errors.Join(result, err)
			}
		}
	}
	return result
}

// TRPC consumes an already bound listener. Explicit go-net transport keeps the
// owned TLS listener in use on Linux, where the SDK defaults to tnet otherwise.
func TRPC(opened net.Listener, serviceName string, gates ...*healthz.DrainGate) server.Service {
	options := []server.Option{}
	for _, gate := range gates {
		options = append(options, server.WithFilter(gate.Filter()))
	}
	return server.New(append(options,
		// 关闭时停止接纳新连接，并等待已接纳的请求完成（写请求可能已在下游提交，不能直接断开）。
		server.WithMaxCloseWaitTime(DrainTimeout),
		server.WithTransport(transport.NewServerTransport()),
		server.WithListener(opened), server.WithAddress(opened.Addr().String()),
		server.WithNetwork("tcp"), server.WithProtocol("trpc"),
		server.WithServiceName(serviceName), server.WithCurrentSerializationType(codec.SerializationTypeNoop),
	)...)
}

// RelayHandler 接管已完成握手的反向连接；只有 control 网关提供。
type RelayHandler interface {
	HandleAttach(*tls.Conn)
	HandleOpen(*tls.Conn)
}

// maxPendingPerIP 限制单个来源 IP 同时未完成握手的连接数，防止慢速握手占满 control 的公网端口。
const maxPendingPerIP = 64

// remoteListener 在各自的 goroutine 里完成 TLS 握手（不串行化 Accept），再按 ALPN 分流：
// 普通连接交给 tRPC 的 Accept，反向连接交给 RelayHandler。tRPC 处理器接管不了 socket，所以分流必须在这一层。
type remoteListener struct {
	net.Listener
	config  *tls.Config
	ctx     context.Context
	once    sync.Once
	conns   chan net.Conn
	done    chan struct{}
	errOnce sync.Once
	err     error
	relay   atomic.Pointer[RelayHandler]
	mu      sync.Mutex
	pending map[string]int
}

func newRemoteListener(raw net.Listener, config *tls.Config, ctx context.Context) *remoteListener {
	return &remoteListener{Listener: raw, config: config, ctx: ctx, conns: make(chan net.Conn), done: make(chan struct{}), pending: map[string]int{}}
}

// SetRelay 必须在开始服务之前调用。
func (l *remoteListener) SetRelay(handler RelayHandler) { l.relay.Store(&handler) }

func (l *remoteListener) Accept() (net.Conn, error) {
	l.once.Do(func() { go l.acceptLoop() })
	select {
	case conn := <-l.conns:
		return conn, nil
	case <-l.done:
		return nil, l.err
	}
}

func (l *remoteListener) Close() error {
	err := l.Listener.Close()
	l.fail(net.ErrClosed)
	return err
}

func (l *remoteListener) fail(err error) {
	l.errOnce.Do(func() {
		l.err = err
		close(l.done)
	})
}

func (l *remoteListener) acceptLoop() {
	for {
		raw, err := l.Listener.Accept()
		if err != nil {
			var temporary interface{ Temporary() bool }
			if errors.As(err, &temporary) && temporary.Temporary() {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			l.fail(err)
			return
		}
		ip := remoteIP(raw)
		if !l.reserve(ip) {
			_ = raw.Close()
			continue
		}
		go l.dispatch(raw, ip)
	}
}

func remoteIP(conn net.Conn) string {
	host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		return conn.RemoteAddr().String()
	}
	return host
}

func (l *remoteListener) reserve(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pending[ip] >= maxPendingPerIP {
		return false
	}
	l.pending[ip]++
	return true
}

func (l *remoteListener) release(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pending[ip]--; l.pending[ip] <= 0 {
		delete(l.pending, ip)
	}
}

func (l *remoteListener) dispatch(raw net.Conn, ip string) {
	conn := tls.Server(raw, l.config)
	ctx, cancel := context.WithTimeout(l.ctx, handshakeTimeout)
	err := conn.HandshakeContext(ctx)
	cancel()
	l.release(ip)
	if err != nil {
		_ = conn.Close()
		return
	}
	protocol := conn.ConnectionState().NegotiatedProtocol
	if protocol == "" {
		select {
		case l.conns <- conn:
		case <-l.done:
			_ = conn.Close()
		}
		return
	}
	handler := l.relay.Load()
	if handler == nil {
		_ = conn.Close()
		return
	}
	switch protocol {
	case relay.ALPNAttach:
		(*handler).HandleAttach(conn)
	case relay.ALPNOpen:
		(*handler).HandleOpen(conn)
	default:
		_ = conn.Close()
	}
}
