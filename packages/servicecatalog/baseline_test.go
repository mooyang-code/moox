package servicecatalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"testing"
)

// H9：访问控制对机器身份不放宽，浏览器不能调的方法仍然不能调。当前目录要与 testdata 里的两份基线逐项一致；
// 有意改动时按 baseline_gen_test.go 文件头的用法重新生成，看过差异再提交。

func readBaseline(t *testing.T, name string, value any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, value); err != nil {
		t.Fatalf("%s：%v", name, err)
	}
}

func setOf(items []string) map[string]bool {
	set := make(map[string]bool, len(items))
	for _, item := range items {
		set[item] = true
	}
	return set
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// 对全部（服务 × 方法 × 调用方）组合核对 Allowed，对全部（服务 × 方法 × 外部身份）组合核对 PrincipalAllowed，
// 允许的与不允许的都要和基线一致；方法集合、只读、可重试、超时、控制台名与 relay_http 也要一致。
func TestCatalogMatchesACLBaseline(t *testing.T) {
	checkACLBaseline(t, mustCatalog(t))
}

// Monitor 按 JSON 保存目录：operators 的“省略”（两者都能调）与 []（都不能调）经 JSON 往返后不能混同，往返后的目录同样要与基线一致。
func TestCatalogJSONRoundTripKeepsACL(t *testing.T) {
	var decoded Catalog
	if err := json.Unmarshal(jsonBytes(t, mustCatalog(t)), &decoded); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatal(err)
	}
	none := methodPtr(&decoded, GatewayControlPath, "PullSnapshot")
	if none.Operators == nil || len(*none.Operators) != 0 || methodPtr(&decoded, "trpc.moox.collector.CollectMgr", "GetTaskList").Operators != nil {
		t.Fatal("operators 的省略与 [] 经 JSON 往返后混同了")
	}
	checkACLBaseline(t, decoded)
}

func checkACLBaseline(t *testing.T, c Catalog) {
	t.Helper()
	var baseline baselineFile
	readBaseline(t, "acl-baseline.json", &baseline)

	// 服务与方法的属性。
	current := []baselineService{}
	for _, component := range c.Components {
		for _, service := range component.Services {
			s := baselineService{Path: service.Path, Component: component.ID, ConsoleName: service.ConsoleName}
			methods := service.MethodNames()
			sort.Strings(methods)
			for _, method := range methods {
				s.Methods = append(s.Methods, baselineMethod{Name: method, ReadOnly: c.ReadOnly(service.Path, method), Retryable: c.Retryable(service.Path, method), TimeoutMS: service.TimeoutFor(method)})
			}
			current = append(current, s)
		}
	}
	sort.Slice(current, func(i, j int) bool { return current[i].Path < current[j].Path })
	if !reflect.DeepEqual(current, baseline.Services) {
		t.Fatalf("服务、方法或其只读、可重试、超时属性与基线不一致：\n当前 %s\n基线 %s", jsonBytes(t, current), jsonBytes(t, baseline.Services))
	}
	relays := []baselineRelay{}
	for _, component := range c.Components {
		if component.RelayHTTP != nil {
			callers, _ := c.RelayHTTPCallers(component.RelayHTTP.Port)
			relays = append(relays, baselineRelay{Component: component.ID, Port: component.RelayHTTP.Port, Callers: callers})
		}
	}
	if !reflect.DeepEqual(relays, baseline.RelayHTTP) {
		t.Fatalf("relay_http 与基线不一致：%+v", relays)
	}

	type pair struct{ path, method string }
	var all []pair
	for _, service := range baseline.Services {
		for _, method := range service.Methods {
			all = append(all, pair{service.Path, method.Name})
		}
	}

	// 调用方全集：基线里的每个调用方都要核对，目录新增的组件或外部身份也必须已经进了基线。
	universe := map[string]bool{"console": true, "moox-cli": true, "access": true, "host-gateway@control": true}
	for _, component := range c.Components {
		universe[component.ID] = true
	}
	external := c.ExternalIdentities()
	for _, principal := range external {
		universe[principal] = true
	}
	if !slices.Equal(sortedKeys(universe), sortedKeys(baseline.Callers)) {
		t.Fatalf("调用方全集与基线不一致：当前 %v，基线 %v", sortedKeys(universe), sortedKeys(baseline.Callers))
	}
	checked := 0
	for _, caller := range sortedKeys(baseline.Callers) {
		allowed := setOf(baseline.Callers[caller])
		for _, p := range all {
			key := p.path + "/" + p.method
			if got := c.Allowed(caller, p.path, p.method); got != allowed[key] {
				t.Errorf("Allowed(%s, %s) = %v，基线 %v", caller, key, got, allowed[key])
			}
			checked++
		}
	}
	// 基线之外的调用方写法：任一主机的 host-gateway 与 host-gateway@control 相同；通配写法、空值与未知调用方一律拒绝。
	for _, caller := range []string{"host-gateway@storage", "host-gateway@factor", "host-gateway@*", "host-gateway@", "*", "", "unknown", "Console"} {
		allowed := map[string]bool{}
		if caller == "host-gateway@storage" || caller == "host-gateway@factor" {
			allowed = setOf(baseline.Callers["host-gateway@control"])
		}
		for _, p := range all {
			key := p.path + "/" + p.method
			if got := c.Allowed(caller, p.path, p.method); got != allowed[key] {
				t.Errorf("Allowed(%q, %s) = %v，应为 %v", caller, key, got, allowed[key])
			}
			checked++
		}
	}

	// 外部身份：基线里的每个外部身份与每个方法；其余调用方都不是外部身份。
	sort.Strings(external)
	if !slices.Equal(external, sortedKeys(baseline.Principals)) {
		t.Fatalf("外部身份与基线不一致：当前 %v，基线 %v", external, sortedKeys(baseline.Principals))
	}
	principalChecked := 0
	for _, caller := range sortedKeys(baseline.Callers) {
		allowed := setOf(baseline.Principals[caller])
		for _, p := range all {
			key := p.path + "/" + p.method
			if got := c.PrincipalAllowed(caller, p.path, p.method); got != allowed[key] {
				t.Errorf("PrincipalAllowed(%s, %s) = %v，基线 %v", caller, key, got, allowed[key])
			}
			principalChecked++
		}
	}

	summary := baseline.Summary
	readOnly, retryable := 0, 0
	for _, service := range current {
		for _, method := range service.Methods {
			if method.ReadOnly {
				readOnly++
			}
			if method.Retryable {
				retryable++
			}
		}
	}
	if len(c.Components) != summary.Components || len(current) != summary.Services || len(all) != summary.Methods ||
		readOnly != summary.ReadOnlyMethods || retryable != summary.RetryableMethods || len(universe) != summary.Callers || len(external) != summary.Principals {
		t.Fatalf("目录统计与基线不一致：%+v", summary)
	}
	t.Logf("核对 Allowed %d 个组合（%d 个方法），PrincipalAllowed %d 个组合", checked, len(all), principalChecked)
}

// 固定拓扑下每台主机编译出的路由（服务、方法、端口、包体上限、超时、只读、调用方）、校验名单与中转许可要与基线一致。
func TestCompileMatchesBaseline(t *testing.T) {
	var baseline compileBaselineFile
	readBaseline(t, "compile-baseline.json", &baseline)
	if !reflect.DeepEqual(baseline.Topology, compileBaselineTopology()) {
		t.Fatal("基线里的拓扑与 compileBaselineTopology 不一致，重新生成基线")
	}
	current, err := compileBaseline(mustCatalog(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Hosts) != len(baseline.Hosts) {
		t.Fatalf("主机数 %d，基线 %d", len(current.Hosts), len(baseline.Hosts))
	}
	routes := 0
	for i, host := range current.Hosts {
		want := baseline.Hosts[i]
		if host.HostID != want.HostID {
			t.Fatalf("主机顺序变了：%s，基线 %s", host.HostID, want.HostID)
		}
		if !slices.Equal(host.VerificationCallers, want.VerificationCallers) {
			t.Errorf("%s 校验名单 %v，基线 %v", host.HostID, host.VerificationCallers, want.VerificationCallers)
		}
		if !reflect.DeepEqual(host.Relays, want.Relays) {
			t.Errorf("%s 中转许可 %+v，基线 %+v", host.HostID, host.Relays, want.Relays)
		}
		got := map[string]compileBaselineRoute{}
		for _, route := range host.Routes {
			got[route.Service+"/"+route.Method] = route
		}
		for _, route := range want.Routes {
			key := route.Service + "/" + route.Method
			if current, ok := got[key]; !ok {
				t.Errorf("%s 少了路由 %s", host.HostID, key)
			} else if !reflect.DeepEqual(current, route) {
				t.Errorf("%s 路由 %s 变了：%+v，基线 %+v", host.HostID, key, current, route)
			}
			delete(got, key)
		}
		for key := range got {
			t.Errorf("%s 多了路由 %s", host.HostID, key)
		}
		routes += len(want.Routes)
	}
	t.Logf("核对 %d 台主机、%d 条路由", len(current.Hosts), routes)
}
