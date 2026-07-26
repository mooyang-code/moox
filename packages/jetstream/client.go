package jetstream

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	trpc "trpc.group/trpc-go/trpc-go"
)

// Client owns a NATS connection and its JetStream context.
type Client struct {
	nc  *nats.Conn
	js  nats.JetStreamContext
	cfg Config

	mu     sync.RWMutex
	closed bool
}

func (c *Client) Ready() bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return !c.closed && c.nc != nil && c.nc.IsConnected()
}

// Connect establishes a NATS connection and creates a JetStream context.
func Connect(ctx context.Context, cfg Config) (*Client, error) {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConnection, err)
	}
	cfg = cfg.normalized()
	if (cfg.Credentials != "") && (cfg.Username != "" || cfg.Password != "") {
		return nil, fmt.Errorf("%w: configure either username/password or credentials, not both", ErrConnection)
	}
	if cfg.Password != "" && cfg.Username == "" {
		return nil, fmt.Errorf("%w: username is required when password is configured", ErrConnection)
	}
	if len(cfg.URLs) == 0 || strings.TrimSpace(strings.Join(cfg.URLs, ",")) == "" {
		return nil, fmt.Errorf("%w: at least one NATS URL is required", ErrConnection)
	}
	if cfg.TLSCAFile != "" && cfg.TLSCAPEMBase64 != "" {
		return nil, fmt.Errorf("%w: TLS CA file and embedded PEM are mutually exclusive", ErrConnection)
	}
	for _, rawURL := range cfg.URLs {
		parsed, err := url.Parse(strings.TrimSpace(rawURL))
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return nil, fmt.Errorf("%w: invalid NATS URL %q", ErrConnection, rawURL)
		}
		if !isLoopbackHost(parsed.Hostname()) && parsed.Scheme != "tls" {
			return nil, fmt.Errorf("%w: non-loopback NATS URL %q must use tls", ErrConnection, rawURL)
		}
		if !isLoopbackHost(parsed.Hostname()) && cfg.TLSCAFile == "" && cfg.TLSCAPEMBase64 == "" {
			return nil, fmt.Errorf("%w: non-loopback NATS URL %q requires TLS CA", ErrConnection, rawURL)
		}
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
	if cfg.TLSCAPEMBase64 != "" {
		pemBytes, err := base64.StdEncoding.DecodeString(cfg.TLSCAPEMBase64)
		if err != nil {
			return nil, fmt.Errorf("%w: decode TLS CA PEM: %w", ErrConnection, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pemBytes) {
			return nil, fmt.Errorf("%w: TLS CA PEM contains no certificates", ErrConnection)
		}
		opts = append(opts, nats.Secure(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}))
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
	reconnectBuffer := cfg.ReconnectBufferBytes
	if reconnectBuffer == 0 {
		reconnectBuffer = -1
	}
	opts = append(opts,
		nats.Timeout(cfg.ConnectTimeout),
		nats.RetryOnFailedConnect(true),
		nats.ReconnectWait(cfg.ReconnectWait),
		nats.MaxReconnects(cfg.MaxReconnects),
		nats.ReconnectBufSize(reconnectBuffer),
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

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(host)
	return host == "127.0.0.1" || host == "localhost" || host == "::1" || host == "[::1]"
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

func (c *Client) DeleteConsumer(ctx context.Context, stream, durable string) error {
	if err := c.alive(); err != nil {
		return err
	}
	if strings.TrimSpace(stream) == "" || strings.TrimSpace(durable) == "" {
		return fmt.Errorf("%w: stream and durable are required", ErrInvalidConsumer)
	}
	if err := c.js.DeleteConsumer(stream, durable, nats.Context(ctx)); err != nil && !errors.Is(err, nats.ErrConsumerNotFound) {
		return fmt.Errorf("%w: delete consumer %s/%s: %w", ErrInvalidConsumer, stream, durable, err)
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
