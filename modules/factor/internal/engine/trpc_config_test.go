package engine

import (
	"os"
	"testing"

	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gopkg.in/yaml.v3"
)

// 随包交付的 trpc_go.engine.yaml 与服务目录一致：EngineControl 只监听本机，端口是目录里登记的端口，
// 服务端超时等于 OpenReplay 的路由超时（最慢的方法；短了会先于路由掐断建会话）；健康端口也与目录一致。
func TestEngineTRPCConfigMatchesTheServiceCatalog(t *testing.T) {
	raw, err := os.ReadFile("../../config/trpc_go.engine.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Server struct {
			Service []struct {
				Name     string `yaml:"name"`
				IP       string `yaml:"ip"`
				Port     int    `yaml:"port"`
				Protocol string `yaml:"protocol"`
				Timeout  int64  `yaml:"timeout"`
			} `yaml:"service"`
		} `yaml:"server"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	component, ok := catalog.Component("factor-engine")
	if !ok {
		t.Fatal("服务目录没有 factor-engine")
	}
	registered, ok := catalog.Service(engineControlService)
	if !ok {
		t.Fatalf("服务目录没有 %s", engineControlService)
	}
	found := map[string]bool{}
	for _, service := range doc.Server.Service {
		found[service.Name] = true
		switch service.Name {
		case engineControlService:
			if service.IP != "127.0.0.1" {
				t.Fatalf("%s 必须只监听 127.0.0.1，配置是 %q", service.Name, service.IP)
			}
			if service.Port != registered.Port {
				t.Fatalf("%s 端口 trpc_go.engine.yaml=%d 服务目录=%d", service.Name, service.Port, registered.Port)
			}
			if service.Protocol != "trpc" {
				t.Fatalf("%s 必须是 trpc 协议，配置是 %q", service.Name, service.Protocol)
			}
			if want := registered.TimeoutFor("OpenReplay"); service.Timeout != want {
				t.Fatalf("%s 超时 trpc_go.engine.yaml=%d，OpenReplay 的路由超时=%d", service.Name, service.Timeout, want)
			}
		case healthService:
			if service.Port != component.Health.Port {
				t.Fatalf("健康端口 trpc_go.engine.yaml=%d 服务目录=%d", service.Port, component.Health.Port)
			}
		}
	}
	for _, name := range []string{healthService, engineControlService} {
		if !found[name] {
			t.Fatalf("trpc_go.engine.yaml 缺少服务 %s", name)
		}
	}
}
