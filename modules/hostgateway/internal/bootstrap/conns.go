package bootstrap

import (
	"net"
	"sync"
)

// trackedListener 记录已接受的连接，关闭网关时一并断开。tRPC 服务关闭时只停止接受新连接，
// 空闲连接上的下一个请求仍会交给已经关闭的网关处理。
type trackedListener struct {
	net.Listener
	mu     sync.Mutex
	conns  map[*trackedConn]struct{}
	closed bool
}

func track(listener net.Listener) *trackedListener {
	return &trackedListener{Listener: listener, conns: map[*trackedConn]struct{}{}}
}

// Accept 接受并登记连接；关闭之后接受到的连接立即断开。
func (l *trackedListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		_ = conn.Close()
		return nil, &net.OpError{Op: "accept", Net: "tcp", Addr: l.Addr(), Err: net.ErrClosed}
	}
	tracked := &trackedConn{Conn: conn, owner: l}
	l.conns[tracked] = struct{}{}
	return tracked, nil
}

// shutdown 关闭监听并断开全部已接受的连接，可以重复调用。
func (l *trackedListener) shutdown() {
	l.mu.Lock()
	l.closed = true
	conns := l.conns
	l.conns = map[*trackedConn]struct{}{}
	l.mu.Unlock()
	_ = l.Listener.Close()
	for conn := range conns {
		_ = conn.Conn.Close()
	}
}

type trackedConn struct {
	net.Conn
	owner *trackedListener
}

// Close 断开连接并撤销登记。
func (c *trackedConn) Close() error {
	c.owner.mu.Lock()
	delete(c.owner.conns, c)
	c.owner.mu.Unlock()
	return c.Conn.Close()
}
