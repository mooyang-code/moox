package command

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/configinit"
	"github.com/spf13/cobra"
)

func TestConfigEditCommandReportsOnlyStatus(t *testing.T) {
	original := runConfigEditor
	t.Cleanup(func() { runConfigEditor = original })
	var got configinit.EditorOptions
	runConfigEditor = func(_ context.Context, options configinit.EditorOptions) (configinit.EditorResult, error) {
		got = options
		return configinit.EditorResult{Path: "/work/moox.toml", Saved: true, Encrypted: true}, nil
	}
	cmd := newConfigEditCommand()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--timeout", "5m"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got.File != "./moox.toml" || got.Timeout != 5*time.Minute || got.Notice == nil {
		t.Fatalf("选项 = %+v", got)
	}
	for _, want := range []string{`"status":"closed"`, `"saved":true`, `"encrypted":true`} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("输出缺少 %s: %s", want, stdout.String())
		}
	}
	runConfigEditor = func(context.Context, configinit.EditorOptions) (configinit.EditorResult, error) {
		return configinit.EditorResult{}, errors.New("读不到 moox.toml")
	}
	cmd = newConfigEditCommand()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err == nil {
		t.Fatal("错误应传给调用方")
	}
}

func TestConfigEncryptCommandReportsStatus(t *testing.T) {
	original := encryptConfigFile
	t.Cleanup(func() { encryptConfigFile = original })
	for already, want := range map[bool]string{false: `"status":"encrypted"`, true: `"status":"already_encrypted"`} {
		encryptConfigFile = func(path string) (bool, error) {
			if path != "./moox.toml" {
				t.Fatalf("路径 = %q", path)
			}
			return already, nil
		}
		cmd := newConfigEncryptCommand()
		var stdout bytes.Buffer
		cmd.SetOut(&stdout)
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(nil)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("输出 = %s，应含 %s", stdout.String(), want)
		}
	}
}

func TestConfigBackupAndRestoreCommandsReportOnlyStatus(t *testing.T) {
	originalBackup, originalRestore := backupConfigFile, runConfigRestore
	t.Cleanup(func() { backupConfigFile, runConfigRestore = originalBackup, originalRestore })
	backupConfigFile = func(_ context.Context, path string) (configinit.BackupResult, error) {
		if path != "./moox.toml" {
			t.Fatalf("路径 = %q", path)
		}
		return configinit.BackupResult{Console: "198.51.100.10", UpdatedAt: time.Unix(1700000100, 0)}, nil
	}
	var restoreOptions configinit.RestoreOptions
	runConfigRestore = func(_ context.Context, options configinit.RestoreOptions) (configinit.RestoreResult, error) {
		restoreOptions = options
		return configinit.RestoreResult{Path: "/work/moox.toml", Console: "198.51.100.10"}, nil
	}
	for _, c := range []struct {
		cmd  *cobra.Command
		args []string
		want []string
	}{
		{newConfigBackupCommand(), nil, []string{`"status":"backed_up"`, `"updated_at":"2023-11-14T22:15:00Z"`}},
		{newConfigRestoreCommand(), []string{"--timeout", "3m"}, []string{`"status":"restored"`, `"path":"/work/moox.toml"`}},
	} {
		var stdout bytes.Buffer
		c.cmd.SetOut(&stdout)
		c.cmd.SetErr(&bytes.Buffer{})
		c.cmd.SetArgs(c.args)
		if err := c.cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		for _, want := range c.want {
			if !strings.Contains(stdout.String(), want) {
				t.Fatalf("输出 %s 缺少 %s", stdout.String(), want)
			}
		}
	}
	if restoreOptions.Output != "./moox.toml" || restoreOptions.Timeout != 3*time.Minute || restoreOptions.Notice == nil {
		t.Fatalf("恢复选项 = %+v", restoreOptions)
	}
}
