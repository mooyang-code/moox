package gatewayclient

import (
	"crypto/tls"
	"net"
	"syscall"

	"trpc.group/trpc-go/trpc-go/pool/connpool"
)

// NewConnectionPool 返回能检测失效 TLS 连接的 tRPC 连接池。
//
// tRPC 连接池在复用空闲连接前，通过 syscall.Conn 非阻塞读取底层套接字，检查对端是否已经关闭；
// *tls.Conn 不实现 syscall.Conn，检查被跳过，主机网关重启后旧连接仍会被复用，第一个请求必然失败。
// 这里的拨号与 tRPC 默认拨号相同，只是给 TLS 连接补上底层 TCP 连接的 syscall.Conn。
func NewConnectionPool() connpool.Pool {
	return connpool.NewConnectionPool(connpool.WithDialFunc(dialCheckable))
}

func dialCheckable(opts *connpool.DialOptions) (net.Conn, error) {
	conn, err := connpool.Dial(opts)
	if err != nil {
		return nil, err
	}
	if tlsConn, ok := conn.(*tls.Conn); ok {
		if raw, ok := tlsConn.NetConn().(syscall.Conn); ok {
			return &checkableTLSConn{Conn: tlsConn, raw: raw}, nil
		}
	}
	return conn, nil
}

// checkableTLSConn 是暴露底层套接字的 TLS 连接。空闲连接上不应有数据：对端关闭时读到 EOF，
// 对端发来 close_notify 时读到数据，两种情况连接池都会丢弃这条连接。
type checkableTLSConn struct {
	*tls.Conn
	raw syscall.Conn
}

// SyscallConn 返回底层 TCP 连接的原始套接字。
func (c *checkableTLSConn) SyscallConn() (syscall.RawConn, error) { return c.raw.SyscallConn() }
