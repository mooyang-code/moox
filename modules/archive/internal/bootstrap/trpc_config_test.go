package bootstrap

import (
	"os"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gopkg.in/yaml.v3"
)

type trpcService struct {
	Name     string `yaml:"name"`
	Port     int    `yaml:"port"`
	Network  string `yaml:"network"`
	Protocol string `yaml:"protocol"`
	Timeout  int    `yaml:"timeout"`
}

// 框架配置里的服务名、端口必须和服务目录一致，且定时器与代码里注册的名字一致。
func TestShippedTRPCConfigMatchesTheServiceCatalogAndTimers(t *testing.T) {
	raw, err := os.ReadFile("../../config/trpc_go.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Server struct {
			Service []trpcService `yaml:"service"`
		} `yaml:"server"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	byName := map[string]trpcService{}
	ports := map[int]string{}
	for _, svc := range doc.Server.Service {
		if prev, ok := ports[svc.Port]; ok {
			t.Fatalf("端口 %d 同时被 %s 和 %s 使用", svc.Port, prev, svc.Name)
		}
		ports[svc.Port], byName[svc.Name] = svc.Name, svc
	}
	for _, name := range []string{healthService, mgrService, workerService, triggerService, maintainService} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("trpc_go.yaml 缺少服务 %s", name)
		}
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	component, ok := catalog.Component("archive-mgr")
	if !ok {
		t.Fatal("服务目录没有 archive-mgr")
	}
	for _, service := range component.Services {
		if byName[service.Path].Port != service.Port {
			t.Fatalf("%s 端口 trpc_go.yaml=%d 服务目录=%d", service.Path, byName[service.Path].Port, service.Port)
		}
	}
	if byName[healthService].Port != component.Health.Port {
		t.Fatalf("健康端口 trpc_go.yaml=%d 服务目录=%d", byName[healthService].Port, component.Health.Port)
	}
	trigger, maintain := byName[triggerService], byName[maintainService]
	if trigger.Protocol != "timer" || trigger.Network != "* * * * * *" || trigger.Timeout != int(triggerTimeout.Milliseconds()) {
		t.Fatalf("全局触发器配置不对: %+v", trigger)
	}
	if maintain.Protocol != "timer" || !strings.HasPrefix(maintain.Network, "13 ") || maintain.Timeout != int(maintainTimeout.Milliseconds()) {
		t.Fatalf("维护定时器配置不对: %+v", maintain)
	}
}
