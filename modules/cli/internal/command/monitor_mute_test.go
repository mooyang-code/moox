package command

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// 屏蔽时长在命令行先拦一次：缺省、非正数、超过 24 小时都不会去连网关。
func TestMonitorMuteRejectsInvalidDurationBeforeConnecting(t *testing.T) {
	for _, args := range [][]string{
		{"monitor", "mute"},
		{"monitor", "mute", "--for", "0s"},
		{"monitor", "mute", "--for", "-1h"},
		{"monitor", "mute", "--for", (maxMuteDuration + time.Second).String()},
	} {
		var out bytes.Buffer
		rootCmd.SetOut(&out)
		rootCmd.SetErr(&out)
		rootCmd.SetArgs(append(args, "--file", "/nonexistent/moox.toml"))
		err := rootCmd.Execute()
		if err == nil || !strings.Contains(err.Error(), "--for") {
			t.Fatalf("%v 应在连接网关之前就因时长被拒绝：%v", args, err)
		}
	}
	rootCmd.SetArgs(nil)
}

func TestMonitorCommandsAreRegistered(t *testing.T) {
	registered := map[string]bool{}
	for _, c := range monitorCmd.Commands() {
		registered[c.Name()] = true
	}
	for _, name := range []string{"mute", "unmute", "mute-status"} {
		if !registered[name] {
			t.Fatalf("monitor %s 没有注册", name)
		}
	}
}
