package console

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/servicecatalog"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/filter"
	"trpc.group/trpc-go/trpc-go/server"
)

// RPCForwarder accepts exact serialized request bytes.
type RPCForwarder interface {
	Forward(context.Context, string, string, int, []byte) ([]byte, error)
}

type localMethod struct {
	implementation any
	handler        server.Method
	filters        filter.ServerChain
}

// LocalDispatcher binds generated handlers to the same instances registered on
// Admin's RPC listeners. Register all services before serving requests.
type LocalDispatcher struct {
	catalog servicecatalog.Catalog
	methods map[string]localMethod
}

func NewLocalDispatcher() (*LocalDispatcher, error) {
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return nil, err
	}
	return &LocalDispatcher{catalog: catalog, methods: make(map[string]localMethod)}, nil
}

func (d *LocalDispatcher) Register(desc *server.ServiceDesc, implementation any, filters ...filter.ServerFilter) error {
	if desc == nil || implementation == nil {
		return fmt.Errorf("local dispatcher requires service descriptor and implementation")
	}
	admin, _ := d.catalog.Component("admin")
	owned := false
	for _, service := range admin.Services {
		owned = owned || service.Path == desc.ServiceName
	}
	if !owned {
		return fmt.Errorf("local dispatcher accepts only Admin services")
	}
	seen := make(map[string]bool, len(desc.Methods))
	for _, method := range desc.Methods {
		if method.Func == nil || !strings.HasPrefix(method.Name, "/"+desc.ServiceName+"/") || d.methods[method.Name].handler.Func != nil || seen[method.Name] {
			return fmt.Errorf("invalid or duplicate local RPC handler: %s", method.Name)
		}
		seen[method.Name] = true
	}
	for _, method := range desc.Methods {
		d.methods[method.Name] = localMethod{implementation, method, append(filter.ServerChain(nil), filters...)}
	}
	return nil
}

func (d *LocalDispatcher) Forward(ctx context.Context, service, method string, serialization int, body []byte) (response []byte, err error) {
	// Enforce ACL here as well as at the browser route. Direct callers cannot
	// bypass it by using a generated service name or a registered handler.
	if !d.catalog.Allowed("console", service, method) {
		return nil, errs.New(errs.RetServerAuthFail, "console method is not allowed")
	}
	entry, ok := d.methods["/"+service+"/"+method]
	if !ok {
		return nil, errs.New(errs.RetServerNoService, "local Admin method is not registered")
	}
	if serialization != codec.SerializationTypeJSON {
		return nil, errs.New(errs.RetServerDecodeFail, "console requires JSON serialization")
	}
	spec, _ := d.catalog.Service(service)
	if int64(len(body)) > spec.MaxBodyBytes {
		return nil, errRequestBodyTooLarge
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(spec.TimeoutMS)*time.Millisecond)
	defer cancel()
	ctx, message := codec.WithCloneMessage(ctx)
	defer codec.PutBackMessage(message)
	message.WithServerRPCName(entry.handler.Name)
	message.WithCalleeServiceName(service)
	message.WithCalleeMethod(method)
	message.WithSerializationType(serialization)
	defer func() {
		if recover() != nil {
			response, err = nil, errs.New(errs.RetServerSystemErr, "local Admin handler failed")
		}
	}()
	value, err := entry.handler.Func(entry.implementation, ctx, func(request any) (filter.ServerChain, error) {
		if err := codec.Unmarshal(serialization, body, request); err != nil {
			return nil, errs.New(errs.RetServerDecodeFail, "invalid console JSON request")
		}
		return entry.filters, nil
	})
	if err != nil {
		return nil, err
	}
	response, err = codec.Marshal(serialization, value)
	if err == nil && int64(len(response)) > spec.MaxBodyBytes {
		return nil, fmt.Errorf("local Admin response exceeds service body limit")
	}
	return response, err
}
