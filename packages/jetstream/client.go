package jetstream

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// Client owns a NATS connection and its JetStream context.
type Client struct {
	nc  *nats.Conn
	js  nats.JetStreamContext
	cfg Config

	mu     sync.RWMutex
	closed bool
}

// Connect establishes a NATS connection and creates a JetStream context.
func Connect(ctx context.Context, cfg Config) (*Client, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConnection, err)
	}
	cfg = cfg.normalized()
	if len(cfg.URLs) == 0 || strings.TrimSpace(strings.Join(cfg.URLs, ",")) == "" {
		return nil, fmt.Errorf("%w: at least one NATS URL is required", ErrConnection)
	}
	opts := make([]nats.Option, 0, 12)
	if cfg.Name != "" {
		opts = append(opts, nats.Name(cfg.Name))
	}
	if cfg.Username != "" || cfg.Password != "" {
		opts = append(opts, nats.UserInfo(cfg.Username, cfg.Password))
	}
	if cfg.Credentials != "" {
		opts = append(opts, nats.UserCredentials(cfg.Credentials))
	}
	if cfg.TLSCAFile != "" {
		opts = append(opts, nats.RootCAs(cfg.TLSCAFile))
	}
	if cfg.TLSCertFile != "" || cfg.TLSKeyFile != "" {
		if cfg.TLSCertFile == "" || cfg.TLSKeyFile == "" {
			return nil, fmt.Errorf("%w: TLS certificate and key must be configured together", ErrConnection)
		}
		opts = append(opts, nats.ClientCert(cfg.TLSCertFile, cfg.TLSKeyFile))
	}
	if cfg.TLSCAFile != "" || cfg.TLSCertFile != "" || cfg.TLSKeyFile != "" {
		opts = append(opts, nats.Secure())
	}
	opts = append(opts,
		nats.Timeout(cfg.ConnectTimeout),
		nats.RetryOnFailedConnect(true),
		nats.ReconnectWait(cfg.ReconnectWait),
		nats.MaxReconnects(cfg.MaxReconnects),
	)

	connectCtx := ctx
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > cfg.ConnectTimeout {
		var cancel context.CancelFunc
		connectCtx, cancel = context.WithTimeout(ctx, cfg.ConnectTimeout)
		defer cancel()
	}
	urls := strings.Join(cfg.URLs, ",")
	type result struct {
		nc  *nats.Conn
		err error
	}
	resultCh := make(chan result, 1)
	go func() {
		nc, err := nats.Connect(urls, opts...)
		if connectCtx.Err() != nil {
			if nc != nil {
				nc.Close()
			}
			return
		}
		select {
		case resultCh <- result{nc: nc, err: err}:
		case <-connectCtx.Done():
			if nc != nil {
				nc.Close()
			}
		}
	}()
	var res result
	select {
	case <-connectCtx.Done():
		return nil, fmt.Errorf("%w: %w", ErrConnection, connectCtx.Err())
	case res = <-resultCh:
	}
	if res.err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConnection, res.err)
	}
	if res.nc == nil || !res.nc.IsConnected() {
		status := "unknown"
		if res.nc != nil {
			status = res.nc.Status().String()
			res.nc.Close()
		}
		return nil, fmt.Errorf("%w: initial NATS connection is not ready (status=%s)", ErrConnection, status)
	}
	js, err := res.nc.JetStream()
	if err != nil {
		res.nc.Close()
		return nil, fmt.Errorf("%w: create jetstream context: %w", ErrConnection, err)
	}
	return &Client{nc: res.nc, js: js, cfg: cfg}, nil
}

// Close closes the client connection. Durable consumers are intentionally not deleted.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	nc := c.nc
	c.mu.Unlock()
	if nc != nil {
		nc.Close()
	}
	return nil
}

func (c *Client) alive() error {
	if c == nil {
		return ErrConnection
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed || c.nc == nil || c.js == nil {
		return ErrClosed
	}
	if c.nc.IsClosed() {
		return ErrClosed
	}
	return nil
}
