package servicecatalog

// 访问控制基线的生成程序（批 4 H9：访问控制对机器身份不放宽，浏览器不能调的方法仍然不能调）。
//
// 生成两份基线，baseline_test.go 每次跑测试都拿当前目录与它们逐项核对：
//   - testdata/acl-baseline.json：每个服务、方法的只读、可重试、超时，以及每个调用方 Allowed 为真、
//     每个外部身份 PrincipalAllowed 为真的“服务/方法”集合；
//   - testdata/compile-baseline.json：compileBaselineTopology 这个固定拓扑下每台主机 Compile 的结果
//     （每条路由的服务、方法、端口、包体上限、超时、调用方，校验名单，以及 control 上的中转许可）。
//
// 用法：每次改 catalog.yaml 或编译逻辑之后，在本目录执行
//
//	MOOX_WRITE_ACL_BASELINE=1 go test -run TestGenerate -count=1 .
//	git diff testdata/
//
// 逐条看差异：放宽了哪个调用方、哪个方法，端口、包体上限、超时变没变，确认都是有意的再一起提交。
// 没设 MOOX_WRITE_ACL_BASELINE 时这两个测试直接跳过。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"testing"
)

type baselineMethod struct {
	Name      string `json:"name"`
	ReadOnly  bool   `json:"read_only"`
	Retryable bool   `json:"retryable"`
	TimeoutMS int64  `json:"timeout_ms"`
}

type baselineService struct {
	Path        string           `json:"path"`
	Component   string           `json:"component"`
	ConsoleName string           `json:"console_name,omitempty"`
	Methods     []baselineMethod `json:"methods"`
}

type baselineRelay struct {
	Component string   `json:"component"`
	Port      int      `json:"port"`
	Callers   []string `json:"callers"`
}

type baselineSummary struct {
	Components       int `json:"components"`
	Services         int `json:"services"`
	Methods          int `json:"methods"`
	ReadOnlyMethods  int `json:"read_only_methods"`
	RetryableMethods int `json:"retryable_methods"`
	Callers          int `json:"callers"`
	Principals       int `json:"principals"`
}

type baselineFile struct {
	Note       string              `json:"note"`
	Summary    baselineSummary     `json:"summary"`
	Services   []baselineService   `json:"services"`
	Callers    map[string][]string `json:"callers"`
	Principals map[string][]string `json:"principals"`
	RelayHTTP  []baselineRelay     `json:"relay_http"`
}

const writeBaselineEnv = "MOOX_WRITE_ACL_BASELINE"

func TestGenerateACLBaseline(t *testing.T) {
	if os.Getenv(writeBaselineEnv) == "" {
		t.Skip("未设置 " + writeBaselineEnv)
	}
	c, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	out := baselineFile{
		Note:       "批 4 T0 记下的服务目录访问控制基线：callers 是 Allowed(调用方, 服务, 方法) 为真的“服务/方法”集合，principals 是 PrincipalAllowed 为真的集合；host-gateway 是每台主机的网关（签名主机另由签名头给出）。改服务目录后重新生成并人工看差异，访问控制不得变宽。",
		Callers:    map[string][]string{},
		Principals: map[string][]string{},
		RelayHTTP:  []baselineRelay{},
	}
	callers := map[string]bool{"console": true, "moox-cli": true, "access": true, hostGatewayCaller: true}
	type pair struct{ path, method string }
	all := []pair{}
	for _, component := range c.Components {
		callers[component.ID] = true
		if component.RelayHTTP != nil {
			relayCallers, _ := c.RelayHTTPCallers(component.RelayHTTP.Port)
			relayCallers = slices.Clone(relayCallers)
			sort.Strings(relayCallers)
			out.RelayHTTP = append(out.RelayHTTP, baselineRelay{Component: component.ID, Port: component.RelayHTTP.Port, Callers: relayCallers})
		}
		for _, service := range component.Services {
			s := baselineService{Path: service.Path, Component: component.ID, ConsoleName: service.ConsoleName}
			methods := service.MethodNames()
			sort.Strings(methods)
			for _, method := range methods {
				m := baselineMethod{Name: method, ReadOnly: c.ReadOnly(service.Path, method), Retryable: c.Retryable(service.Path, method), TimeoutMS: service.TimeoutFor(method)}
				s.Methods = append(s.Methods, m)
				all = append(all, pair{service.Path, method})
				out.Summary.Methods++
				if m.ReadOnly {
					out.Summary.ReadOnlyMethods++
				}
				if m.Retryable {
					out.Summary.RetryableMethods++
				}
			}
			for _, method := range service.Methods {
				for _, caller := range method.Callers {
					callers[caller] = true
				}
			}
			out.Services = append(out.Services, s)
		}
	}
	for _, principal := range c.ExternalIdentities() {
		callers[principal] = true
	}
	sort.Slice(out.Services, func(i, j int) bool { return out.Services[i].Path < out.Services[j].Path })
	sort.Slice(out.RelayHTTP, func(i, j int) bool { return out.RelayHTTP[i].Port < out.RelayHTTP[j].Port })
	sort.Slice(all, func(i, j int) bool {
		if all[i].path != all[j].path {
			return all[i].path < all[j].path
		}
		return all[i].method < all[j].method
	})
	for caller := range callers {
		allowed := []string{}
		for _, p := range all {
			if c.Allowed(caller, p.path, p.method) {
				allowed = append(allowed, p.path+"/"+p.method)
			}
		}
		out.Callers[caller] = allowed
	}
	for _, principal := range c.ExternalIdentities() {
		allowed := []string{}
		for _, p := range all {
			if c.PrincipalAllowed(principal, p.path, p.method) {
				allowed = append(allowed, p.path+"/"+p.method)
			}
		}
		out.Principals[principal] = allowed
	}
	out.Summary.Components = len(c.Components)
	out.Summary.Services = len(out.Services)
	out.Summary.Callers = len(out.Callers)
	out.Summary.Principals = len(out.Principals)
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("testdata", "acl-baseline.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// compileBaselineTopology 是编译基线的固定拓扑：routes_test.go 的三主机拓扑，再加上已有测试里放过的 control 上的 archive-mgr
// 与只能出站的因子机（因子引擎、归档 worker）。三主机拓扑没有出站主机，中转许可是空的，也缺这三个组件的路由，所以补上。
func compileBaselineTopology() Topology {
	t := productionTopology()
	t.Hosts = append(t.Hosts, Host{ID: "factor", Address: "192.168.0.103", Status: Enabled, Reachability: ReachabilityOutbound})
	t.Deployments = append(t.Deployments, Deployment{HostID: "control", ComponentID: "archive-mgr", Status: Enabled})
	for _, id := range []string{"host-gateway", "host-agent", "factor-engine", "archive-worker"} {
		t.Deployments = append(t.Deployments, Deployment{HostID: "factor", ComponentID: id, Status: Enabled})
	}
	// productionTopology 按 map 遍历生成部署，顺序不固定；编译结果与顺序无关，排序只为了基线文件稳定。
	sort.Slice(t.Deployments, func(i, j int) bool {
		if t.Deployments[i].HostID != t.Deployments[j].HostID {
			return t.Deployments[i].HostID < t.Deployments[j].HostID
		}
		return t.Deployments[i].ComponentID < t.Deployments[j].ComponentID
	})
	return t
}

type compileBaselineRoute struct {
	Service      string   `json:"service"`
	Method       string   `json:"method"`
	Component    string   `json:"component"`
	Port         int      `json:"port"`
	MaxBodyBytes int64    `json:"max_body_bytes"`
	TimeoutMS    int64    `json:"timeout_ms"`
	ReadOnly     bool     `json:"read_only"`
	Callers      []string `json:"callers"`
}

type compileBaselineHost struct {
	HostID              string                 `json:"host_id"`
	VerificationCallers []string               `json:"verification_callers"`
	Relays              []RelayTarget          `json:"relays"`
	Routes              []compileBaselineRoute `json:"routes"`
}

type compileBaselineFile struct {
	Note     string                `json:"note"`
	Topology Topology              `json:"topology"`
	Hosts    []compileBaselineHost `json:"hosts"`
}

// compileBaseline 按固定拓扑逐台主机编译，取出基线关心的字段。
func compileBaseline(c Catalog) (compileBaselineFile, error) {
	topology := compileBaselineTopology()
	out := compileBaselineFile{
		Note:     "批 4 记下的路由编译基线：compileBaselineTopology 下每台主机 Compile 的路由（服务、方法、端口、包体上限、超时、调用方）、校验名单与中转许可。生成与用法见 baseline_gen_test.go。",
		Topology: topology,
	}
	for _, host := range topology.Hosts {
		compiled, err := c.Compile(topology, host.ID)
		if err != nil {
			return compileBaselineFile{}, err
		}
		entry := compileBaselineHost{HostID: host.ID, VerificationCallers: compiled.VerificationCallers, Relays: compiled.Relays, Routes: []compileBaselineRoute{}}
		if entry.Relays == nil {
			entry.Relays = []RelayTarget{}
		}
		for _, route := range compiled.Routes {
			_, rawPort, err := net.SplitHostPort(route.Address)
			if err != nil {
				return compileBaselineFile{}, err
			}
			port, err := strconv.Atoi(rawPort)
			if err != nil {
				return compileBaselineFile{}, err
			}
			entry.Routes = append(entry.Routes, compileBaselineRoute{Service: route.ServicePath, Method: route.Method, Component: route.ComponentID, Port: port,
				MaxBodyBytes: route.MaxBodyBytes, TimeoutMS: route.TimeoutMS, ReadOnly: route.ReadOnly, Callers: route.Callers})
		}
		out.Hosts = append(out.Hosts, entry)
	}
	return out, nil
}

// encodeCompileBaseline 输出缩进的 JSON，但主机、部署和路由各占一行，差异按条逐行可读。
func encodeCompileBaseline(f compileBaselineFile) ([]byte, error) {
	var b bytes.Buffer
	var failed error
	comma := func(last bool) {
		if !last {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	field := func(indent, key string, value any, last bool) {
		raw, err := json.MarshalIndent(value, indent, "  ")
		if err != nil {
			failed = err
		}
		fmt.Fprintf(&b, "%s%q: %s", indent, key, raw)
		comma(last)
	}
	lines := func(indent, key string, n int, item func(int) any, last bool) {
		fmt.Fprintf(&b, "%s%q: [\n", indent, key)
		for i := 0; i < n; i++ {
			raw, err := json.Marshal(item(i))
			if err != nil {
				failed = err
			}
			b.WriteString(indent + "  ")
			b.Write(raw)
			comma(i == n-1)
		}
		b.WriteString(indent + "]")
		comma(last)
	}
	b.WriteString("{\n")
	field("  ", "note", f.Note, false)
	b.WriteString("  \"topology\": {\n")
	field("    ", "control_host_id", f.Topology.ControlHostID, false)
	lines("    ", "hosts", len(f.Topology.Hosts), func(i int) any { return f.Topology.Hosts[i] }, false)
	lines("    ", "deployments", len(f.Topology.Deployments), func(i int) any { return f.Topology.Deployments[i] }, true)
	b.WriteString("  },\n  \"hosts\": [\n")
	for i, host := range f.Hosts {
		b.WriteString("    {\n")
		field("      ", "host_id", host.HostID, false)
		field("      ", "verification_callers", host.VerificationCallers, false)
		field("      ", "relays", host.Relays, false)
		lines("      ", "routes", len(host.Routes), func(j int) any { return host.Routes[j] }, true)
		b.WriteString("    }")
		comma(i == len(f.Hosts)-1)
	}
	b.WriteString("  ]\n}\n")
	return b.Bytes(), failed
}

func TestGenerateCompileBaseline(t *testing.T) {
	if os.Getenv(writeBaselineEnv) == "" {
		t.Skip("未设置 " + writeBaselineEnv)
	}
	c, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := compileBaseline(c)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := encodeCompileBaseline(baseline)
	if err != nil {
		t.Fatal(err)
	}
	var check compileBaselineFile
	if err := json.Unmarshal(raw, &check); err != nil {
		t.Fatalf("生成的编译基线不是合法 JSON：%v", err)
	}
	if err := os.WriteFile(filepath.Join("testdata", "compile-baseline.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}
