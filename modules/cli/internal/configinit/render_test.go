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
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
)

// sampleRequest 是三台独立的腾讯云服务器（存储、管理台、海外），不配因子计算机（地址用文档保留的测试网段）。
func sampleRequest() Request {
	var request Request
	request.Admin.Username = "admin"
	request.Admin.Password = "Adm1n-P@ss"
	request.Tencent = Keys{SecretID: "AKIDexample", SecretKey: "key-example"}
	request.Notification.ChannelType = "wecom"
	request.Notification.WebhookURL = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abc"
	request.Servers.Storage = &Server{Address: "198.51.100.11", SSHUser: "ubuntu", SSHPassword: "pw-storage"}
	request.Servers.Control = &Server{Address: "198.51.100.10", SSHUser: "ubuntu", SSHPassword: "pw-control"}
	request.Servers.Overseas = &Server{Address: "198.51.100.12", SSHUser: "ubuntu", SSHPassword: "pw-compute"}
	return request
}

func renderWith(t *testing.T, request Request, prober Prober) (setupconfig.Manifest, string) {
	t.Helper()
	if prober == nil {
		prober = &fakeProber{report: linuxReport()}
	}
	raw, err := Render(context.Background(), &request, testEnv(newFakeCloud(), prober))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := setupconfig.Parse(raw)
	if err != nil {
		t.Fatalf("渲染结果应通过 CLI 自己的校验: %v", err)
	}
	return manifest, string(raw)
}

func render(t *testing.T, request Request) setupconfig.Manifest {
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
	if manifest.Notification.WebhookURL != "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abc" {
		t.Fatalf("通知 = %+v", manifest.Notification)
	}
	if len(manifest.HostCatalog) != 3 || manifest.HostCatalog["storage"].SSH.Password != "pw-storage" || manifest.HostCatalog["control"].Address != "198.51.100.10" {
		t.Fatalf("主机 = %+v", manifest.HostCatalog)
	}
	// 角色到组件的映射与示例模板的放置一致。
	if !slices.Equal(manifest.Placements["control"], []string{"console-proxy", "web-host", "admin", "eventbus", "monitor", "collector", "cloudnode", "factor-mgr", "strategy"}) ||
		!slices.Equal(manifest.Placements["storage"], []string{"storage-primary", "storage-node", "storage-view", "access"}) ||
		!slices.Equal(manifest.Placements["overseas"], []string{"trade", "access", "egress-proxy"}) {
		t.Fatalf("放置 = %+v", manifest.Placements)
	}
	if manifest.EventBus.PublicAddress != "198.51.100.10" {
		t.Fatalf("EventBus 地址应由 eventbus 所在主机推出，实际 %q", manifest.EventBus.PublicAddress)
	}
	if manifest.HasCompileHost() || manifest.FactorEngineRef.Host != "" {
		t.Fatal("没配因子计算机时不应有编译机或因子引擎位置")
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
	if manifest.TencentCloud.Region != "ap-guangzhou" {
		t.Fatalf("腾讯云默认地域应取管理台所在地域，实际 %q", manifest.TencentCloud.Region)
	}
	request := sampleRequest()
	request.Servers.Control = &Server{SharedWith: RoleOverseas}
	if got := render(t, request).TencentCloud.Region; got != "ap-hongkong" {
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
	_, text := renderWith(t, sampleRequest(), nil)
	for _, want := range []string{"[storage_retention]", "[egress_proxy.dns]", "[local_logs]", "# 告警推送渠道，初始化前必填", "# View 只按根数保留", "[factors]"} {
		if !strings.Contains(text, want) {
			t.Fatalf("应保留示例里的 %q", want)
		}
	}
	for _, leftover := range []string{"203.0.113.", "192.0.2.30", "[hosts.factor]", "[compile_host]", "[factor_engine]"} {
		if strings.Contains(text, leftover) {
			t.Fatalf("不应残留示例里的 %q", leftover)
		}
	}
}

// 只做 A 股、港股、美股时可以不配海外服务器。
func TestOverseasServerIsOptional(t *testing.T) {
	request := sampleRequest()
	request.Servers.Overseas = nil
	manifest := render(t, request)
	if _, ok := manifest.HostCatalog["overseas"]; ok || manifest.PlacementHost("trade") != "" || manifest.PlacementHost("egress-proxy") != "" {
		t.Fatalf("不配海外服务器时不应有交易与出口代理: %+v", manifest.Placements)
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
		if manifest.PlacementHost("admin") != target.host || manifest.PlacementHost("console-proxy") != target.host {
			t.Fatalf("%s：管理台组件应放到 %s，实际 %+v", target.role, target.host, manifest.Placements)
		}
		if manifest.HostCatalog[target.host].TLSMode != "auto" {
			t.Fatalf("%s：管理台的 HTTPS 入口应跟着搬过去，证书模式 %q", target.role, manifest.HostCatalog[target.host].TLSMode)
		}
		if manifest.EventBus.PublicAddress != manifest.HostCatalog[target.host].Address {
			t.Fatalf("%s：EventBus 地址应是共用主机的地址", target.role)
		}
	}
}

// Linux 因子计算机同时作为编译机；它只登记登录方式，不是部署主机。
func TestLinuxFactorMachineIsAlsoTheCompileHost(t *testing.T) {
	request := sampleRequest()
	request.Servers.Factor = &FactorServer{Mode: FactorRemote, Address: "192.168.0.103", SSHPort: 2222, SSHUser: "builder", SSHPassword: "pw-build"}
	manifest := render(t, request)
	if !manifest.HasCompileHost() || manifest.CompileHost().Name != "factor" || manifest.CompileHost().Port != 2222 || manifest.CompileHost().Password != "pw-build" {
		t.Fatalf("编译机 = %+v", manifest.CompileHost())
	}
	if manifest.FactorEngineRef.Host != "factor" || manifest.FactorEngineHost().Address != "192.168.0.103" {
		t.Fatalf("因子引擎位置 = %+v", manifest.FactorEngineRef)
	}
	if host := manifest.HostCatalog["factor"]; host.Region != "" || host.Provider != "" {
		t.Fatalf("因子计算机不是腾讯云服务器，不应写地域和云厂商: %+v", host)
	}
	for _, host := range manifest.Hosts() {
		if host.Name == "factor" {
			t.Fatal("因子计算机不应成为部署主机")
		}
	}
}

func TestNonLinuxFactorMachineIsRecordedButNotACompileHost(t *testing.T) {
	request := sampleRequest()
	request.Servers.Factor = &FactorServer{Mode: FactorRemote, Address: "192.168.0.20", SSHUser: "me", SSHPassword: "pw"}
	manifest, _ := renderWith(t, request, &fakeProber{report: ProbeReport{OK: true, OS: "darwin"}})
	if manifest.HasCompileHost() {
		t.Fatal("macOS 不能作为 Linux 编译机")
	}
	if manifest.FactorEngineHost().Address != "192.168.0.20" {
		t.Fatalf("因子计算机应登记在 hosts 里: %+v", manifest.HostCatalog)
	}
}

func TestLocalFactorMachineOnlyRecordsLocal(t *testing.T) {
	request := sampleRequest()
	request.Servers.Factor = &FactorServer{Mode: FactorLocal, Address: "ignored", SSHPassword: "ignored"}
	prober := &fakeProber{report: ProbeReport{OK: true, OS: "darwin"}}
	manifest, text := renderWith(t, request, prober)
	if !manifest.FactorEngineIsLocal() || manifest.HasCompileHost() {
		t.Fatalf("本机应只记 local: %+v", manifest.FactorEngineRef)
	}
	if strings.Contains(text, "ignored") || strings.Contains(text, "[hosts.factor]") {
		t.Fatal("本机模式不应写入地址和密码")
	}
	if len(prober.targets) != 1 || prober.targets[0].Mode != FactorLocal {
		t.Fatalf("提交时应检测本机环境: %+v", prober.targets)
	}
}

func TestFactorMachineMustPassTheEnvironmentCheck(t *testing.T) {
	request := sampleRequest()
	request.Servers.Factor = &FactorServer{Mode: FactorRemote, Address: "192.168.0.103", SSHUser: "u", SSHPassword: "p"}
	failing := &fakeProber{report: ProbeReport{OK: false, Items: []CheckItem{{Name: "内存", Status: CheckFail, Detail: "内存不足"}}}}
	if err := renderErr(request, failing); err == nil || !strings.Contains(err.Error(), "内存") {
		t.Fatalf("环境检测失败时应拒绝并说明原因，实际 %v", err)
	}
	unknown := &fakeProber{report: linuxReport(), hostKey: "SHA256:abc"}
	if err := renderErr(request, unknown); err == nil || !strings.Contains(err.Error(), "主机指纹") {
		t.Fatalf("没确认指纹时应提示，实际 %v", err)
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
	request.Servers.Factor = &FactorServer{Mode: FactorRemote, Address: "198.51.100.11", SSHUser: "u", SSHPassword: "p"}
	if err := renderErr(request, nil); err == nil || !strings.Contains(err.Error(), "因子计算机") {
		t.Fatalf("因子计算机和存储服务器同 IP 时应拒绝，实际 %v", err)
	}
}

func TestRenderLeavesEverythingAfterTheTopologyUntouched(t *testing.T) {
	_, got := renderWith(t, sampleRequest(), nil)
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
		request.Servers.Control.SSHPassword = secret
		request.Tencent.SecretKey = secret
		_, raw := renderWith(t, request, nil)
		var decoded struct {
			Admin   struct{ Password string }
			Tencent struct {
				SecretKey string `toml:"secret_key"`
			} `toml:"tencent_cloud"`
			Hosts map[string]struct {
				SSH struct{ Password string }
			}
		}
		if _, err := toml.Decode(raw, &decoded); err != nil {
			t.Fatalf("%q: 渲染结果不是合法 TOML: %v", secret, err)
		}
		if decoded.Admin.Password != secret || decoded.Tencent.SecretKey != secret || decoded.Hosts["control"].SSH.Password != secret {
			t.Fatalf("%q 没有原样往返: %+v", secret, decoded)
		}
	}
}

func TestRenderRejectsInputThatCouldBreakTheFile(t *testing.T) {
	cases := map[string]func(*Request){
		"管理员密码含换行":        func(r *Request) { r.Admin.Password = "a\nb" },
		"SSH 密码含换行":       func(r *Request) { r.Servers.Control.SSHPassword = "x\r\n[hosts.evil]" },
		"SSH 密码含制表符":      func(r *Request) { r.Servers.Control.SSHPassword = "a\tb" },
		"SecretKey 含控制字符": func(r *Request) { r.Tencent.SecretKey = "a\x00b" },
		"管理员密码为空":         func(r *Request) { r.Admin.Password = "" },
		"缺少存储服务器":         func(r *Request) { r.Servers.Storage = nil },
		"缺少管理台服务器":        func(r *Request) { r.Servers.Control = nil },
		"存储服务器不能共用":       func(r *Request) { r.Servers.Storage.SharedWith = RoleOverseas },
		"管理台共用未知角色":       func(r *Request) { r.Servers.Control.SharedWith = "nowhere" },
		"管理台不能与因子机共用":     func(r *Request) { r.Servers.Control.SharedWith = RoleFactor },
		"因子机位置未知":         func(r *Request) { r.Servers.Factor = &FactorServer{Mode: "cloud"} },
		"因子机缺密码": func(r *Request) {
			r.Servers.Factor = &FactorServer{Mode: FactorRemote, Address: "192.168.0.9", SSHUser: "u"}
		},
		"SSH 端口越界":       func(r *Request) { r.Servers.Storage.SSHPort = 70000 },
		"webhook 不是官方域名": func(r *Request) { r.Notification.WebhookURL = "https://evil.example/x" },
		"管理员密码超过 72 字节":  func(r *Request) { r.Admin.Password = strings.Repeat("x", 73) },
		"字段超长":           func(r *Request) { r.Servers.Control.SSHUser = strings.Repeat("a", 600) },
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

// 模板必须与仓库根目录的示例保持一致；示例改版而模板没同步时，这里先报警。
func TestTemplateMatchesTheRepositoryExample(t *testing.T) {
	root, err := filepath.Abs("../../../../moox.toml.example")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(root)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != exampleTemplate {
		t.Fatal("internal/configinit/moox.toml.example 与仓库根目录的 moox.toml.example 不一致：改了示例后要同步复制一份")
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
