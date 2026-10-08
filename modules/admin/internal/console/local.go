package console

import (
	"context"
	"fmt"
	"reflect"

	"github.com/mooyang-code/moox/packages/gatewayroute"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/filter"
	"trpc.group/trpc-go/trpc-go/server"
)

// LocalServices 是管理后台自己的 tRPC 服务。控制台在进程内直接调用它们，不经过主机网关，
// 因此即使网关异常，也能在页面上处理部署和认证。调用前路由已按 console 的 ACL 校验，
// 调用时经过与 tRPC 服务端相同的过滤器（校验、脱敏等）。
type LocalServices struct {
	services map[string]localService
	filters  filter.ServerChain
}

type localService struct {
	impl    any
	methods map[string]server.Method
}

// NewLocalServices 创建进程内服务表；filters 是进程内调用经过的服务端过滤器。
func NewLocalServices(filters ...filter.ServerFilter) *LocalServices {
	return &LocalServices{services: map[string]localService{}, filters: filters}
}

// Register 登记一个生成的服务描述及其实现。
func (l *LocalServices) Register(desc *server.ServiceDesc, impl any) error {
	if desc == nil || impl == nil {
		return fmt.Errorf("进程内服务描述和实现都不能为空")
	}
	if desc.HandlerType != nil {
		handlerType := reflect.TypeOf(desc.HandlerType).Elem()
		if !reflect.TypeOf(impl).Implements(handlerType) {
			return fmt.Errorf("%T 没有实现 %s", impl, handlerType)
		}
	}
	if _, exists := l.services[desc.ServiceName]; exists {
		return fmt.Errorf("进程内服务 %s 重复登记", desc.ServiceName)
	}
	methods := make(map[string]server.Method, len(desc.Methods))
	prefix := "/" + desc.ServiceName + "/"
	for _, method := range desc.Methods {
		name := method.Name
		if len(name) > len(prefix) && name[:len(prefix)] == prefix {
			name = name[len(prefix):]
		}
		methods[name] = method
	}
	l.services[desc.ServiceName] = localService{impl: impl, methods: methods}
	return nil
}

// Has 判断服务是否在本进程。
func (l *LocalServices) Has(servicePath string) bool {
	_, ok := l.services[servicePath]
	return ok
}

// Invoke 以 JSON 解码请求、调用实现，再把响应编码为 JSON。
func (l *LocalServices) Invoke(parent context.Context, servicePath, method string, body []byte) ([]byte, error) {
	service, ok := l.services[servicePath]
	if !ok {
		return nil, errs.New(gatewayroute.RetServiceNotHere, fmt.Sprintf("服务 %s 不在本进程", servicePath))
	}
	handler, ok := service.methods[method]
	if !ok {
		return nil, errs.NewFrameError(errs.RetServerNoFunc, fmt.Sprintf("服务 %s 没有方法 %s", servicePath, method))
	}
	ctx, msg := codec.WithNewMessage(parent)
	msg.WithServerRPCName("/" + servicePath + "/" + method)
	msg.WithCalleeServiceName(servicePath)
	msg.WithCalleeMethod(method)
	msg.WithSerializationType(codec.SerializationTypeJSON)
	if metadata := codec.Message(parent).ServerMetaData(); len(metadata) > 0 {
		msg.WithServerMetaData(metadata.Clone())
	}
	rsp, err := handler.Func(service.impl, ctx, func(reqBody interface{}) (filter.ServerChain, error) {
		if len(body) > 0 {
			if err := codec.Unmarshal(codec.SerializationTypeJSON, body, reqBody); err != nil {
				return nil, errs.NewFrameError(errs.RetServerDecodeFail, fmt.Sprintf("解析 %s/%s 请求: %v", servicePath, method, err))
			}
		}
		return l.filters, nil
	})
	if err != nil {
		return nil, err
	}
	out, err := codec.Marshal(codec.SerializationTypeJSON, rsp)
	if err != nil {
		return nil, errs.NewFrameError(errs.RetServerEncodeFail, fmt.Sprintf("编码 %s/%s 响应: %v", servicePath, method, err))
	}
	return out, nil
}
