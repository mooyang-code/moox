package gatewayclient

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	_ "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/pool/connpool"
	"trpc.group/trpc-go/trpc-go/transport"
)

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
	closed    bool
	transport transport.ClientTransport
}

func newRPCPool() *rpcPool {
	return &rpcPool{pools: make(map[trustIdentity]connpool.Pool), conns: make(map[*trackedConn]struct{}), transport: transport.NewClientTransport()}
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
		pool = connpool.NewConnectionPool(connpool.WithDialFunc(p.dial), connpool.WithMaxIdle(8))
		p.pools[key] = pool
	}
	p.mu.Unlock()
	return pool.Get(network, address, opts)
}

func (p *rpcPool) dial(opts *connpool.DialOptions) (net.Conn, error) {
	conn, err := connpool.Dial(opts)
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
		options = append(options, client.WithTimeout(remaining), client.WithDialTimeout(remaining))
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
