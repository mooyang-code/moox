package configinit

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
)

// sampleRequest 是三台独立服务器（存储、海外、管理台）的请求，不配因子执行服务器（地址用文档保留的测试网段）。
func sampleRequest() Request {
	var request Request
	request.Admin.Username = "admin"
	request.Admin.Password = "Adm1n-P@ss"
	request.Tencent.SecretID = "AKIDexample"
	request.Tencent.SecretKey = "key-example"
	request.Tencent.Region = "ap-guangzhou"
	request.Notification.ChannelType = "wecom"
	request.Notification.WebhookURL = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abc"
	request.Servers.Storage = &Server{ID: "storage", Address: "198.51.100.11", Region: "ap-nanjing", Provider: "tencent", TLSMode: "auto", SSHUser: "ubuntu", SSHPassword: "pw-storage"}
	request.Servers.Overseas = &Server{ID: "compute-1", Address: "198.51.100.12", Region: "ap-hongkong", Provider: "tencent", SSHUser: "ubuntu", SSHPassword: "pw-compute"}
	request.Servers.Control = &Server{ID: "control", Address: "198.51.100.10", Region: "ap-guangzhou", Provider: "tencent", TLSMode: "auto", SSHUser: "ubuntu", SSHPassword: "pw-control"}
	return request
}

func render(t *testing.T, request Request) setupconfig.Manifest {
	t.Helper()
	raw, err := Render(&request)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := setupconfig.Parse(raw)
	if err != nil {
		t.Fatalf("渲染结果应通过 CLI 自己的校验: %v", err)
	}
	return manifest
}

func TestRenderProducesAManifestTheCLIAccepts(t *testing.T) {
	manifest := render(t, sampleRequest())
	if manifest.Admin.Username != "admin" || manifest.Admin.Password != "Adm1n-P@ss" {
		t.Fatalf("管理员 = %+v", manifest.Admin)
	}
	if manifest.TencentCloud.SecretID != "AKIDexample" || manifest.TencentCloud.SecretKey != "key-example" || manifest.TencentCloud.Region != "ap-guangzhou" {
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
		!slices.Equal(manifest.Placements["compute-1"], []string{"trade", "access", "egress-proxy"}) {
		t.Fatalf("放置 = %+v", manifest.Placements)
	}
	if manifest.HasCompileHost() {
		t.Fatal("没配因子执行服务器时不应有编译机")
	}
	if manifest.EventBus.PublicAddress != "198.51.100.10" {
		t.Fatalf("EventBus 地址应由 eventbus 所在主机推出，实际 %q", manifest.EventBus.PublicAddress)
	}
}

func TestRenderKeepsTheExampleDefaultsAndComments(t *testing.T) {
	request := sampleRequest()
	raw, err := Render(&request)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"[storage_retention]", "[egress_proxy.dns]", "[local_logs]", "# 告警推送渠道，初始化前必填", "# View 只按根数保留", "[factors]"} {
		if !strings.Contains(text, want) {
			t.Fatalf("应保留示例里的 %q", want)
		}
	}
	for _, leftover := range []string{"203.0.113.", "192.0.2.30", "[hosts.compile]", "[compile_host]"} {
		if strings.Contains(text, leftover) {
			t.Fatalf("不应残留示例里的 %q", leftover)
		}
	}
}

func TestFactorServerBecomesTheCompileHostAndAcceptsALANAddress(t *testing.T) {
	request := sampleRequest()
	request.Servers.Factor = &Server{Address: "192.168.0.103", SSHPort: 2222, SSHUser: "builder", SSHPassword: "pw-build"}
	manifest := render(t, request)
	if !manifest.HasCompileHost() || manifest.CompileHost().Name != "factor" || manifest.CompileHost().Port != 2222 || manifest.CompileHost().Password != "pw-build" {
		t.Fatalf("编译机 = %+v", manifest.CompileHost())
	}
	if manifest.HostCatalog["factor"].Address != "192.168.0.103" {
		t.Fatalf("因子执行服务器 = %+v", manifest.HostCatalog["factor"])
	}
	if _, placed := manifest.Placements["factor"]; placed {
		t.Fatal("因子执行服务器不放置 MooX 组件")
	}
}

func TestControlCanShareAServerWithStorageOrOverseas(t *testing.T) {
	for _, target := range []struct{ role, host string }{{RoleStorage, "storage"}, {RoleOverseas, "compute-1"}} {
		request := sampleRequest()
		// 共用时管理台自己的地址和密码不用填，填了也忽略。
		request.Servers.Control = &Server{SharedWith: target.role, TLSMode: "public"}
		manifest := render(t, request)
		if len(manifest.HostCatalog) != 2 {
			t.Fatalf("%s：应只有两台主机，实际 %v", target.role, manifest.HostCatalog)
		}
		if manifest.PlacementHost("admin") != target.host || manifest.PlacementHost("console-proxy") != target.host {
			t.Fatalf("%s：管理台组件应放到 %s，实际 %+v", target.role, target.host, manifest.Placements)
		}
		if manifest.HostCatalog[target.host].TLSMode != "public" {
			t.Fatalf("%s：证书模式应取管理台的选择，实际 %q", target.role, manifest.HostCatalog[target.host].TLSMode)
		}
		if manifest.EventBus.PublicAddress != manifest.HostCatalog[target.host].Address {
			t.Fatalf("%s：EventBus 地址应是共用主机的地址", target.role)
		}
	}
}

func TestSameAddressWithoutSharingSuggestsTheShareOption(t *testing.T) {
	request := sampleRequest()
	request.Servers.Control.Address = request.Servers.Storage.Address
	_, err := Render(&request)
	if err == nil || !strings.Contains(err.Error(), "与数据存储服务器共用") {
		t.Fatalf("地址相同时应提示选择共用，实际 %v", err)
	}
}

func TestEmptyHostIDUsesTheRoleDefault(t *testing.T) {
	request := sampleRequest()
	request.Servers.Storage.ID = ""
	request.Servers.Overseas.ID = " "
	manifest := render(t, request)
	if _, ok := manifest.HostCatalog["storage"]; !ok {
		t.Fatalf("存储服务器应默认用 storage，实际 %v", manifest.HostCatalog)
	}
	if _, ok := manifest.HostCatalog["compute-1"]; !ok {
		t.Fatalf("海外服务器应默认用 compute-1，实际 %v", manifest.HostCatalog)
	}
}

func TestRenderWritesEveryHostFieldAndOmitsTheUnsetOnes(t *testing.T) {
	request := sampleRequest()
	request.Servers.Control.PrivateAddress = "10.0.0.5"
	request.Servers.Control.SSHPort = 2200
	manifest := render(t, request)
	control := manifest.HostCatalog["control"]
	if control.PrivateAddress != "10.0.0.5" || control.Region != "ap-guangzhou" || control.Provider != "tencent" || control.TLSMode != "auto" || control.SSH.Port != 2200 || control.SSH.Username != "ubuntu" {
		t.Fatalf("control = %+v", control)
	}
	compute := manifest.HostCatalog["compute-1"]
	if compute.PrivateAddress != "" || compute.TLSMode != "" {
		t.Fatalf("没填的可选项不应写出: %+v", compute)
	}
}

// 只替换账号、云凭据、通知和拓扑；拓扑之后的段落（egress_proxy 起）必须与示例逐字一致。
func TestRenderLeavesEverythingAfterTheTopologyUntouched(t *testing.T) {
	request := sampleRequest()
	raw, err := Render(&request)
	if err != nil {
		t.Fatal(err)
	}
	marker := "\n[egress_proxy]\n"
	got, want := string(raw), exampleTemplate
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
	secrets := []string{`a"b`, `back\slash`, `tab	inside`, "中文密码🔒", `#not-a-comment`, `key = "value"`, ` leading and trailing `, `\n literal`, `'single'`, `]`, `$(rm -rf)`}
	for _, secret := range secrets {
		request := sampleRequest()
		request.Admin.Password = secret
		if len([]byte(secret)) > 72 {
			continue
		}
		request.Servers.Control.SSHPassword = secret
		request.Tencent.SecretKey = secret
		raw, err := Render(&request)
		if err != nil {
			if strings.Contains(secret, "\t") {
				continue // 控制字符（制表符）按规则被拒绝
			}
			t.Fatalf("%q: %v", secret, err)
		}
		var decoded struct {
			Admin   struct{ Password string }
			Tencent struct {
				SecretKey string `toml:"secret_key"`
			} `toml:"tencent_cloud"`
			Hosts map[string]struct {
				SSH struct{ Password string }
			}
		}
		if _, err := toml.Decode(string(raw), &decoded); err != nil {
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
		"SecretKey 含控制字符": func(r *Request) { r.Tencent.SecretKey = "a\x00b" },
		"主机 ID 含大写":       func(r *Request) { r.Servers.Control.ID = "Control" },
		"主机 ID 含特殊字符":     func(r *Request) { r.Servers.Control.ID = `x"]` + "\n[evil" },
		"管理员密码为空":         func(r *Request) { r.Admin.Password = "" },
		"缺少存储服务器":         func(r *Request) { r.Servers.Storage = nil },
		"缺少海外服务器":         func(r *Request) { r.Servers.Overseas = nil },
		"缺少管理台服务器":        func(r *Request) { r.Servers.Control = nil },
		"存储服务器不能共用":       func(r *Request) { r.Servers.Storage.SharedWith = RoleOverseas },
		"管理台共用未知角色":       func(r *Request) { r.Servers.Control.SharedWith = "nowhere" },
		"管理台不能与因子机共用":     func(r *Request) { r.Servers.Control.SharedWith = RoleFactor },
		"因子机不能共用":         func(r *Request) { r.Servers.Factor = &Server{SharedWith: RoleStorage} },
		"因子机缺密码":          func(r *Request) { r.Servers.Factor = &Server{Address: "192.168.0.9", SSHUser: "u"} },
		"主机 ID 重复":        func(r *Request) { r.Servers.Overseas.ID = "control" },
		"因子机 ID 与部署主机重复": func(r *Request) {
			r.Servers.Factor = &Server{ID: "storage", Address: "192.168.0.9", SSHUser: "u", SSHPassword: "p"}
		},
		"地址重复":           func(r *Request) { r.Servers.Overseas.Address = r.Servers.Control.Address },
		"SSH 端口越界":       func(r *Request) { r.Servers.Storage.SSHPort = 70000 },
		"webhook 不是官方域名": func(r *Request) { r.Notification.WebhookURL = "https://evil.example/x" },
		"管理员密码超过 72 字节":  func(r *Request) { r.Admin.Password = strings.Repeat("x", 73) },
		"字段超长":           func(r *Request) { r.Servers.Control.Address = strings.Repeat("a", 600) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			request := sampleRequest()
			mutate(&request)
			raw, err := Render(&request)
			if err == nil {
				t.Fatalf("应被拒绝，却渲染出了 %d 字节", len(raw))
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
