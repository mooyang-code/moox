package jetstream

import (
	"net"
	"sync"
	"time"
)

// trackingDialer closes the previous TCP connection before dialing a new one.
// nats.go's reconnect loop can replace nc.conn after a failed CONNECT/INFO
// handshake without closing the prior socket, which leaves client CLOSE-WAIT
// and EventBus FIN-WAIT-2 until tcp_fin_timeout.
type trackingDialer struct {
	dial func(network, address string) (net.Conn, error)
	mu   sync.Mutex
	prev net.Conn
}

func newTrackingDialer(timeout time.Duration) *trackingDialer {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	inner := net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	return &trackingDialer{
		dial: func(network, address string) (net.Conn, error) {
			return inner.Dial(network, address)
		},
	}
}

func (d *trackingDialer) Dial(network, address string) (net.Conn, error) {
	if d == nil || d.dial == nil {
		return nil, net.ErrClosed
	}
	d.mu.Lock()
	old := d.prev
	d.prev = nil
	d.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	conn, err := d.dial(network, address)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.prev = conn
	d.mu.Unlock()
	return conn, nil
}
