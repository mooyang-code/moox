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

// ServerOption 只作用在这个监听上；Admin 的其他监听照常解码 PB/JSON、走各自的过滤器。
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
		},
	}
	return service.Register(description, implementation)
}

// verified 取出网关写的签名主机头。这个头只可能来自本机网关：服务只听回环端口，网关在入口剥掉了外面带进来的同名头。
func verified(ctx context.Context) (context.Context, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	headers := http.Header{}
	for key, value := range codec.Message(ctx).ServerMetaData() {
		headers.Add(key, string(value))
	}
	hosts := headers.Values(gatewayauth.HeaderVerifiedHost)
	if len(hosts) != 1 || !servicecatalog.ValidHostID(hosts[0]) {
		return nil, errors.New("gateway control requires the verified signing host")
	}
	return context.WithValue(ctx, verifiedHost{}, hosts[0]), nil
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
		if path != "/"+servicecatalog.GatewayControlPath+"/"+method || method != "PullSnapshot" {
			return nil, errors.New("unknown gateway control method")
		}
		verified, err := verified(ctx)
		if err != nil {
			return nil, err
		}
		request := &pb.PullSnapshotReq{}
		if err := codec.Unmarshal(serialization, body.Data, request); err != nil {
			return nil, errors.New("invalid gateway control request encoding")
		}
		response, err := s.PullSnapshot(verified, request)
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
