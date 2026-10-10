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
	"time"

	"github.com/mooyang-code/moox/modules/hostgateway/internal/tlsconfig"
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
	listeners.Remote = &tlsListener{Listener: listeners.Remote, config: material.Server(), ctx: ctx}
	return listeners, nil
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
func TRPC(opened net.Listener, serviceName string) server.Service {
	return server.New(
		server.WithTransport(transport.NewServerTransport()),
		server.WithListener(opened), server.WithAddress(opened.Addr().String()),
		server.WithNetwork("tcp"), server.WithProtocol("trpc"),
		server.WithServiceName(serviceName), server.WithCurrentSerializationType(codec.SerializationTypeNoop),
	)
}

type tlsListener struct {
	net.Listener
	config *tls.Config
	ctx    context.Context
}

func (l *tlsListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &handshakeConn{Conn: tls.Server(conn, l.config), ctx: l.ctx}, nil
}

// The SDK sets its own idle read deadline. HandshakeContext adds a separate
// bound for a stalled TLS handshake without serializing the listener's Accept.
type handshakeConn struct {
	*tls.Conn
	ctx  context.Context
	once sync.Once
	err  error
}

func (c *handshakeConn) handshake() error {
	c.once.Do(func() {
		ctx, cancel := context.WithTimeout(c.ctx, handshakeTimeout)
		defer cancel()
		c.err = c.HandshakeContext(ctx)
		if c.err != nil {
			_ = c.Conn.Close()
		}
	})
	return c.err
}

func (c *handshakeConn) Read(p []byte) (int, error) {
	if err := c.handshake(); err != nil {
		return 0, err
	}
	return c.Conn.Read(p)
}

func (c *handshakeConn) Write(p []byte) (int, error) {
	if err := c.handshake(); err != nil {
		return 0, err
	}
	return c.Conn.Write(p)
}
