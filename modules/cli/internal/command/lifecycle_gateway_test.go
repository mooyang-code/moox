package command

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// lifecycleGateway 是测试用的操作员网关：按服务名把 GetLifecycle、ControlLifecycle 交给各模块的生命周期控制器
// （packages/lifecycle 的 RPC 实现，和各模块服务里的那一行转发相同），请求与响应都过一遍 protobuf 编码；
// 服务目录只列出登记了控制器的服务。
type lifecycleGateway struct {
	mu       sync.Mutex
	modules  map[string]*lifecycle.Controller // 服务名 → 控制器
	failures map[string]error                 // 服务名 → 网关上的失败（模块不可达）
	calls    []string                         // “模块:命令”，按发出的顺序
	closed   bool
}

func newLifecycleGateway() *lifecycleGateway {
	return &lifecycleGateway{modules: map[string]*lifecycle.Controller{}, failures: map[string]error{}}
}

// add 登记一个模块：名字取自 lifecycleModules，钩子由测试给出。
func (g *lifecycleGateway) add(t *testing.T, name string, hooks lifecycle.Hooks) *lifecycle.Controller {
	t.Helper()
	module, ok := lifecycleModuleByName(name)
	require.True(t, ok, name)
	ctrl, err := lifecycle.New(lifecycle.Options{Module: name, QuiesceTimeout: 50 * time.Millisecond, Hooks: hooks})
	require.NoError(t, err)
	g.modules[module.Service] = ctrl
	return ctrl
}

// use 让本测试里的命令都拿到这个网关。
func (g *lifecycleGateway) use(t *testing.T) {
	t.Helper()
	previous := openCommandGateway
	openCommandGateway = func(context.Context, string, *setupconfig.Snapshot) (commandGateway, error) { return g, nil }
	t.Cleanup(func() { openCommandGateway = previous })
	interval := systemPollInterval
	systemPollInterval = time.Millisecond
	t.Cleanup(func() { systemPollInterval = interval })
}

func (g *lifecycleGateway) snapshot() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.calls...)
}

func (g *lifecycleGateway) Forward(context.Context, string, string, int, []byte) ([]byte, error) {
	return nil, errors.New("not used")
}

func (g *lifecycleGateway) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	return nil
}

func (g *lifecycleGateway) Directory() servicecatalog.Directory {
	services := map[string][]string{}
	for service := range g.modules {
		services[service] = []string{"control"}
	}
	return servicecatalog.Directory{Services: services}
}

func (g *lifecycleGateway) Invoke(ctx context.Context, service, method string, req, rsp any) error {
	ctrl := g.modules[service]
	if ctrl == nil {
		return errors.New("service has no enabled deployment")
	}
	if err := g.failures[service]; err != nil {
		return err
	}
	raw, err := proto.Marshal(req.(proto.Message))
	if err != nil {
		return err
	}
	var out proto.Message
	switch method {
	case lifecycle.GetMethod:
		in := &commonpb.GetLifecycleReq{}
		if err := proto.Unmarshal(raw, in); err != nil {
			return err
		}
		out, _ = ctrl.GetLifecycle(ctx, in)
	case lifecycle.ControlMethod:
		in := &commonpb.ControlLifecycleReq{}
		if err := proto.Unmarshal(raw, in); err != nil {
			return err
		}
		g.mu.Lock()
		g.calls = append(g.calls, ctrl.State().Module+":"+in.GetCommand())
		g.mu.Unlock()
		out, _ = ctrl.ControlLifecycle(ctx, in)
	default:
		return errors.New("unexpected method " + method)
	}
	encoded, err := proto.Marshal(out)
	if err != nil {
		return err
	}
	return proto.Unmarshal(encoded, rsp.(proto.Message))
}

func indexOf(calls []string, call string) int {
	for i, c := range calls {
		if c == call {
			return i
		}
	}
	return -1
}
