package report

import (
	"os"
	"testing"
)

// 上报的版本：环境变量 MOOX_VERSION 优先，其次是构建时注入的 BuildVersion，都没有才是 dev。
func TestDefaultConfigVersionPrecedence(t *testing.T) {
	original := BuildVersion
	t.Cleanup(func() { BuildVersion = original })
	t.Setenv("MOOX_VERSION", "")
	t.Setenv("MOOX_SERVICE_VERSION", "")

	BuildVersion = ""
	if got := DefaultConfig("trade", "trade").Version; got != "dev" {
		t.Fatalf("没有注入版本时上报 dev：%q", got)
	}
	BuildVersion = "v0.0.2"
	if got := DefaultConfig("trade", "trade").Version; got != "v0.0.2" {
		t.Fatalf("应上报构建时注入的版本：%q", got)
	}
	if got := (Config{Module: "trade", ServiceName: "trade"}).withDefaults().Version; got != "v0.0.2" {
		t.Fatalf("没写版本的配置补默认值时同样取注入的版本：%q", got)
	}
	t.Setenv("MOOX_VERSION", "v9.9.9")
	if got := DefaultConfig("trade", "trade").Version; got != "v9.9.9" {
		t.Fatalf("环境变量优先：%q", got)
	}
}

// 由 scripts/test/contract/test-build-version-injection.sh 带着构建脚本里的 -ldflags 运行：确认构建脚本写的变量路径
// 确实能把版本号注入进来（变量改名、包路径变化都会让它失败）。平时没有设置 MOOX_EXPECT_BUILD_VERSION，跳过。
func TestBuildVersionInjectedByLdflags(t *testing.T) {
	want := os.Getenv("MOOX_EXPECT_BUILD_VERSION")
	if want == "" {
		t.Skip("只在版本注入的契约检查里运行")
	}
	if BuildVersion != want {
		t.Fatalf("BuildVersion = %q，期望构建脚本注入的 %q", BuildVersion, want)
	}
	t.Setenv("MOOX_VERSION", "")
	t.Setenv("MOOX_SERVICE_VERSION", "")
	if got := DefaultConfig("trade", "trade").Version; got != want {
		t.Fatalf("上报的版本 = %q，期望 %q", got, want)
	}
}
