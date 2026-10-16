package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, root, module, body string) {
	t.Helper()
	dir := filepath.Join(root, module, "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "trpc_go.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCheckAppliesTRPCDefaultsAndInheritance(t *testing.T) {
	root := t.TempDir()
	// 省略 network/protocol 时框架默认就是 tcp + trpc，仍会落到 tnet。
	writeConfig(t, root, "omitted", `server:
  service:
    - name: trpc.test.Omitted
      ip: 127.0.0.1
      port: 1
`)
	// 带引号的值与 server 级继承的 go-net 都应视为合规。
	writeConfig(t, root, "inherited", `server:
  transport: "go-net"
  service:
    - name: trpc.test.Inherited
      ip: 127.0.0.1
      port: 2
      protocol: "trpc"
`)
	// 非 trpc 协议和定时器不受 tnet 自动切换影响。
	writeConfig(t, root, "others", `server:
  service:
    - name: trpc.test.Health
      ip: 127.0.0.1
      port: 3
      protocol: http_no_protocol
    - name: trpc.test.timer
      port: 4
      network: "*/30 * * * * *"
      protocol: timer
`)
	// 显式写 tnet 也不允许。
	writeConfig(t, root, "explicit", `server:
  service:
    - name: trpc.test.Explicit
      ip: 127.0.0.1
      port: 5
      network: tcp4
      protocol: trpc
      transport: tnet
`)

	findings, err := check(root)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(findings, "\n")
	for _, want := range []string{"trpc.test.Omitted", "trpc.test.Explicit"} {
		if !strings.Contains(got, want) {
			t.Fatalf("应报告 %s，实际：\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"trpc.test.Inherited", "trpc.test.Health", "trpc.test.timer"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("不应报告 %s，实际：\n%s", unwanted, got)
		}
	}
	if len(findings) != 2 {
		t.Fatalf("应只有 2 条问题，实际：\n%s", got)
	}
}

func TestCheckRejectsEmptyRoot(t *testing.T) {
	if _, err := check(t.TempDir()); err == nil {
		t.Fatal("没有任何配置时应报错，避免路径写错导致检查形同虚设")
	}
}
