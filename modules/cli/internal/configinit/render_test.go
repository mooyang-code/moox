package configinit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
)

// sampleRequest 是与示例拓扑同构的三主机请求（地址用文档保留的测试网段）。
func sampleRequest() Request {
	var request Request
	request.Admin.Username = "admin"
	request.Admin.Password = "Adm1n-P@ss"
	request.Tencent.SecretID = "AKIDexample"
	request.Tencent.SecretKey = "key-example"
	request.Tencent.Region = "ap-guangzhou"
	request.Notification.ChannelType = "wecom"
	request.Notification.WebhookURL = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abc"
	request.Hosts = []Host{
		{ID: "control", Address: "198.51.100.10", Region: "ap-guangzhou", Provider: "tencent", TLSMode: "auto", SSHUser: "ubuntu", SSHPassword: "pw-control",
			Components: []string{"console-proxy", "web-host", "admin", "eventbus", "monitor", "collector", "cloudnode", "factor-mgr", "strategy"}},
		{ID: "storage", Address: "198.51.100.11", Region: "ap-nanjing", Provider: "tencent", TLSMode: "auto", SSHUser: "ubuntu", SSHPassword: "pw-storage",
			Components: []string{"storage-primary", "storage-node", "storage-view", "access"}},
		{ID: "compute-1", Address: "198.51.100.12", Region: "ap-hongkong", Provider: "tencent", SSHUser: "ubuntu", SSHPassword: "pw-compute",
			Components: []string{"trade", "access", "egress-proxy"}},
	}
	return request
}

func TestRenderProducesAManifestTheCLIAccepts(t *testing.T) {
	raw, err := Render(sampleRequest())
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := setupconfig.Parse(raw)
	if err != nil {
		t.Fatalf("渲染结果应通过 CLI 自己的校验: %v", err)
	}
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
	if manifest.PlacementHost("storage-view") != "storage" || manifest.PlacementHost("admin") != "control" {
		t.Fatalf("放置 = %+v", manifest.Placements)
	}
	if manifest.EventBus.PublicAddress != "198.51.100.10" {
		t.Fatalf("EventBus 地址应由 eventbus 所在主机推出，实际 %q", manifest.EventBus.PublicAddress)
	}
}

func TestRenderKeepsTheExampleDefaultsAndComments(t *testing.T) {
	raw, err := Render(sampleRequest())
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

func TestRenderWritesTheCompileHostOnlyWhenRequested(t *testing.T) {
	request := sampleRequest()
	request.Compile = &Host{ID: "builder", Address: "198.51.100.30", SSHPort: 2222, SSHUser: "builder", SSHPassword: "pw-build"}
	raw, err := Render(request)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := setupconfig.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.HasCompileHost() || manifest.CompileHost().Name != "builder" || manifest.CompileHost().Port != 2222 || manifest.CompileHost().Password != "pw-build" {
		t.Fatalf("编译机 = %+v", manifest.CompileHost())
	}
	if len(manifest.Placements["builder"]) != 0 {
		t.Fatal("编译机不应放置组件")
	}
}

func TestRenderWritesEveryHostFieldAndOmitsTheUnsetOnes(t *testing.T) {
	request := sampleRequest()
	request.Hosts[0].PrivateAddress = "10.0.0.5"
	request.Hosts[0].SSHPort = 2200
	raw, err := Render(request)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := setupconfig.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
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
	raw, err := Render(sampleRequest())
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
		request.Hosts[0].SSHPassword = secret
		request.Tencent.SecretKey = secret
		raw, err := Render(request)
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
		"SSH 密码含换行":       func(r *Request) { r.Hosts[0].SSHPassword = "x\r\n[hosts.evil]" },
		"SecretKey 含控制字符": func(r *Request) { r.Tencent.SecretKey = "a\x00b" },
		"主机 ID 含大写":       func(r *Request) { r.Hosts[0].ID = "Control" },
		"主机 ID 含特殊字符":     func(r *Request) { r.Hosts[0].ID = `x"]` + "\n[evil" },
		"管理员密码为空":         func(r *Request) { r.Admin.Password = "" },
		"没有主机":            func(r *Request) { r.Hosts = nil },
		"主机没有组件":          func(r *Request) { r.Hosts[1].Components = nil },
		"组件不存在":           func(r *Request) { r.Hosts[1].Components = []string{"no-such"} },
		"自动放置的组件":         func(r *Request) { r.Hosts[1].Components = []string{"host-gateway"} },
		"主机 ID 重复":        func(r *Request) { r.Hosts[1].ID = "control" },
		"地址重复":            func(r *Request) { r.Hosts[1].Address = r.Hosts[0].Address },
		"webhook 不是官方域名":  func(r *Request) { r.Notification.WebhookURL = "https://evil.example/x" },
		"管理员密码超过 72 字节":   func(r *Request) { r.Admin.Password = strings.Repeat("x", 73) },
		"字段超长":            func(r *Request) { r.Hosts[0].Address = strings.Repeat("a", 600) },
		"缺少 eventbus":     func(r *Request) { r.Hosts[0].Components = []string{"console-proxy", "web-host", "admin"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			request := sampleRequest()
			mutate(&request)
			raw, err := Render(request)
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
