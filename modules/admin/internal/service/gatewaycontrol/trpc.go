package gatewaycontrol

import (
	"context"
	"errors"
	"net/http"
	"strings"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

const maxRequestBytes = 64 << 10

// ServerOption applies only to this listener, after tRPC's configured options.
// Other Admin listeners retain their normal PB/JSON decoding and filters.
func ServerOption() server.Option {
	return func(options *server.Options) {
		if options.ServiceName == servicecatalog.GatewayControlPath {
			options.CurrentSerializationType = codec.SerializationTypeNoop
			options.Transport = transport.NewServerTransport()
		}
	}
}

func Register(service server.Service, implementation *Service) error {
	if service == nil || implementation == nil {
		return errors.New("gateway control listener and implementation are required")
	}
	description := &server.ServiceDesc{
		ServiceName: servicecatalog.GatewayControlPath, HandlerType: (*pb.GatewayControlService)(nil),
		Methods: []server.Method{
			{Name: "/" + servicecatalog.GatewayControlPath + "/PullSnapshot", Func: implementation.handle},
			{Name: "/" + servicecatalog.GatewayControlPath + "/ReportStatus", Func: implementation.handle},
		},
	}
	return service.Register(description, implementation)
}

func (s *Service) authenticate(ctx context.Context, path, method string, body []byte) (context.Context, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	message := codec.Message(ctx)
	headers := http.Header{}
	for key, value := range message.ServerMetaData() {
		headers.Add(key, string(value))
	}
	callers := headers.Values("X-Moox-Caller")
	if len(callers) != 1 || !strings.HasPrefix(callers[0], "host-gateway@") || !servicecatalog.ValidHostID(strings.TrimPrefix(callers[0], "host-gateway@")) {
		return nil, errors.New("gateway control authentication failed")
	}
	keys, err := s.keys.InternalVerification(ctx, callers)
	if err != nil {
		return nil, errors.New("gateway control authentication failed")
	}
	credentials := make([]gatewayauth.Credentials, 0, len(keys))
	for _, key := range keys {
		credentials = append(credentials, gatewayauth.Credentials{Caller: key.Caller, KeyID: key.KeyID, Secret: string(key.Secret)})
	}
	registry, err := gatewayauth.NewCredentialRegistry(credentials)
	if err != nil {
		return nil, errors.New("gateway control authentication failed")
	}
	claims, err := registry.Verify(gatewayauth.Request{Method: "POST", Path: path, TargetNode: s.controlHostID, Callee: servicecatalog.GatewayControlPath, Func: method, Body: body}, headers, s.now())
	if err != nil {
		return nil, errors.New("gateway control authentication failed")
	}
	consumed, err := s.nonces.ConsumeGatewayControlNonce(ctx, claims.KeyID, claims.Nonce, claims.TTL)
	if err != nil {
		return nil, errors.New("gateway control replay store unavailable")
	}
	if !consumed {
		return nil, errors.New("gateway control request replayed")
	}
	return context.WithValue(ctx, verifiedCaller{}, claims.Caller), nil
}

func (s *Service) handle(_ interface{}, ctx context.Context, decode server.FilterFunc) (interface{}, error) {
	raw := &codec.Body{}
	filters, err := decode(raw)
	if err != nil {
		return nil, err
	}
	return filters.Filter(ctx, raw, func(ctx context.Context, input interface{}) (interface{}, error) {
		body, ok := input.(*codec.Body)
		if !ok || body == nil || len(body.Data) > maxRequestBytes {
			return nil, errors.New("invalid gateway control request size")
		}
		message := codec.Message(ctx)
		serialization := message.SerializationType()
		if serialization != codec.SerializationTypePB && serialization != codec.SerializationTypeJSON {
			return nil, errors.New("gateway control requires PB or JSON serialization")
		}
		path := message.ServerRPCName()
		method := strings.TrimPrefix(path, "/"+servicecatalog.GatewayControlPath+"/")
		if path != "/"+servicecatalog.GatewayControlPath+"/"+method || method != "PullSnapshot" && method != "ReportStatus" {
			return nil, errors.New("unknown gateway control method")
		}
		verified, err := s.authenticate(ctx, path, method, body.Data)
		if err != nil {
			return nil, err
		}
		var response interface{}
		if method == "PullSnapshot" {
			request := &pb.PullSnapshotReq{}
			if err := codec.Unmarshal(serialization, body.Data, request); err != nil {
				return nil, errors.New("invalid gateway control request encoding")
			}
			response, err = s.PullSnapshot(verified, request)
		} else {
			request := &pb.ReportStatusReq{}
			if err := codec.Unmarshal(serialization, body.Data, request); err != nil {
				return nil, errors.New("invalid gateway control request encoding")
			}
			response, err = s.ReportStatus(verified, request)
		}
		if err != nil {
			return nil, err
		}
		encoded, err := codec.Marshal(serialization, response)
		if err != nil {
			return nil, errors.New("encode gateway control response")
		}
		return &codec.Body{Data: encoded}, nil
	})
}
