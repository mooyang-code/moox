package directorypb

import (
	"context"
	"errors"
	"fmt"

	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/server"
)

// GetDirectoryRPCName 是 Directory/GetDirectory 的完整 RPC 名。
const GetDirectoryRPCName = "/trpc.moox.hostgateway.Directory/GetDirectory"

// NoopServiceDesc 返回能注册到 Noop 序列化服务上的 Directory 服务描述。
//
// 主机网关的本机入口以 Noop 序列化透传所有请求，生成的描述会返回 PB 对象，无法在这样的服务上
// 编码。这里按调用方声明的序列化类型自行编解码，因此 Directory 可以与通配转发共用 11002：
// tRPC 先精确匹配方法名，其余请求才落到通配转发。
func NoopServiceDesc() *server.ServiceDesc {
	return &server.ServiceDesc{
		ServiceName: DirectoryServer_ServiceDesc.ServiceName,
		HandlerType: ((*DirectoryService)(nil)),
		Methods:     []server.Method{{Name: GetDirectoryRPCName, Func: noopGetDirectoryHandler}},
	}
}

func noopGetDirectoryHandler(svr interface{}, ctx context.Context, f server.FilterFunc) (interface{}, error) {
	request := &codec.Body{}
	filters, err := f(request)
	if err != nil {
		return nil, err
	}
	return filters.Filter(ctx, request, func(ctx context.Context, body interface{}) (interface{}, error) {
		raw, ok := body.(*codec.Body)
		if !ok || raw == nil {
			return nil, errors.New("Directory 请求体无效")
		}
		serialization := codec.Message(ctx).SerializationType()
		req := &GetDirectoryReq{}
		if err := codec.Unmarshal(serialization, raw.Data, req); err != nil {
			return nil, errs.NewFrameError(errs.RetServerDecodeFail, fmt.Sprintf("解析 Directory 请求: %v", err))
		}
		rsp, err := svr.(DirectoryService).GetDirectory(ctx, req)
		if err != nil {
			return nil, err
		}
		out, err := codec.Marshal(serialization, rsp)
		if err != nil {
			return nil, errs.NewFrameError(errs.RetServerEncodeFail, fmt.Sprintf("编码 Directory 响应: %v", err))
		}
		return &codec.Body{Data: out}, nil
	})
}
