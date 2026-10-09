package adminclient

import (
	"context"
	"encoding/json"
	"fmt"

	"trpc.group/trpc-go/trpc-go/codec"
)

// GatewayForwarder is borrowed from the command-owned SSH gateway client.
type GatewayForwarder interface {
	Forward(context.Context, string, string, int, []byte) ([]byte, error)
}

func (c *Client) callCollectorJSON(ctx context.Context, method string, body, response any) error {
	if c == nil || c.CollectorGateway == nil {
		return fmt.Errorf("Collector requires the operator's SSH gateway client")
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	raw, err = c.CollectorGateway.Forward(ctx, "trpc.moox.collector.CollectMgr", method, codec.SerializationTypeJSON, raw)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, response); err != nil {
		return fmt.Errorf("decode Collector response: %w", err)
	}
	return nil
}
