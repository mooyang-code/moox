package configinit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
)

// sampleRequest 是三台独立的腾讯云服务器（存储、管理台、海外），不配因子计算机（地址用文档保留的测试网段）。
func sampleRequest() Request {
	var request Request
	request.Admin.Username = "admin"
	request.Admin.Password = "Adm1n-P@ss"
	request.Tencent = Keys{SecretID: "AKIDexample", SecretKey: "key-example"}
	request.Servers.Storage = &Server{SSHTarget: SSHTarget{Address: "198.51.100.11", SSHUser: "ubuntu"}}
	request.Servers.Control = &Server{SSHTarget: SSHTarget{Address: "198.51.100.10", SSHUser: "ubuntu"}}
	request.Servers.Overseas = &Server{SSHTarget: SSHTarget{Address: "198.51.100.12", SSHUser: "ubuntu"}}
	return request
}

// renderedManifest 把渲染出的两份文件放在一起看：拓扑字段来自 moox.toml，账号与密钥来自 moox.secrets.toml。
type renderedManifest struct {
	*topology.Config
	Admin        topology.Admin
	TencentCloud topology.TencentCloud
}

// parseRendered 用 CLI 自己的严格解码与校验解析两份文件。
func parseRendered(t *testing.T, rendered Rendered) renderedManifest {
	t.Helper()
	pair, err := parsePair(rendered.Config, rendered.Secrets)
	if err != nil {
		t.Fatalf("渲染结果应通过 CLI 自己的校验: %v", err)
	}
	return renderedManifest{Config: &pair.Config, Admin: pair.Secrets.Admin, TencentCloud: pair.Secrets.TencentCloud}
}

func renderPair(t *testing.T, request Request, prober Prober) (renderedManifest, Rendered) {
	t.Helper()
	if prober == nil {
		prober = &fakeProber{report: linuxReport()}
	}
	rendered, err := Render(context.Background(), &request, testEnv(newFakeCloud(), prober))
	if err != nil {
		t.Fatal(err)
	}
	return parseRendered(t, rendered), rendered
}

// renderWith 返回解析结果与 moox.toml 的文本。
func renderWith(t *testing.T, request Request, prober Prober) (renderedManifest, string) {
	t.Helper()
	manifest, rendered := renderPair(t, request, prober)
	return manifest, string(rendered.Config)
}

func render(t *testing.T, request Request) renderedManifest {
	t.Helper()
	manifest, _ := renderWith(t, request, nil)
	return manifest
}

func renderErr(request Request, prober Prober) error {
	if prober == nil {
		prober = &fakeProber{report: linuxReport()}
	}
	_, err := Render(context.Background(), &request, testEnv(newFakeCloud(), prober))
	return err
}

func TestRenderProducesAManifestTheCLIAccepts(t *testing.T) {
	manifest := render(t, sampleRequest())
	if manifest.Admin.Username != "admin" || manifest.Admin.Password != "Adm1n-P@ss" {
		t.Fatalf("管理员 = %+v", manifest.Admin)
	}
	if manifest.TencentCloud.SecretID != "AKIDexample" || manifest.TencentCloud.SecretKey != "key-example" {
		t.Fatalf("云凭据 = %+v", manifest.TencentCloud)
	}
	if len(manifest.HostCatalog) != 3 || manifest.HostCatalog["storage"].SSH.PrivateKeyFile != sshKeyTOMLPath || manifest.HostCatalog["control"].Address != "198.51.100.10" {
		t.Fatalf("主机 = %+v", manifest.HostCatalog)
	}
	// 角色到组件的映射与示例模板的部署一致。
	if !slices.Equal(manifest.Deployments["control"], []string{"admin", "monitor", "collector", "factor-mgr", "archive-mgr", "strategy"}) ||
		!slices.Equal(manifest.Deployments["storage"], []string{"storage-primary", "storage-view", "eventbus"}) ||
		!slices.Equal(manifest.Deployments["overseas"], []string{"trade", "egress-proxy", "collector-stream"}) {
		t.Fatalf("部署 = %+v", manifest.Deployments)
	}
	if manifest.EventBusHost().Address != manifest.HostCatalog["storage"].Address {
		t.Fatalf("EventBus 地址应由 eventbus 所在主机推出，实际 %q", manifest.EventBusHost().Address)
	}
	if _, ok := manifest.HostCatalog["factor"]; ok {
		t.Fatal("没配因子机时不应登记 factor 主机")
	}
}

// 地域、云厂商不让用户填：由公网 IP 在腾讯云里识别；腾讯云默认地域取管理台所在地域。
func TestRegionsComeFromTheCloudNotTheUser(t *testing.T) {
	manifest := render(t, sampleRequest())
	for id, want := range map[string]string{"control": "ap-guangzhou", "storage": "ap-nanjing", "overseas": "ap-hongkong"} {
		host := manifest.HostCatalog[id]
		if host.Region != want || host.Provider != "tencent" {
			t.Fatalf("%s：地域/云厂商 = %q/%q，应为 %q/tencent", id, host.Region, host.Provider, want)
		}
	}
	if manifest.CloudRegion() != "ap-guangzhou" {
		t.Fatalf("腾讯云默认地域应取管理台所在地域，实际 %q", manifest.CloudRegion())
	}
	request := sampleRequest()
	request.Servers.Control = &Server{SharedWith: RoleOverseas}
	if got := render(t, request).CloudRegion(); got != "ap-hongkong" {
		t.Fatalf("管理台和海外服务器共用时，默认地域应为香港，实际 %q", got)
	}
}

func TestServersOutsideTheAccountAreRejected(t *testing.T) {
	request := sampleRequest()
	request.Servers.Storage.Address = "203.0.113.99"
	err := renderErr(request, nil)
	if err == nil || !strings.Contains(err.Error(), "不在当前腾讯云账号下") || !strings.Contains(err.Error(), "数据存储服务器") {
		t.Fatalf("不在账号下的服务器应被拒绝，实际 %v", err)
	}
	request = sampleRequest()
	request.Servers.Storage.Address = "192.168.0.10"
	if err := renderErr(request, nil); err == nil || !strings.Contains(err.Error(), "公网 IPv4") {
		t.Fatalf("腾讯云服务器要填公网 IP，实际 %v", err)
	}
}

func TestRenderKeepsTheExampleDefaultsAndComments(t *testing.T) {
	_, rendered := renderPair(t, sampleRequest(), nil)
	text, secrets := string(rendered.Config), string(rendered.Secrets)
	for _, want := range []string{"[storage_retention]", "[egress_proxy]", "# View 只按根数保留", "[logging.cls]", "# 手工调过的组件配置"} {
		if !strings.Contains(text, want) {
			t.Fatalf("moox.toml 应保留示例里的 %q", want)
		}
	}
	for _, want := range []string{"# 腾讯云账号的管理密钥", "[logging.cls]"} {
		if !strings.Contains(secrets, want) {
			t.Fatalf("moox.secrets.toml 应保留示例里的 %q", want)
		}
	}
	for _, leftover := range []string{"[notification]", "[[exchange_keys]]"} {
		if strings.Contains(secrets, leftover) {
			t.Fatalf("moox.secrets.toml 不应残留 %q", leftover)
		}
	}
	for _, leftover := range []string{"203.0.113.", "192.0.2.30", "[hosts.factor]", "[factor_engine]", `"factor-engine", "archive-worker"`, "[factors]", "[local_logs]", "[admin]"} {
		if strings.Contains(text, leftover) {
			t.Fatalf("moox.toml 不应残留 %q", leftover)
		}
	}
}

// 两份文件分开：页面提交的每个秘密值都只在 moox.secrets.toml 里，不出现在明文的 moox.toml。
func TestRenderKeepsEverySecretOutOfTheTopologyFile(t *testing.T) {
	request := sampleRequest()
	_, rendered := renderPair(t, request, nil)
	for _, secret := range []string{"Adm1n-P@ss", "AKIDexample", "key-example"} {
		if strings.Contains(string(rendered.Config), secret) {
			t.Fatalf("moox.toml 里出现了秘密 %q", secret)
		}
		if !strings.Contains(string(rendered.Secrets), secret) {
			t.Fatalf("moox.secrets.toml 里应有 %q", secret)
		}
	}
}

// 只做 A 股、港股、美股时可以不配海外服务器。
func TestOverseasServerIsOptional(t *testing.T) {
	request := sampleRequest()
	request.Servers.Overseas = nil
	manifest := render(t, request)
	if _, ok := manifest.HostCatalog["overseas"]; ok || manifest.ComponentHost("trade") != "" || manifest.ComponentHost("egress-proxy") != "" {
		t.Fatalf("不配海外服务器时不应有交易与出口代理: %+v", manifest.Deployments)
	}
	request.Servers.Control = &Server{SharedWith: RoleOverseas}
	if err := renderErr(request, nil); err == nil || !strings.Contains(err.Error(), "没有配置海外服务器") {
		t.Fatalf("没配海外服务器却选择共用，应提示，实际 %v", err)
	}
}

func TestControlCanShareAServerWithStorageOrOverseas(t *testing.T) {
	for _, target := range []struct{ role, host string }{{RoleStorage, "storage"}, {RoleOverseas, "overseas"}} {
		request := sampleRequest()
		// 共用时管理台自己的 IP 和密码不用填。
		request.Servers.Control = &Server{SharedWith: target.role}
		manifest := render(t, request)
		if len(manifest.HostCatalog) != 2 {
			t.Fatalf("%s：应只有两台主机，实际 %v", target.role, manifest.HostCatalog)
		}
		if manifest.ComponentHost("admin") != target.host || manifest.ComponentHost("monitor") != target.host {
			t.Fatalf("%s：管理台组件应放到 %s，实际 %+v", target.role, target.host, manifest.Deployments)
		}
		// EventBus 是独立单元，默认放在存储机上（Storage 内部的事件流量最大），不跟着管理台组件走。
		if manifest.ComponentHost("eventbus") != "storage" || manifest.EventBusHost().Address != manifest.HostCatalog["storage"].Address {
			t.Fatalf("%s：EventBus 应在存储机上，地址是存储机的地址：%+v", target.role, manifest.Deployments)
		}
	}
}

// 因子机是只能出站的普通部署主机，部署 factor-engine 与 archive-worker，带中文名；不是腾讯云服务器，不写地域和云厂商。
func TestFactorMachineIsAnOutboundDeploymentHost(t *testing.T) {
	request := sampleRequest()
	request.Servers.Factor = &SSHTarget{Address: "192.168.0.103", SSHPort: 2222, SSHUser: "builder"}
	manifest := render(t, request)
	host := manifest.HostByID("factor")
	if host.Port != 2222 || host.PrivateKeyFile != sshKeyTOMLPath || host.Reachability != "outbound" || host.Label != "因子机" {
		t.Fatalf("因子机 = %+v", host)
	}
	if definition := manifest.HostCatalog["factor"]; definition.Region != "" || definition.Provider != "" || definition.Label != "因子机" {
		t.Fatalf("因子机不是腾讯云服务器，不应写地域和云厂商，但要写中文名: %+v", definition)
	}
	if !slices.Equal(manifest.Deployments["factor"], []string{"factor-engine", "archive-worker"}) {
		t.Fatalf("因子机应部署两个外部程序: %+v", manifest.Deployments["factor"])
	}
	deployed := false
	for _, h := range manifest.Hosts() {
		deployed = deployed || h.Name == "factor"
	}
	if !deployed {
		t.Fatal("因子机应是部署主机")
	}
}

func TestRenderedHostsCarryChineseLabels(t *testing.T) {
	manifest := render(t, sampleRequest())
	for id, want := range map[string]string{"control": "控制机", "storage": "存储机", "overseas": "海外机"} {
		if got := manifest.HostCatalog[id].Label; got != want {
			t.Fatalf("%s 的中文名 = %q，应为 %q", id, got, want)
		}
	}
}

func TestNonLinuxFactorMachineIsRejected(t *testing.T) {
	request := sampleRequest()
	request.Servers.Factor = &SSHTarget{Address: "192.168.0.20", SSHUser: "me"}
	macOS := &fakeProber{report: ProbeReport{OK: false, OS: "darwin", Items: []CheckItem{{Name: "系统", Status: CheckFail, Detail: "macOS。因子引擎只支持 Linux"}}}}
	if err := renderErr(request, macOS); err == nil || !strings.Contains(err.Error(), "只支持 Linux") {
		t.Fatalf("非 Linux 的因子机应被拒绝并说明原因，实际 %v", err)
	}
}

func TestFactorMachineMustPassTheEnvironmentCheck(t *testing.T) {
	request := sampleRequest()
	request.Servers.Factor = &SSHTarget{Address: "192.168.0.103", SSHUser: "u"}
	failing := &fakeProber{report: ProbeReport{OK: false, Items: []CheckItem{{Name: "内存", Status: CheckFail, Detail: "内存不足"}}}}
	if err := renderErr(request, failing); err == nil || !strings.Contains(err.Error(), "内存") {
		t.Fatalf("环境检测失败时应拒绝并说明原因，实际 %v", err)
	}
	unknown := &fakeProber{report: linuxReport(), hostKey: "SHA256:abc"}
	if err := renderErr(request, unknown); err == nil || !strings.Contains(err.Error(), "免密登录") {
		t.Fatalf("没确认指纹、不能免密登录时应提示，实际 %v", err)
	}
	unreachable := &fakeProber{err: errors.New("连不上 192.168.0.103:22")}
	if err := renderErr(request, unreachable); err == nil || !strings.Contains(err.Error(), "连不上") {
		t.Fatalf("连不上时应报错，实际 %v", err)
	}
}

func TestSameAddressIsExplained(t *testing.T) {
	request := sampleRequest()
	request.Servers.Control.Address = request.Servers.Storage.Address
	if err := renderErr(request, nil); err == nil || !strings.Contains(err.Error(), "选择共用") {
		t.Fatalf("管理台与存储同 IP 时应提示选择共用，实际 %v", err)
	}
	request = sampleRequest()
	request.Servers.Overseas.Address = request.Servers.Storage.Address
	if err := renderErr(request, nil); err == nil || !strings.Contains(err.Error(), "需要分开") {
		t.Fatalf("存储与海外同 IP 时应说明需要分开，实际 %v", err)
	}
	request = sampleRequest()
	request.Servers.Factor = &SSHTarget{Address: "198.51.100.11", SSHUser: "u"}
	if err := renderErr(request, nil); err == nil || !strings.Contains(err.Error(), "因子机") {
		t.Fatalf("因子机和存储服务器同 IP 时应拒绝，实际 %v", err)
	}
}

func TestRenderLeavesEverythingAfterTheTopologyUntouched(t *testing.T) {
	request := sampleRequest()
	// 不配海外机且存储机无 VPC 时不追加 [scf_plan]，拓扑之后的段落应与示例逐字一致。
	request.Servers.Overseas = nil
	_, got := renderWith(t, request, nil)
	marker := "\n[egress_proxy]\n"
	want := exampleTemplate
	gotAt, wantAt := strings.Index(got, marker), strings.Index(want, marker)
	if gotAt < 0 || wantAt < 0 {
		t.Fatalf("找不到 [egress_proxy] 段: 渲染=%d 示例=%d", gotAt, wantAt)
	}
	if got[gotAt:] != want[wantAt:] {
		t.Fatal("拓扑之后的段落应与示例逐字一致")
	}
}

// normalize 已经拒绝控制字符，tomlString 仍要按规范转义（第二道防线）。
func TestTomlStringEscapesControlCharactersAndRoundTrips(t *testing.T) {
	for _, value := range []string{"a\nb", "a\tb", "a\rb", "a\bb", "a\fb", "a\x01b", "a\x7fb", `a"b`, `a\b`, "中文🔒", ""} {
		rendered := tomlString(value)
		if strings.ContainsAny(rendered, "\n\t\r\b\f\x01\x7f") {
			t.Fatalf("%q 渲染后仍含裸控制字符: %q", value, rendered)
		}
		var decoded struct{ V string }
		if _, err := toml.Decode("v = "+rendered, &decoded); err != nil {
			t.Fatalf("%q: 不是合法 TOML: %v", value, err)
		}
		if decoded.V != value {
			t.Fatalf("%q 没有原样往返，得到 %q", value, decoded.V)
		}
	}
}

// 密码里常见的特殊字符必须原样往返，不能破坏 TOML 结构。
func TestRenderEscapesSecretsAndRoundTrips(t *testing.T) {
	secrets := []string{`a"b`, `back\slash`, "中文密码🔒", `#not-a-comment`, `key = "value"`, ` leading and trailing `, `\n literal`, `'single'`, `]`, `$(rm -rf)`}
	for _, secret := range secrets {
		request := sampleRequest()
		request.Admin.Password = secret
		request.Tencent.SecretKey = secret
		_, rendered := renderPair(t, request, nil)
		raw := string(rendered.Secrets)
		var decoded struct {
			Admin   struct{ Password string }
			Tencent struct {
				SecretKey string `toml:"secret_key"`
			} `toml:"tencent_cloud"`
		}
		if _, err := toml.Decode(raw, &decoded); err != nil {
			t.Fatalf("%q: 渲染结果不是合法 TOML: %v", secret, err)
		}
		if decoded.Admin.Password != secret || decoded.Tencent.SecretKey != secret {
			t.Fatalf("%q 没有原样往返: %+v", secret, decoded)
		}
	}
}

func TestRenderRejectsInputThatCouldBreakTheFile(t *testing.T) {
	cases := map[string]func(*Request){
		"管理员密码含换行":        func(r *Request) { r.Admin.Password = "a\nb" },
		"SSH 用户名含换行":      func(r *Request) { r.Servers.Control.SSHUser = "x\r\n[hosts.evil]" },
		"SecretKey 含控制字符": func(r *Request) { r.Tencent.SecretKey = "a\x00b" },
		"管理员密码为空":         func(r *Request) { r.Admin.Password = "" },
		"缺少存储服务器":         func(r *Request) { r.Servers.Storage = nil },
		"缺少管理台服务器":        func(r *Request) { r.Servers.Control = nil },
		"存储服务器不能共用":       func(r *Request) { r.Servers.Storage.SharedWith = RoleOverseas },
		"管理台共用未知角色":       func(r *Request) { r.Servers.Control.SharedWith = "nowhere" },
		"管理台不能与因子机共用":     func(r *Request) { r.Servers.Control.SharedWith = RoleFactor },
		"因子机缺地址":          func(r *Request) { r.Servers.Factor = &SSHTarget{SSHUser: "u"} },
		"SSH 端口越界":        func(r *Request) { r.Servers.Storage.SSHPort = 70000 },
		"管理员密码超过 72 字节":   func(r *Request) { r.Admin.Password = strings.Repeat("x", 73) },
		"字段超长":            func(r *Request) { r.Servers.Control.SSHUser = strings.Repeat("a", 600) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			request := sampleRequest()
			mutate(&request)
			err := renderErr(request, nil)
			if err == nil {
				t.Fatal("应被拒绝")
			}
			for _, secret := range []string{"Adm1n-P@ss", "key-example", "pw-control", "pw-storage", "pw-compute"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("错误信息泄露了秘密 %q: %v", secret, err)
				}
			}
		})
	}
}

// 两份模板直接嵌入自仓库根目录的示例文件，不存在副本。
func TestTemplatesMatchTheRepositoryExamples(t *testing.T) {
	for name, embedded := range map[string]string{"moox.toml.example": exampleTemplate, "moox.secrets.toml.example": secretsTemplate} {
		root, err := filepath.Abs("../../../../" + name)
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.ReadFile(root)
		if err != nil {
			t.Fatal(err)
		}
		if string(want) != embedded {
			t.Fatalf("嵌入的 %s 与仓库根目录的 %s 不一致", name, name)
		}
	}
}

func TestSetKeyFailsLoudlyWhenTheTemplateDrifts(t *testing.T) {
	if _, err := setKey([]string{"[admin]", "username = \"x\""}, "admin", "password", `"p"`); err == nil {
		t.Fatal("模板里没有该键时必须报错")
	}
	if _, err := setKey([]string{"[other]", "password = \"x\""}, "admin", "password", `"p"`); err == nil {
		t.Fatal("别的段里的同名键不能被替换")
	}
	if _, err := replaceTopology([]string{"[admin]"}, nil); err == nil {
		t.Fatal("找不到拓扑段时必须报错")
	}
}

// 配置页能生成的每种拓扑都有外部入口（云函数、Skill 要从它进来），并且不会把只能出站的因子机标成入口。
func TestEveryGeneratedTopologyHasAnExternalEntry(t *testing.T) {
	factorServer := &SSHTarget{Address: "192.0.2.30", SSHUser: "builder"}
	cases := map[string]func(*Request){
		"全部":      func(r *Request) { r.Servers.Factor = factorServer },
		"只有必选主机":  func(r *Request) { r.Servers.Overseas = nil },
		"有因子机没海外": func(r *Request) { r.Servers.Overseas, r.Servers.Factor = nil, factorServer },
		"管理台共用存储": func(r *Request) { r.Servers.Control = &Server{SharedWith: RoleStorage} },
		"管理台共用海外": func(r *Request) { r.Servers.Control = &Server{SharedWith: RoleOverseas} },
	}
	for name, change := range cases {
		request := sampleRequest()
		change(&request)
		manifest := render(t, request)
		if entry := manifest.EntryHost(); entry.Name != manifest.StorageHost().Name || entry.Name == "" {
			t.Fatalf("%s: 默认外部入口应是存储机，得到 %q", name, entry.Name)
		}
		for _, host := range manifest.Hosts() {
			if host.ExternalEntry && host.Reachability != "" {
				t.Fatalf("%s: 只能出站的主机 %s 被标成了外部入口", name, host.Name)
			}
			if slices.Contains(manifest.Deployments[host.Name], "access") {
				t.Fatalf("%s: 生成的配置里还有已删除的 access 组件", name)
			}
		}
	}
}

// 存储服务器是带私有网络的云服务器且配了海外机：按第一步密钥的 AppId 与存储服务器的 VPC、子网、私网 IP 生成 A 股（VPC）
// 以及 crypto（首尔、东京公网地域直写存储机 11004）的 [scf_plan]。
func TestRenderPlansStockCNCloudFunctionsInTheStorageVPC(t *testing.T) {
	cloud := newFakeCloud()
	cloud.instances["198.51.100.11"] = Instance{Kind: "cvm", KindName: "云服务器", Region: "ap-nanjing", RegionName: "南京", Name: "storage",
		VpcID: "vpc-abc", SubnetID: "subnet-def", PrivateIP: "10.0.0.5"}
	rendered, err := Render(context.Background(), ptr(sampleRequest()), testEnv(cloud, &fakeProber{report: linuxReport()}))
	if err != nil {
		t.Fatal(err)
	}
	manifest := parseRendered(t, rendered)
	raw := rendered.Config
	if !manifest.SCFEnabled() {
		t.Fatal("应打开 [scf_plan]")
	}
	account := manifest.SCFCloudAccount()
	if account.AppID != "1250000001" || account.CredentialSecretID != "tencent-default" || account.COSRegion != "ap-guangzhou" || account.COSBucket != "moox-scf-guangzhou-1250000001" {
		t.Fatalf("云账号 = %+v", account)
	}
	region := manifest.SCFPlan.Regions["ap-nanjing"]
	if region.VPCID != "vpc-abc" || region.SubnetID != "subnet-def" || region.EntryHost != "storage" {
		t.Fatalf("地域网络 = %+v", region)
	}
	for _, r := range []string{"ap-seoul", "ap-tokyo"} {
		pub := manifest.SCFPlan.Regions[r]
		if pub.Network != topology.SCFNetworkPublic || pub.EntryHost != "storage" || pub.VPCID != "" || pub.SubnetID != "" {
			t.Fatalf("crypto 公网地域 %s = %+v", r, pub)
		}
	}
	spaceByID := map[string]topology.SCFPlanSpace{}
	for _, sp := range manifest.SCFPlan.Spaces {
		spaceByID[sp.SpaceID] = sp
	}
	if len(spaceByID) != 2 ||
		!slices.Equal(spaceByID["stockcn"].Markets["equity"], []string{"ap-nanjing"}) ||
		!slices.Equal(spaceByID["crypto"].Markets["spot"], []string{"ap-seoul", "ap-tokyo"}) ||
		!slices.Equal(spaceByID["crypto"].Markets["swap"], []string{"ap-seoul", "ap-tokyo"}) {
		t.Fatalf("空间 = %+v", manifest.SCFPlan.Spaces)
	}
	if !strings.Contains(string(raw), `private_address = "10.0.0.5"`) {
		t.Fatal("存储服务器应写入私网地址")
	}
}

// 存储服务器是轻量应用服务器（识别不到可绑定的 VPC）：
// - 配了海外机时，自动为 crypto 生成首尔、东京公网地域（直写存储机 11004，不需要 VPC 或私网地址），不生成 stockcn；
// - 没配海外机时，不生成 [scf_plan]，保持关闭。
func TestRenderLeavesCloudFunctionsOffWithoutStorageVPC(t *testing.T) {
	manifest, raw := renderWith(t, sampleRequest(), nil)
	if !manifest.SCFEnabled() {
		t.Fatal("配了海外机时应自动打开 [scf_plan] 为 crypto 配海外公网地域")
	}
	if len(manifest.SCFPlan.Spaces) != 1 || manifest.SCFPlan.Spaces[0].SpaceID != "crypto" {
		t.Fatalf("无 VPC 时只应生成 crypto 空间: %+v", manifest.SCFPlan.Spaces)
	}
	if strings.Contains(raw, "private_address = ") {
		t.Fatal("无 VPC 空间时不应写入 private_address")
	}

	noOverseas := sampleRequest()
	noOverseas.Servers.Overseas = nil
	if render(t, noOverseas).SCFEnabled() {
		t.Fatal("没有 VPC 且没配海外机时不应打开 [scf_plan]")
	}
}

func ptr[T any](value T) *T { return &value }

// 只给存储服务器写私网地址；管理台与存储同地域却不在同一个 VPC 时不生成 stockcn 也不写私网地址；拿不到 AppId 时不生成任何云函数计划。
func TestRenderCloudFunctionPlanGuards(t *testing.T) {
	storageCVM := Instance{Kind: "cvm", KindName: "云服务器", Region: "ap-nanjing", Name: "storage", VpcID: "vpc-abc", SubnetID: "subnet-def", PrivateIP: "10.0.0.5"}
	renderCloud := func(cloud *fakeCloud) (renderedManifest, string) {
		rendered, err := Render(context.Background(), ptr(sampleRequest()), testEnv(cloud, &fakeProber{report: linuxReport()}))
		if err != nil {
			t.Fatal(err)
		}
		return parseRendered(t, rendered), string(rendered.Config)
	}

	cloud := newFakeCloud()
	cloud.instances["198.51.100.11"] = storageCVM
	cloud.instances["198.51.100.10"] = Instance{Kind: "cvm", Region: "ap-guangzhou", Name: "control", VpcID: "vpc-gz", SubnetID: "subnet-gz", PrivateIP: "10.1.0.2"}
	manifest, raw := renderCloud(cloud)
	if !manifest.SCFEnabled() || strings.Count(raw, "private_address = ") != 1 {
		t.Fatalf("只有存储服务器写私网地址：%d 处", strings.Count(raw, "private_address = "))
	}

	sameRegion := newFakeCloud()
	sameRegion.instances["198.51.100.11"] = storageCVM
	sameRegion.instances["198.51.100.10"] = Instance{Kind: "lighthouse", Region: "ap-nanjing", Name: "control", PrivateIP: "10.9.0.2"}
	manifest, raw = renderCloud(sameRegion)
	if strings.Contains(raw, "private_address = ") {
		t.Fatal("管理台与存储同地域但不在同一 VPC 时不能写私网地址")
	}
	for _, sp := range manifest.SCFPlan.Spaces {
		if sp.SpaceID == "stockcn" {
			t.Fatal("管理台与存储同地域但不在同一 VPC 时不生成 stockcn 计划")
		}
	}

	noAppID := newFakeCloud()
	noAppID.instances["198.51.100.11"] = storageCVM
	noAppID.noAppID = true
	if manifest, _ = renderCloud(noAppID); manifest.SCFEnabled() {
		t.Fatal("拿不到 AppId 时不生成计划")
	}
}

// 入口主机优先选存储服务器（同地域走内网）；存储服务器是轻量服务器没有可用 VPC 时，stockcn 退而选海外服务器的外部入口，crypto 仍走公网直连存储机。
func TestRenderPrefersStorageRegionForCloudFunctions(t *testing.T) {
	plan := func(cloud *fakeCloud) renderedManifest {
		rendered, err := Render(context.Background(), ptr(sampleRequest()), testEnv(cloud, &fakeProber{report: linuxReport()}))
		if err != nil {
			t.Fatal(err)
		}
		return parseRendered(t, rendered)
	}
	spaceMarkets := func(m renderedManifest, spaceID string) map[string][]string {
		for _, sp := range m.SCFPlan.Spaces {
			if sp.SpaceID == spaceID {
				return sp.Markets
			}
		}
		return nil
	}
	both := newFakeCloud()
	both.instances["198.51.100.11"] = Instance{Kind: "cvm", Region: "ap-nanjing", Name: "storage", VpcID: "vpc-nj", SubnetID: "subnet-nj", PrivateIP: "10.0.0.5"}
	both.instances["198.51.100.12"] = Instance{Kind: "cvm", Region: "ap-hongkong", Name: "hk", VpcID: "vpc-hk", SubnetID: "subnet-hk", PrivateIP: "172.19.0.5"}
	manifest := plan(both)
	if got := spaceMarkets(manifest, "stockcn")["equity"]; !slices.Equal(got, []string{"ap-nanjing"}) || manifest.SCFPlan.Regions["ap-nanjing"].EntryHost != "storage" {
		t.Fatalf("存储服务器可用时应选它所在地域：%v", got)
	}

	fallback := newFakeCloud() // 存储服务器是轻量服务器（没有 VPC）
	fallback.instances["198.51.100.12"] = Instance{Kind: "cvm", Region: "ap-hongkong", Name: "hk", VpcID: "vpc-hk", SubnetID: "subnet-hk", PrivateIP: "172.19.0.5"}
	manifest = plan(fallback)
	if !manifest.SCFEnabled() || manifest.SCFPlan.Regions["ap-hongkong"].EntryHost != "overseas" || !slices.Equal(spaceMarkets(manifest, "stockcn")["equity"], []string{"ap-hongkong"}) {
		t.Fatalf("存储服务器不可用时应退到海外服务器：%+v", manifest.SCFPlan)
	}
	if !slices.Equal(spaceMarkets(manifest, "crypto")["spot"], []string{"ap-seoul", "ap-tokyo"}) || manifest.SCFPlan.Regions["ap-seoul"].EntryHost != "storage" {
		t.Fatalf("crypto 仍应走公网直连存储机：%+v", manifest.SCFPlan)
	}
}

// 初始化时没填日志保留天数就是默认的 2 天；填了就写进 [logging]；超出范围的拒绝。
func TestRenderWritesLogRetentionDays(t *testing.T) {
	if got := render(t, sampleRequest()).Logging.RetentionDays; got != 2 {
		t.Fatalf("默认保留天数 = %d，应为 2", got)
	}
	request := sampleRequest()
	request.Logging.RetentionDays = 7
	if got := render(t, request).Logging.RetentionDays; got != 7 {
		t.Fatalf("保留天数 = %d，应为 7", got)
	}
	for _, days := range []int64{-1, 3601} {
		request := sampleRequest()
		request.Logging.RetentionDays = days
		if err := renderErr(request, nil); err == nil {
			t.Fatalf("保留天数 %d 应被拒绝", days)
		}
	}
}
