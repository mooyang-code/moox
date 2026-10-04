package jetstream

import (
	"context"

	"github.com/nats-io/nats.go"
)

func (c *Client) coreConnection() (*nats.Conn, error) {
	if c == nil {
		return nil, ErrConnection
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.closed || c.nc == nil || c.nc.IsClosed() {
		return nil, ErrClosed
	}
	return c.nc, nil
}

// RequestWithContext uses the existing authenticated connection for a
// non-persistent request. Callers must reconcile or retry lost responses.
func (c *Client) RequestWithContext(ctx context.Context, subject string, payload []byte) (*nats.Msg, error) {
	nc, err := c.coreConnection()
	if err != nil {
		return nil, err
	}
	return nc.RequestWithContext(ctx, subject, payload)
}

// Subscribe registers a Core NATS service on this connection. The subscription
// survives automatic NATS reconnects, but not an explicit Client.Reconnect;
// services should use a dedicated Client and own their subscription lifecycle.
func (c *Client) Subscribe(subject string, handler nats.MsgHandler) (*nats.Subscription, error) {
	nc, err := c.coreConnection()
	if err != nil {
		return nil, err
	}
	return nc.Subscribe(subject, handler)
}

func (c *Client) FlushWithContext(ctx context.Context) error {
	nc, err := c.coreConnection()
	if err != nil {
		return err
	}
	return nc.FlushWithContext(ctx)
}

func (c *Client) MaxPayload() int64 {
	nc, err := c.coreConnection()
	if err != nil {
		return 0
	}
	return nc.MaxPayload()
}
