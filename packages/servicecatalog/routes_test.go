package servicecatalog

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/packages/gatewayroute"
)

var update = flag.Bool("update", false, "重新生成 testdata 中的期望结果")

// productionDeployment 是生产三台主机的部署（设计文档 3.2）。
func productionDeployment() Deployment {
	hosts := []Host{
		{ID: "control", Address: "106.53.107.122", Enabled: true},
		{ID: "storage", Address: "146.56.196.204", PrivateAddress: "10.206.0.5", Region: "ap-nanjing", Enabled: true},
		{ID: "compute-1", Address: "43.132.204.177", PrivateAddress: "172.19.32.13", Region: "ap-hongkong", Enabled: true},
	}
	placements := map[string][]string{
		"control":   {"console-proxy", "web-host", "admin", "eventbus", "monitor", "collector", "cloudnode", "factor-mgr", "strategy"},
		"storage":   {"storage-primary", "storage-node", "storage-view", "access"},
		"compute-1": {"trade", "access", "egress-proxy"},
	}
	deployment := Deployment{Hosts: hosts}
	for host, components := range placements {
		for _, component := range components {
			deployment.Placements = append(deployment.Placements, Placement{HostID: host, ComponentID: component, Enabled: true})
		}
	}
	return deployment
}

func TestCompileProductionDeployment(t *testing.T) {
	compiled, err := Default().Compile(productionDeployment())
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range compiled.Hosts {
		if _, err := gatewayroute.NormalizeAndHash(host.HostID, host.Routes); err != nil {
			t.Fatalf("主机 %s 的路由无法通过网关校验: %v", host.HostID, err)
		}
	}
	golden := filepath.Join("testdata", "compiled_production.json")
	encoded, err := json.MarshalIndent(compiled, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if *update {
		if err := os.WriteFile(golden, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("读取期望结果失败（可用 go test -run TestCompileProductionDeployment -update 生成）: %v", err)
	}
	if string(want) != string(encoded) {
		t.Fatalf("编译结果与 %s 不一致；确认改动无误后用 -update 重新生成", golden)
	}

	control, _ := compiled.HostConfig("control")
	assertRouteCallers(t, control, "trpc.moox.admin.GatewayControl", "PullSnapshot",
		[]string{"host-gateway@compute-1", "host-gateway@control", "host-gateway@storage"})
	assertRouteCallers(t, control, "trpc.moox.cloudnode.CloudNodeMgr", "CollectGarbage", []string{"admin"})
	assertRouteCallers(t, control, "trpc.moox.collector.MarketFetchRuntime", "ClaimTimerBatch", []string{"access"})
	storage, _ := compiled.HostConfig("storage")
	assertRouteCallers(t, storage, "trpc.moox.storage.PrimaryStore", "WriteFactorRows", []string{"access", "factor-mgr"})
	if containsCaller(storage.Callers, "host-gateway@storage") {
		t.Fatal("storage 不提供网关控制，不需要主机网关的校验密钥")
	}
	if !containsCaller(control.Callers, "host-gateway@storage") || !containsCaller(control.Callers, "console") {
		t.Fatalf("control 的校验密钥范围不完整: %v", control.Callers)
	}
	compute, _ := compiled.HostConfig("compute-1")
	if !containsCaller(compute.Callers, "strategy") || !containsCaller(compute.Callers, "collector") || containsCaller(compute.Callers, "factor-mgr") {
		t.Fatalf("compute-1 的校验密钥范围不对: %v", compute.Callers)
	}
	// 出口代理只给 Collector 调用。
	assertRouteCallers(t, compute, "trpc.moox.egress.Proxy", "Do", []string{"collector"})
	assertRouteCallers(t, compute, "trpc.moox.egress.Proxy", "ResolveDomains", []string{"collector"})
	if got := compiled.Directory.ServiceHostIDs("trpc.moox.storage.Metadata"); strings.Join(got, ",") != "storage" {
		t.Fatalf("Metadata 应当在 storage，实际 %v", got)
	}
	if got := compiled.Directory.ComponentHostIDs("access"); strings.Join(got, ",") != "compute-1,storage" {
		t.Fatalf("外部接入应当在 compute-1 和 storage，实际 %v", got)
	}
	if got := compiled.Directory.ComponentHostIDs("host-gateway"); strings.Join(got, ",") != "compute-1,control,storage" {
		t.Fatalf("主机网关应当在每台主机，实际 %v", got)
	}
}

func TestCompileDisabledPlacementAndHost(t *testing.T) {
	deployment := productionDeployment()
	for i := range deployment.Placements {
		if deployment.Placements[i].ComponentID == "storage-view" {
			deployment.Placements[i].Enabled = false
		}
	}
	for i := range deployment.Hosts {
		if deployment.Hosts[i].ID == "compute-1" {
			deployment.Hosts[i].Enabled = false
		}
	}
	compiled, err := Default().Compile(deployment)
	if err != nil {
		t.Fatal(err)
	}
	if got := compiled.Directory.ServiceHostIDs("trpc.moox.storage.DataView"); len(got) != 0 {
		t.Fatalf("停用的部署不应出现在目录里: %v", got)
	}
	storage, _ := compiled.HostConfig("storage")
	for _, route := range storage.Routes {
		if route.ServicePath == "trpc.moox.storage.DataView" {
			t.Fatal("停用的部署不应产生路由")
		}
	}
	compute, _ := compiled.HostConfig("compute-1")
	if !compute.Disabled || len(compute.Routes) != 0 {
		t.Fatalf("停用的主机应当收到无路由的 Disabled 快照: %+v", compute)
	}
	if _, ok := compiled.Directory.Host("compute-1"); ok {
		t.Fatal("停用的主机不应出现在目录里")
	}
	control, _ := compiled.HostConfig("control")
	assertRouteCallers(t, control, "trpc.moox.admin.GatewayControl", "ReportStatus",
		[]string{"host-gateway@compute-1", "host-gateway@control", "host-gateway@storage"})
}

func TestCompileVersionTracksDirectory(t *testing.T) {
	first, err := Default().Compile(productionDeployment())
	if err != nil {
		t.Fatal(err)
	}
	second, err := Default().Compile(productionDeployment())
	if err != nil {
		t.Fatal(err)
	}
	if first.Directory.Version == "" || first.Directory.Version != second.Directory.Version {
		t.Fatalf("同一部署的目录版本应当稳定: %q %q", first.Directory.Version, second.Directory.Version)
	}
	deployment := productionDeployment()
	deployment.Hosts[1].PrivateAddress = "10.206.0.6"
	third, err := Default().Compile(deployment)
	if err != nil {
		t.Fatal(err)
	}
	if third.Directory.Version == first.Directory.Version {
		t.Fatal("主机地址变化后目录版本应当变化")
	}
}

func assertRouteCallers(t *testing.T, host HostConfig, path, method string, want []string) {
	t.Helper()
	for _, route := range host.Routes {
		if route.ServicePath != path {
			continue
		}
		for _, candidate := range route.AllowedMethods {
			if candidate == method {
				if strings.Join(route.AllowedCallers, ",") != strings.Join(want, ",") {
					t.Fatalf("%s/%s 的调用方 = %v, want %v", path, method, route.AllowedCallers, want)
				}
				return
			}
		}
	}
	t.Fatalf("主机 %s 没有 %s/%s 的路由", host.HostID, path, method)
}

func containsCaller(callers []string, caller string) bool {
	for _, item := range callers {
		if item == caller {
			return true
		}
	}
	return false
}
