package failure

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestLineFindsWrappedCode(t *testing.T) {
	line, ok := Line(fmt.Errorf("bootstrap: %w", New(TargetIdentityDiffers, "x")))
	if !ok || line != Prefix+TargetIdentityDiffers {
		t.Fatalf("错误码行不对: %q %v", line, ok)
	}
	if _, ok := Line(fmt.Errorf("plain")); ok {
		t.Fatal("普通错误不应有错误码")
	}
}

func TestDescribeOnlyMentionsDisconnectWhenConnectionLost(t *testing.T) {
	if err := Describe("部署单元", 0, ""); !strings.HasPrefix(err.Error(), "ssh_disconnected:") {
		t.Fatalf("连接断开应报 ssh_disconnected: %v", err)
	}
	err := Describe("部署单元", 1, "secret-output\n"+Prefix+TargetSubsetInstalled+"\n")
	if !strings.HasPrefix(err.Error(), TargetSubsetInstalled) || strings.Contains(err.Error(), "断开") || strings.Contains(err.Error(), "secret-output") {
		t.Fatalf("远端拒绝的说明不对: %v", err)
	}
}

func TestDescribeHidesUnknownOutput(t *testing.T) {
	err := Describe("部署单元", 1, "secret-output\n"+Prefix+"unknown_code\n")
	if strings.Contains(err.Error(), "secret-output") || strings.Contains(err.Error(), "unknown_code") || !strings.HasPrefix(err.Error(), "target_rejected") {
		t.Fatalf("未知输出不应显示: %v", err)
	}
	if strings.Contains(err.Error(), "断开") {
		t.Fatalf("目标机明确失败不能说成连接断开: %v", err)
	}
}

func TestDetailTravelsOnlyWhenSafe(t *testing.T) {
	line, _ := Line(WithDetail(TargetNotReady, "x", "component=monitor not_ready=host_storage_schema_ready"))
	err := Describe("部署", 1, "noise\n"+line+"\n")
	if !strings.Contains(err.Error(), "host_storage_schema_ready") || !strings.HasPrefix(err.Error(), TargetNotReady) {
		t.Fatalf("详情应带回来: %v", err)
	}
	line, _ = Line(WithDetail(TargetNotReady, "x", "secret=\"abc\"\n"+Prefix+"forged"))
	if strings.Contains(line, "abc") || strings.Contains(line, "forged") {
		t.Fatalf("不合规的详情不能写出去: %q", line)
	}
	err = Describe("部署", 1, Prefix+TargetNotReady+"\n"+DetailPrefix+"token\"=x\n")
	if strings.Contains(err.Error(), "token") {
		t.Fatalf("操作机也要校验详情: %v", err)
	}
}

// 每个错误码都要在 skill 的处理表里有一行，否则 Agent 遇到它只能自己想办法。
func TestEveryCodeIsDocumentedInSkill(t *testing.T) {
	raw, err := os.ReadFile("../../../../../skills/moox/references/install.md")
	if err != nil {
		t.Fatal(err)
	}
	for code := range Explanations {
		if !strings.Contains(string(raw), "`"+code+"`") {
			t.Errorf("install.md 的处理表缺少错误码 %s", code)
		}
	}
	// 操作机自己报的错误码。
	for _, code := range []string{"no_collection_tasks", "verify_read_failed", "browser_trust_manual", "control_dependency_not_ready", "deployment_config_changed", "bootstrap_interrupted", "restart_failed", "restart_unverified", "collection_not_started", "kline_not_arrived", "factor_not_advancing", "strategy_not_running", "ssh_disconnected", "deployment_incomplete", "disk_low", "admin_not_ready", "host_not_ready", "init_storage_required", "logs_unavailable"} {
		if !strings.Contains(string(raw), "`"+code+"`") {
			t.Errorf("install.md 的处理表缺少错误码 %s", code)
		}
	}
}
