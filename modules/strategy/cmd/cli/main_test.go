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
