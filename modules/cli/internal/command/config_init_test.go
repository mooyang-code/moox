package command

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/configinit"
)

func TestConfigInitCommandReportsOnlyStatusAndUsesDefaults(t *testing.T) {
	original := runConfigInit
	t.Cleanup(func() { runConfigInit = original })
	var got configinit.Options
	runConfigInit = func(_ context.Context, options configinit.Options) (configinit.Result, error) {
		got = options
		return configinit.Result{Path: "/work/moox.toml", Hosts: []string{"control", "storage"}, CompileHost: "compile"}, nil
	}
	cmd := newConfigInitCommand()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got.Output != "./moox.toml" || got.Timeout != configinit.DefaultTimeout || got.Notice == nil {
		t.Fatalf("默认选项 = %+v", got)
	}
	for _, want := range []string{`"status":"written"`, `"path":"/work/moox.toml"`, `"hosts":["control","storage"]`, `"compile_host":"compile"`, "仓库根目录且名为 moox.toml", "moox-cli setup validate"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("输出缺少 %q: %s", want, stdout.String())
		}
	}
}

func TestConfigInitCommandPassesFlagsAndErrors(t *testing.T) {
	original := runConfigInit
	t.Cleanup(func() { runConfigInit = original })
	var got configinit.Options
	runConfigInit = func(_ context.Context, options configinit.Options) (configinit.Result, error) {
		got = options
		return configinit.Result{}, errors.New("录入页已超过 2m0s 没有提交")
	}
	cmd := newConfigInitCommand()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--output", "./moox.toml.new", "--timeout", "2m"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "没有提交") {
		t.Fatalf("err = %v", err)
	}
	if got.Output != "./moox.toml.new" || got.Timeout != 2*time.Minute {
		t.Fatalf("选项 = %+v", got)
	}
	cmd = newConfigInitCommand()
	cmd.SetArgs([]string{"unexpected"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("不接受位置参数")
	}
}

func TestConfigCommandRegistersInitEditAndEncrypt(t *testing.T) {
	cmd := newConfigCommand(configDeps{})
	names := map[string]bool{}
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}
	for _, want := range []string{"init", "edit", "encrypt", "backup", "restore"} {
		if !names[want] {
			t.Fatalf("config 命令下应有 %s", want)
		}
	}
}
