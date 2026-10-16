// Package rpcconn owns the lifecycle of tRPC's go-net pooled connections.
package rpcconn

import (
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"syscall"
	"time"

	"trpc.group/trpc-go/trpc-go/pool/connpool"
)

type Pool struct {
	pool        connpool.Pool
	tls         *tls.Config
	mu          sync.Mutex
	closed      bool
	connections map[*connection]struct{}
}

// Each pool has exactly one immutable trust identity. Pools are never shared
// between a TLS control connection and local plaintext upstream connections.
func New(config *tls.Config) *Pool {
	p := &Pool{tls: config, connections: map[*connection]struct{}{}}
	p.pool = connpool.NewConnectionPool(connpool.WithDialFunc(p.dial), connpool.WithMaxIdle(8))
	return p
}

func (p *Pool) Get(network, address string, opts connpool.GetOptions) (net.Conn, error) {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return nil, net.ErrClosed
	}
	return p.pool.Get(network, address, opts)
}

func (p *Pool) dial(opts *connpool.DialOptions) (net.Conn, error) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	dialer := &net.Dialer{Timeout: timeout}
	var raw net.Conn
	var err error
	if p.tls == nil {
		raw, err = dialer.Dial(opts.Network, opts.Address)
	} else {
		raw, err = tls.DialWithDialer(dialer, opts.Network, opts.Address, p.tls)
	}
	if err != nil {
		return nil, err
	}
	conn := &connection{Conn: raw, owner: p}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		raw.Close()
		return nil, net.ErrClosed
	}
	p.connections[conn] = struct{}{}
	p.mu.Unlock()
	// 明文 TCP 透出原始套接字：tRPC 连接池复用空闲连接前会对它做一次非阻塞读，
	// 发现对端已关闭（例如上游重启或空闲回收）就丢弃并重新建连，而不是把请求发到死连接上读到 EOF。
	// TLS 连接不透出：直接读底层套接字会吞掉 TLS 记录，只能依赖空闲超时回收和只读重试。
	if _, ok := raw.(syscall.Conn); ok && p.tls == nil {
		return plainConnection{conn}, nil
	}
	return conn, nil
}

func (p *Pool) Close() error {
	p.mu.Lock()
	p.closed = true
	owned := make([]*connection, 0, len(p.connections))
	for conn := range p.connections {
		owned = append(owned, conn)
	}
	p.mu.Unlock()
	var result error
	for _, conn := range owned {
		result = errors.Join(result, conn.Close())
	}
	return result
}

type connection struct {
	net.Conn
	owner *Pool
	once  sync.Once
	err   error
}

func (c *connection) Close() error {
	c.once.Do(func() {
		c.err = c.Conn.Close()
		c.owner.mu.Lock()
		delete(c.owner.connections, c)
		c.owner.mu.Unlock()
	})
	return c.err
}

// plainConnection 是明文连接：在 connection 的基础上实现 syscall.Conn，供连接池做存活探测。
type plainConnection struct {
	*connection
}

func (c plainConnection) SyscallConn() (syscall.RawConn, error) {
	return c.Conn.(syscall.Conn).SyscallConn()
}
