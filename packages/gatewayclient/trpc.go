package gatewayclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayclient/relay"
	"github.com/mooyang-code/moox/packages/servicecatalog"

	_ "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/pool/connpool"
	"trpc.group/trpc-go/trpc-go/transport"
)

// 小于该长度的请求体压缩收益不抵 CPU 开销。
const minCompressBytes = 1024

// The SDK pool keys connections by address and protocol only. Partition its
// factories by TLS identity so a previously authenticated socket cannot bypass
// a different CA or server name on a later call to the same address.
type trustIdentity struct {
	ca, server, cert, key, provider, local string
}

type rpcPool struct {
	mu        sync.Mutex
	pools     map[trustIdentity]connpool.Pool
	conns     map[*trackedConn]struct{}
	relays    map[string]*relayRoute
	closed    bool
	transport transport.ClientTransport
	settings  poolSettings
	stats     poolCounters
}

// newRPCPool 创建连接池；不带参数时保持改动前的行为（用于只访问本机目录服务的来源）。
func newRPCPool(settings ...poolSettings) *rpcPool {
	pool := &rpcPool{pools: make(map[trustIdentity]connpool.Pool), conns: make(map[*trackedConn]struct{}), relays: make(map[string]*relayRoute), transport: transport.NewClientTransport(), settings: legacyPoolSettings()}
	if len(settings) > 0 {
		pool.settings = settings[0]
	}
	return pool
}

// route 登记合成地址对应的中转路线；control 地址随目录变化时覆盖旧值，已建立的连接不受影响。
func (p *rpcPool) route(address string, route *relayRoute) {
	p.mu.Lock()
	p.relays[address] = route
	p.mu.Unlock()
}

func (p *rpcPool) Get(network, address string, opts connpool.GetOptions) (net.Conn, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, net.ErrClosed
	}
	key := trustIdentity{opts.CACertFile, opts.TLSServerName, opts.TLSCertFile, opts.TLSKeyFile, opts.TLSCertProvider, opts.LocalAddr}
	pool := p.pools[key]
	if pool == nil {
		pool = connpool.NewConnectionPool(p.settings.options(p.dial)...)
		p.pools[key] = pool
	}
	p.mu.Unlock()
	// 连接池取走 opts.Ctx，先记下本次调用的状态；获取连接失败意味着请求尚未发出。
	ctx := opts.Ctx
	conn, err := pool.Get(network, address, opts)
	if err != nil {
		markConnectFailure(ctx)
		return nil, err
	}
	return conn, nil
}

func (p *rpcPool) dial(opts *connpool.DialOptions) (net.Conn, error) {
	p.mu.Lock()
	route := p.relays[opts.Address]
	p.mu.Unlock()
	// 连接池后台补连接（MinIdle）固定使用 200ms 拨号超时，跨境 TLS 握手经常超过它，在这里抬到后台拨号超时。
	background := p.settings.minIdle > 0 && opts.Timeout > 0 && opts.Timeout <= backgroundDialThreshold
	if background {
		opts.Timeout = backgroundDialTimeout
	}
	var conn net.Conn
	var err error
	if route != nil {
		conn, err = dialRelay(opts, route)
	} else {
		conn, err = connpool.Dial(opts)
	}
	p.stats.record(background, err == nil)
	if err != nil {
		return nil, err
	}
	tracked := &trackedConn{Conn: conn, owner: p}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = conn.Close()
		return nil, net.ErrClosed
	}
	p.conns[tracked] = struct{}{}
	p.mu.Unlock()
	return tracked, nil
}

func (p *rpcPool) Close() error {
	p.mu.Lock()
	p.closed = true
	conns := make([]*trackedConn, 0, len(p.conns))
	for conn := range p.conns {
		conns = append(conns, conn)
	}
	p.pools = nil
	p.mu.Unlock()
	var err error
	for _, conn := range conns {
		err = errors.Join(err, conn.Close())
	}
	return err
}

type trackedConn struct {
	net.Conn
	owner *rpcPool
	once  sync.Once
	err   error
}

func (c *trackedConn) Close() error {
	c.once.Do(func() {
		c.err = c.Conn.Close()
		c.owner.mu.Lock()
		delete(c.owner.conns, c)
		c.owner.mu.Unlock()
	})
	return c.err
}

func (p *rpcPool) invoke(ctx context.Context, ep endpoint, service, method string, serialization int, body []byte, headers http.Header) ([]byte, error) {
	ctx, message := codec.WithNewMessage(ctx)
	defer codec.PutBackMessage(message)
	message.WithClientRPCName("/" + service + "/" + method)
	message.WithCalleeServiceName(service)
	message.WithCalleeMethod(method)
	message.WithSerializationType(serialization)
	options := []client.Option{
		client.WithTarget("ip://" + ep.address), client.WithNetwork("tcp"), client.WithProtocol("trpc"),
		client.WithServiceName(service), client.WithCalleeMethod(method),
		client.WithSerializationType(serialization), client.WithCurrentSerializationType(codec.SerializationTypeNoop),
		// Linux amd64 otherwise switches to tnet, which requires its own dialer
		// and cannot consume this owned go-net connection pool.
		client.WithTransport(p.transport), client.WithPool(p), client.WithMultiplexed(false),
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		dialTimeout := remaining
		if state := callStateFrom(ctx); state != nil && p.settings.fastConnect && !noRetry(ctx) {
			// 建连失败可以在预算内重试，所以单次拨号不能吃掉整个调用预算。
			dialTimeout = connectBudget(remaining, state.attempt)
			if dialTimeout <= 0 {
				dialTimeout = remaining
			}
		}
		options = append(options, client.WithTimeout(remaining), client.WithDialTimeout(dialTimeout))
	}
	if ep.compress && len(body) >= minCompressBytes {
		options = append(options, client.WithCompressType(codec.CompressTypeGzip))
	}
	if ep.relay != nil {
		p.route(ep.address, ep.relay)
	}
	if ep.caFile != "" {
		options = append(options, client.WithTLS("", "", ep.caFile, ep.serverName))
	}
	for key, values := range headers {
		options = append(options, client.WithMetaData(key, []byte(values[0])))
	}
	response := &codec.Body{}
	if err := client.DefaultClient.Invoke(ctx, &codec.Body{Data: body}, response, options...); err != nil {
		return nil, err
	}
	return response.Data, nil
}

// dialRelay 经 control 网关打开中转流，再在流上与目标网关做 TLS（ServerName 为目标主机 ID）。
// control 只转发密文；连接池按 TLS 身份与合成地址隔离，复用与只读重试语义与直连相同。
func dialRelay(opts *connpool.DialOptions, route *relayRoute) (net.Conn, error) {
	roots, err := loadRoots(route.caFile)
	if err != nil {
		return nil, err
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	stream, err := relay.Dial(ctx, relay.OpenOptions{
		Address: route.controlAddress, ControlHost: route.controlHost,
		TLS:         &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		Credentials: route.credentials, Target: relay.Open{HostID: route.target, Port: servicecatalog.HostGatewayRemotePort},
	})
	if err != nil {
		return nil, err
	}
	conn := tls.Client(stream, &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: route.target})
	if err := conn.HandshakeContext(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("relay TLS handshake with %s: %w", route.target, err)
	}
	return conn, nil
}

func loadRoots(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read MooX CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, errors.New("MooX CA file contains no certificates")
	}
	return roots, nil
}

func (c *poolCounters) record(background, ok bool) {
	switch {
	case background && ok:
		c.warmupDialsOK.Add(1)
	case background:
		c.warmupDialsErr.Add(1)
	case ok:
		c.requestDialsOK.Add(1)
	default:
		c.requestDialsErr.Add(1)
	}
}
