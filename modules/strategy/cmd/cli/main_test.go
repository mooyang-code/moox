package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeDSL(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "strategy.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidateDSL(t *testing.T) {
	path := writeDSL(t, `name: momentum
rules:
  - id: main
    type: rank
    score: "rank(bias_q_20)"
    select: {top: 1}
    weight: {total: 1}
`)
	var out, errOut bytes.Buffer
	if err := runCLI([]string{"validate", path}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"name=momentum", "dsl_hash=sha256:", "main(rank)", "bias_q_20"} {
		if !strings.Contains(text, want) {
			t.Fatalf("输出缺少 %q：%s", want, text)
		}
	}
}

func TestValidateRejectsInvalidDSL(t *testing.T) {
	path := writeDSL(t, "name: broken\nrules: []\nunknown: 1\n")
	var out, errOut bytes.Buffer
	if err := runCLI([]string{"validate", path}, &out, &errOut); err == nil {
		t.Fatal("未知字段应被拒绝")
	}
	if err := runCLI([]string{"check"}, &out, &errOut); err == nil || !strings.Contains(err.Error(), "用法") {
		t.Fatalf("未知子命令应提示用法：%v", err)
	}
}

func TestTemplateCheckAndRender(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := runCLI([]string{"template", "check", "../../templates/tranche_example.yaml"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "template_id=tranche_example") || !strings.Contains(out.String(), "h、n、m、p、w") {
		t.Fatalf("check 输出：%s", out.String())
	}
	params := filepath.Join(t.TempDir(), "params.csv")
	if err := os.WriteFile(params, []byte("h,n,m,p,w\n1,20,24,0.1,0.5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runCLI([]string{"template", "render", "../../templates/tranche_example.yaml", "--name", "demo", "--var", "leverage=3", "--params", params}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`name: "demo"`, "type: tranche", "leverage: 3", "[1, 20, 24, 0.1, 0.5]"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("render 输出缺少 %q：%s", want, out.String())
		}
	}
	if err := runCLI([]string{"template", "render", "../../templates/tranche_example.yaml", "--name", "x"}, &out, &errOut); err == nil || !strings.Contains(err.Error(), "参数表不能为空") {
		t.Fatalf("没有参数表应报错：%v", err)
	}
}
