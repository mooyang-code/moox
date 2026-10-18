package adminclient

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mooyang-code/moox/packages/gatewayclient"
	"trpc.group/trpc-go/trpc-go/codec"
)

// GatewayForwarder is borrowed from the command-owned SSH gateway client.
type GatewayForwarder interface {
	Forward(context.Context, string, string, int, []byte) ([]byte, error)
}

// CallGatewayJSON forwards native JSON on the command-owned tunnel.
func (c *Client) CallGatewayJSON(ctx context.Context, service, method string, body, response any) error {
	raw, err := c.gatewayJSON(ctx, service, method, body)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, response); err != nil {
		return fmt.Errorf("decode %s response: %w", service, err)
	}
	return nil
}

func (c *Client) gatewayJSON(ctx context.Context, service, method string, body any) ([]byte, error) {
	if c == nil || c.Gateway == nil {
		return nil, fmt.Errorf("%s requires the operator's SSH gateway client", service)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	metadata := gatewayclient.CallMetadataFromContext(ctx)
	metadata.SpaceID = c.SpaceID
	return c.Gateway.Forward(gatewayclient.WithCallMetadata(ctx, metadata), service, method, codec.SerializationTypeJSON, raw)
}

func (c *Client) callCollectorJSON(ctx context.Context, method string, body, response any) error {
	return c.CallGatewayJSON(ctx, "trpc.moox.collector.CollectMgr", method, body, response)
}

func (c *Client) callCloudNodeJSON(ctx context.Context, method string, body any) ([]byte, error) {
	return c.gatewayJSON(ctx, "trpc.moox.cloudnode.CloudNodeMgr", method, body)
}
