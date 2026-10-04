package gatewayauth

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/filter"
	"trpc.group/trpc-go/trpc-go/transport"
)

// NewTRPCClientFilter signs the serialized request body before it crosses the
// native Node Service Gateway. The generated Storage clients remain unchanged;
// only their target and this filter are supplied by the caller.
func NewTRPCClientFilter(credentials Credentials, targetNode string, now func() time.Time) filter.ClientFilter {
	if now == nil {
		now = time.Now
	}
	return func(ctx context.Context, req, rsp interface{}, next filter.ClientHandleFunc) error {
		msg := codec.Message(ctx)
		if msg == nil {
			return fmt.Errorf("gateway tRPC client message is missing")
		}
		body, err := codec.Marshal(msg.SerializationType(), req)
		if err != nil {
			return fmt.Errorf("marshal gateway tRPC request: %w", err)
		}
		path := msg.ClientRPCName()
		if path == "" {
			path = "/" + strings.TrimPrefix(msg.CalleeServiceName(), "/") + "/" + msg.CalleeMethod()
		}
		headers, err := Sign(credentials, Request{Method: http.MethodPost, Path: path, TargetNode: targetNode, Caller: credentials.Caller, Callee: msg.CalleeServiceName(), Func: msg.CalleeMethod(), Body: body}, now())
		if err != nil {
			return err
		}
		// Preserve transparent metadata supplied by the caller (for example the
		// strategy space scope) while adding the gateway authentication headers.
		metadata := make(codec.MetaData, len(msg.ClientMetaData())+len(headers))
		for key, value := range msg.ClientMetaData() {
			metadata[key] = value
		}
		for key, values := range headers {
			if len(values) == 1 {
				metadata[key] = []byte(values[0])
			}
		}
		msg.WithClientMetaData(metadata)
		rawRsp := &codec.Body{}
		if err := next(ctx, &codec.Body{Data: body}, rawRsp); err != nil {
			return err
		}
		return codec.Unmarshal(msg.SerializationType(), rawRsp.Data, rsp)
	}
}

// NewTRPCClientOptions returns the common target, protocol, and HMAC options
// for a generated client that must use the native Node Service Gateway.
func NewTRPCClientOptions(target, targetNode string, credentials Credentials) []client.Option {
	target = strings.TrimSpace(target)
	if target != "" && !strings.Contains(target, "://") {
		target = "ip://" + target
	}
	return []client.Option{
		client.WithTarget(target),
		client.WithNetwork("tcp"),
		client.WithProtocol("trpc"),
		client.WithTransport(transport.DefaultClientTransport),
		client.WithCurrentSerializationType(codec.SerializationTypeNoop),
		client.WithFilter(NewTRPCClientFilter(credentials, strings.TrimSpace(targetNode), nil)),
	}
}

// CredentialsFromEnv reads the per-process service credential material used by
// the native gateway. Missing credentials intentionally fail on the first RPC.
func CredentialsFromEnv() Credentials {
	return Credentials{KeyID: strings.TrimSpace(os.Getenv("MOOX_GATEWAY_SERVICE_KEY_ID")), Caller: strings.TrimSpace(os.Getenv("MOOX_GATEWAY_CALLER")), Secret: strings.TrimSpace(os.Getenv("MOOX_GATEWAY_SERVICE_SECRET_KEY"))}
}

func ServiceGatewayTarget(raw string) string {
	if target := strings.TrimSpace(os.Getenv("MOOX_SERVICE_GATEWAY_TARGET")); target != "" {
		return target
	}
	return raw
}

func ServiceGatewayNodeID() string { return strings.TrimSpace(os.Getenv("MOOX_GATEWAY_TARGET_NODE")) }
