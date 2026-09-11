package jetstream

import (
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

type stubConn struct {
	closed bool
}

func (c *stubConn) Read([]byte) (int, error)         { return 0, net.ErrClosed }
func (c *stubConn) Write([]byte) (int, error)        { return 0, net.ErrClosed }
func (c *stubConn) Close() error                     { c.closed = true; return nil }
func (c *stubConn) LocalAddr() net.Addr              { return stubAddr{} }
func (c *stubConn) RemoteAddr() net.Addr             { return stubAddr{} }
func (c *stubConn) SetDeadline(time.Time) error      { return nil }
func (c *stubConn) SetReadDeadline(time.Time) error  { return nil }
func (c *stubConn) SetWriteDeadline(time.Time) error { return nil }

type stubAddr struct{}

func (stubAddr) Network() string { return "tcp" }
func (stubAddr) String() string  { return "127.0.0.1:0" }

func TestTrackingDialerClosesPreviousConnection(t *testing.T) {
	first := &stubConn{}
	second := &stubConn{}
	var dials int
	dialer := &trackingDialer{
		dial: func(string, string) (net.Conn, error) {
			dials++
			if dials == 1 {
				return first, nil
			}
			return second, nil
		},
	}
	got, err := dialer.Dial("tcp", "127.0.0.1:4222")
	if err != nil || got != first {
		t.Fatalf("first dial = %v, %v", got, err)
	}
	got, err = dialer.Dial("tcp", "127.0.0.1:4222")
	if err != nil || got != second {
		t.Fatalf("second dial = %v, %v", got, err)
	}
	if !first.closed {
		t.Fatal("previous NATS TCP connection was not closed before the next dial")
	}
	if second.closed {
		t.Fatal("current NATS TCP connection was closed")
	}
}

func TestTrackingDialerClosesPreviousWhenDialFails(t *testing.T) {
	first := &stubConn{}
	var dials int
	dialer := &trackingDialer{
		dial: func(string, string) (net.Conn, error) {
			dials++
			if dials == 1 {
				return first, nil
			}
			return nil, net.ErrClosed
		},
	}
	if _, err := dialer.Dial("tcp", "127.0.0.1:4222"); err != nil {
		t.Fatalf("first dial: %v", err)
	}
	if _, err := dialer.Dial("tcp", "127.0.0.1:4222"); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("second dial error = %v, want net.ErrClosed", err)
	}
	if !first.closed {
		t.Fatal("previous NATS TCP connection was not closed after a failed dial")
	}
}

func TestTrackingDialerIsConcurrencySafe(t *testing.T) {
	dialer := &trackingDialer{
		dial: func(string, string) (net.Conn, error) {
			return &stubConn{}, nil
		},
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := dialer.Dial("tcp", "127.0.0.1:4222")
			if err != nil {
				t.Errorf("dial: %v", err)
				return
			}
			_ = conn.Close()
		}()
	}
	wg.Wait()
}
