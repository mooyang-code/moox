package configinit

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 录入页的「交易所密钥」一步写在 page.html 的脚本里：没配海外服务器时隐藏、前进后退跳过、隐藏时不发送密钥。
// 这些行为用 jsdom 加载真实的页面来检查（testdata/page_exchange.cjs）；本机没有 node 或 web 依赖没装时跳过。
func TestPageExchangeKeyStep(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("没有 node，跳过录入页脚本检查")
	}
	jsdom, err := filepath.Abs("../../../../web/node_modules/jsdom")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(jsdom); err != nil {
		t.Skip("web/node_modules 里没有 jsdom（先运行 pnpm install），跳过录入页脚本检查")
	}
	roles, err := loadRoles()
	if err != nil {
		t.Fatal(err)
	}
	bootJSON, err := json.Marshal(map[string]any{"roles": roles})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	boot := filepath.Join(dir, "boot.json")
	if err := os.WriteFile(boot, bootJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	page, err := filepath.Abs("page.html")
	if err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("testdata/page_exchange.cjs")
	if err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(node, script, jsdom, page, boot).CombinedOutput()
	if err != nil {
		t.Fatalf("录入页脚本检查没有通过：%v\n%s", err, output)
	}
	if strings.Contains(string(output), "FAIL") {
		t.Fatalf("录入页脚本检查有失败项：\n%s", output)
	}
}

// 配置页的「交易所密钥」：概览只显示打码信息并按序号取明文，表单能增删改并把完整列表交给保存接口。
func TestEditorExchangeKeySection(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("没有 node，跳过配置页脚本检查")
	}
	jsdom, err := filepath.Abs("../../../../web/node_modules/jsdom")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(jsdom); err != nil {
		t.Skip("web/node_modules 里没有 jsdom（先运行 pnpm install），跳过配置页脚本检查")
	}
	page, err := filepath.Abs("editor.html")
	if err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("testdata/editor_exchange.cjs")
	if err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(node, script, jsdom, page).CombinedOutput()
	if err != nil || strings.Contains(string(output), "FAIL") {
		t.Fatalf("配置页脚本检查没有通过：%v\n%s", err, output)
	}
}
