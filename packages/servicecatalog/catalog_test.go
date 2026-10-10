package servicecatalog

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestDefaultCatalogIsValid(t *testing.T) {
	catalog := Default()
	if catalog.Checksum() == "" || !strings.HasPrefix(catalog.Checksum(), "sha256:") {
		t.Fatalf("checksum = %q", catalog.Checksum())
	}
	for _, id := range []string{"host-gateway", "admin", "access", "console-proxy", "web-host"} {
		component, ok := catalog.Component(id)
		if !ok {
			t.Fatalf("缺少组件 %s", id)
		}
		if id != "access" && !component.Protected {
			t.Fatalf("组件 %s 应当受保护", id)
		}
	}
	if access, _ := catalog.Component("access"); access.Replicas != ReplicasMulti {
		t.Fatalf("外部接入应当允许多份，实际 %s", access.Replicas)
	}
}

var (
	protoPackagePattern = regexp.MustCompile(`(?m)^package\s+([\w.]+);`)
	protoServicePattern = regexp.MustCompile(`(?s)service\s+(\w+)\s*\{(.*?)\n\}`)
	protoRPCPattern     = regexp.MustCompile(`rpc\s+(\w+)\s*\(`)
)

// TestCatalogRPCsMatchProtos 保证目录里每个服务的方法清单与 proto 完全一致。
func TestCatalogRPCsMatchProtos(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "modules", "*", "proto", "*.proto"))
	if err != nil {
		t.Fatal(err)
	}
	more, err := filepath.Glob(filepath.Join("..", "..", "packages", "*", "proto", "*.proto"))
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, more...)
	protos := map[string][]string{}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		pkg := protoPackagePattern.FindStringSubmatch(text)
		if pkg == nil {
			continue
		}
		for _, match := range protoServicePattern.FindAllStringSubmatch(text, -1) {
			var rpcs []string
			for _, rpc := range protoRPCPattern.FindAllStringSubmatch(match[2], -1) {
				rpcs = append(rpcs, rpc[1])
			}
			sort.Strings(rpcs)
			protos[pkg[1]+"."+match[1]] = rpcs
		}
	}
	if len(protos) == 0 {
		t.Fatal("没有找到任何 proto 服务")
	}
	for _, component := range Default().Components {
		for _, service := range component.Services {
			want, ok := protos[service.Path]
			if !ok {
				t.Errorf("服务 %s 在 proto 中不存在", service.Path)
				continue
			}
			got := append([]string(nil), service.RPCs...)
			sort.Strings(got)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("服务 %s 的方法与 proto 不一致\n目录: %v\nproto: %v", service.Path, got, want)
			}
		}
	}
}

func TestConsoleRoutes(t *testing.T) {
	catalog := Default()
	cases := []struct {
		consoleName, method, path string
		ok                        bool
	}{
		{"storage", "ListDatasets", "trpc.moox.storage.Metadata", true},
		{"storage", "UpsertFields", "trpc.moox.storage.PrimaryStore", true},
		{"storage", "QueryTimeSeriesRows", "trpc.moox.storage.DataView", true},
		{"storage", "ApplyTagSnapshot", "", false},
		{"collector", "GetTaskList", "trpc.moox.collector.CollectMgr", true},
		{"cloudnode", "CollectGarbage", "", false},
		{"secret", "GetSecretValue", "", false},
		{"secret", "ListSecrets", "trpc.moox.ops.SecretMgr", true},
		{"publishlease", "AcquireCollectorPublishLease", "trpc.moox.admin.CollectorPublishLease", true},
		{"publishlease", "ValidateCollectorPublishLease", "", false},
		{"trade", "ClaimLogicalAccountOwner", "", false},
		{"trade", "SubmitOrder", "trpc.moox.trade.TradeConsoleService", true},
		{"factor-mgr", "ListFactors", "trpc.moox.factor.FactorMgr", true},
		{"monitor", "GetHealthOverview", "trpc.moox.monitor.MonitorMgr", true},
		{"sysdeploy", "ListHosts", "trpc.moox.ops.SysDeploy", true},
		{"unknown", "ListDatasets", "", false},
	}
	for _, tc := range cases {
		path, ok := catalog.ConsoleTarget(tc.consoleName, tc.method)
		if ok != tc.ok || path != tc.path {
			t.Errorf("ConsoleTarget(%s, %s) = %q, %v; want %q, %v", tc.consoleName, tc.method, path, ok, tc.path, tc.ok)
		}
	}
}

func TestReadOnly(t *testing.T) {
	catalog := Default()
	if !catalog.IsReadOnly("trpc.moox.storage.PrimaryStore", "ReadTimeSeriesRows") {
		t.Fatal("ReadTimeSeriesRows 应当是只读方法")
	}
	if catalog.IsReadOnly("trpc.moox.storage.PrimaryStore", "CommitTimeSeriesBatch") {
		t.Fatal("CommitTimeSeriesBatch 不能重试")
	}
	if catalog.IsReadOnly("trpc.moox.unknown.Service", "Get") {
		t.Fatal("未知服务不能视为只读")
	}
}
