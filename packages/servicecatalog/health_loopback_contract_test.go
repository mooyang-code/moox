package servicecatalog_test

// 健康端口只听本机的契约（批 4 T8）：/healthz、/readyz、/metrics 不验签，只给同机进程与 moox-cli doctor 经 SSH 在目标机上请求，
// 所以每个组件的健康监听在仓库模板里都必须是 127.0.0.1，部署渲染也不改写它（modules/cli/internal/setup/deploy/render.go）。
// 新加组件时要在 healthListeners 里登记它的健康监听在哪，否则这里失败。

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"gopkg.in/yaml.v3"
)

// healthListener 是一个组件健康监听的出处（路径相对仓库根）：
//   - trpc：tRPC 框架配置里端口等于健康端口的那个服务；
//   - key：yaml 文件里的一个键（用点号分层），值是监听地址；
//   - text：文件里必须原样出现的一段（部署脚本里写死的配置、代码里的默认值）。
type healthListener struct {
	kind, file, key string
}

var healthListeners = map[string][]healthListener{
	"host-gateway":        {{kind: "key", file: "modules/hostgateway/config/app.yaml", key: "server.health_addr"}},
	"host-agent":          {{kind: "trpc", file: "modules/hostagent/config/trpc_go.yaml"}},
	"admin":               {{kind: "trpc", file: "modules/admin/config/trpc_go.yaml"}},
	"eventbus":            {{kind: "trpc", file: "modules/eventbus/config/trpc_go.yaml"}},
	"monitor":             {{kind: "trpc", file: "modules/monitor/config/trpc_go.yaml"}},
	"collector":           {{kind: "trpc", file: "modules/collector/config/trpc_go.yaml"}},
	"collector-stream":    {{kind: "trpc", file: "modules/collector/config/stream/trpc_go.yaml"}},
	"factor-mgr":          {{kind: "trpc", file: "modules/factor/config/trpc_go.yaml"}},
	"archive-mgr":         {{kind: "trpc", file: "modules/archive/config/trpc_go.yaml"}},
	"strategy":            {{kind: "trpc", file: "modules/strategy/config/trpc_go.yaml"}},
	"storage-primary":     {{kind: "trpc", file: "modules/storage/config/trpc_go.yaml"}, {kind: "trpc", file: "modules/storage/config/trpc_go.primary.yaml"}},
	"storage-view":        {{kind: "trpc", file: "modules/storage/config/storage_view/trpc_go.yaml"}},
	"access":              {{kind: "trpc", file: "modules/access/config/trpc_go.yaml"}},
	"egress-proxy":        {{kind: "trpc", file: "modules/egressproxy/config/trpc_go.yaml"}},
	"trade":               {{kind: "trpc", file: "modules/trade/config/trpc_go.yaml"}},
	"callback-dispatcher": {{kind: "trpc", file: "modules/callbackdispatcher/config/trpc_go.yaml"}},
	"factor-engine":       {{kind: "trpc", file: "modules/factor/config/trpc_go.engine.yaml"}},
	"archive-worker":      {{kind: "key", file: "modules/archive/config/worker/app.yaml", key: "health.addr"}},
}

func repoFile(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// trpcServices 读 tRPC 框架配置里的服务监听；定时器（protocol: timer）不监听端口，它的 port 只是编号，跳过。
func trpcServices(t *testing.T, name string) []map[string]any {
	t.Helper()
	var doc struct {
		Server struct {
			Service []map[string]any `yaml:"service"`
		} `yaml:"server"`
	}
	if err := yaml.Unmarshal(repoFile(t, name), &doc); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return slices.DeleteFunc(doc.Server.Service, func(service map[string]any) bool { return service["protocol"] == "timer" })
}

// yamlValue 取 yaml 文件里按点号分层的一个字符串值。
func yamlValue(t *testing.T, name, key string) string {
	t.Helper()
	var node any
	if err := yaml.Unmarshal(repoFile(t, name), &node); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	for _, part := range strings.Split(key, ".") {
		object, ok := node.(map[string]any)
		if !ok {
			t.Fatalf("%s 里没有 %s", name, key)
		}
		node = object[part]
	}
	value, ok := node.(string)
	if !ok {
		t.Fatalf("%s 里的 %s 不是字符串：%v", name, key, node)
	}
	return value
}

func TestEveryHealthListenerBindsLoopbackOnly(t *testing.T) {
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	want := func(port int) string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) }
	for _, component := range catalog.Components {
		if component.ThirdParty {
			// 第三方程序（内置 SeaweedFS）没有健康端口，它的端口由 args 里的 -ip.bind=127.0.0.1 绑在本机。
			continue
		}
		listeners, ok := healthListeners[component.ID]
		if !ok || len(listeners) == 0 {
			t.Errorf("%s 没有登记健康监听的出处，无法确认它只听本机", component.ID)
			continue
		}
		port := component.Health.Port
		for _, listener := range listeners {
			switch listener.kind {
			case "trpc":
				found := 0
				for _, service := range trpcServices(t, listener.file) {
					if service["port"] != port {
						continue
					}
					found++
					if service["ip"] != "127.0.0.1" || service["nic"] != nil || service["address"] != nil {
						t.Errorf("%s：%s 的健康服务 %v 必须只听 127.0.0.1（ip=%v nic=%v address=%v）", listener.file, component.ID, service["name"], service["ip"], service["nic"], service["address"])
					}
				}
				if found != 1 {
					t.Errorf("%s：%s 的健康端口 %d 应当恰好有一个服务监听，实际 %d 个", listener.file, component.ID, port, found)
				}
			case "key":
				if got := yamlValue(t, listener.file, listener.key); got != want(port) {
					t.Errorf("%s：%s 的 %s = %q，必须是 %q", listener.file, component.ID, listener.key, got, want(port))
				}
			default:
				t.Fatalf("unknown listener kind %q", listener.kind)
			}
		}
	}
	for id := range healthListeners {
		if _, ok := catalog.Component(id); !ok {
			t.Errorf("healthListeners 登记了目录里没有的组件 %s", id)
		}
	}
	// 部署生成主机网关配置用的是代码里的默认值，不是仓库里的 app.yaml。
	gateway, _ := catalog.Component("host-gateway")
	if got := hostgatewayconfig.Default("storage", "control", "192.0.2.1", "fixture-key").Server.HealthAddr; got != want(gateway.Health.Port) {
		t.Errorf("hostgatewayconfig.Default 的 health_addr = %q，必须是 %q", got, want(gateway.Health.Port))
	}
}

// 任何 tRPC 框架配置里名为 *.Health 的服务都只听本机，包括以后新增、还没登记到上面的文件。
func TestNoTrpcHealthServiceListensOnAnInterface(t *testing.T) {
	var files []string
	for _, pattern := range []string{"../../modules/*/config/trpc_go*.yaml", "../../modules/*/config/*/trpc_go*.yaml", "../../config/trpc_go*.yaml"} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, matches...)
	}
	if len(files) < 15 {
		t.Fatalf("只找到 %d 份 tRPC 框架配置，路径可能变了", len(files))
	}
	checked := 0
	for _, file := range files {
		name := strings.TrimPrefix(filepath.ToSlash(file), "../../")
		for _, service := range trpcServices(t, name) {
			if serviceName, _ := service["name"].(string); !strings.HasSuffix(serviceName, ".Health") {
				continue
			}
			checked++
			if service["ip"] != "127.0.0.1" || service["nic"] != nil || service["address"] != nil {
				t.Errorf("%s 的 %v 必须只听 127.0.0.1", name, service["name"])
			}
		}
	}
	if checked < 15 || !slices.ContainsFunc(files, func(file string) bool { return strings.HasSuffix(file, "trpc_go.engine.yaml") }) {
		t.Fatalf("只核对了 %d 个健康服务", checked)
	}
}
