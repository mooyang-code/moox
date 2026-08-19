package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var storageCmd = &cobra.Command{
	Use:     "storage",
	Aliases: []string{"存储"},
	Short:   "存储数据导入与读写工具",
	Long:    "存储数据导入与读写工具。历史数据导入请使用 moox-cli storage import --format csv。",
	Run: func(cmd *cobra.Command, args []string) {
		_ = cmd.Help()
	},
}

func init() {
	rootCmd.AddCommand(storageCmd)
	storageCmd.AddCommand(storageRepairViewCmd)
	storageCmd.AddCommand(storageForceRebuildViewCmd)
	storageCmd.AddCommand(storageResetViewConsumersCmd)
}

// storageRepairViewCmd delegates the maintenance implementation to the
// storage module binary. Keeping the SQLite/NATS maintenance code next to the
// storage schema avoids duplicating internal storage knowledge in the root
// CLI, while still exposing one self-service moox-cli command.
var storageRepairViewCmd = &cobra.Command{
	Use:                "repair-view [flags]",
	Aliases:            []string{"修复视图", "repair"},
	Short:              "清理 View 消费积压并触发安全的 A/B 重建",
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runStorageMaintenanceBinary(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args)
	},
}

var storageForceRebuildViewCmd = &cobra.Command{
	Use:                "force-rebuild-view [flags]",
	Aliases:            []string{"强制重建视图", "重建视图"},
	Short:              "删除旧 View、清空 durable consumer 状态并从头重建",
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runStorageMaintenanceBinaryAction(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), "force-rebuild-view", args)
	},
}

var storageResetViewConsumersCmd = &cobra.Command{
	Use:                "reset-view-consumers [flags]",
	Aliases:            []string{"重置视图消费", "清理视图队列"},
	Short:              "删除所有 View durable/历史消息/索引并按回溯窗口重新构建",
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runStorageMaintenanceBinaryAction(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), "reset-view-consumers", args)
	},
}

func runStorageMaintenanceBinary(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) error {
	return runStorageMaintenanceBinaryAction(ctx, stdin, stdout, stderr, "repair-view", args)
}

func runStorageMaintenanceBinaryAction(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, action string, args []string) error {
	path, err := resolveStorageMaintenanceBinary()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		args = []string{"--help"}
	}
	cmd := exec.CommandContext(ctx, path, append([]string{action}, args...)...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("moox-storage-cli %s failed with exit code %d", action, exitErr.ExitCode())
		}
		return fmt.Errorf("run moox-storage-cli %s: %w", action, err)
	}
	return nil
}

func resolveStorageMaintenanceBinary() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("MOOX_STORAGE_CLI")); configured != "" {
		if isExecutableFile(configured) {
			return configured, nil
		}
		return "", fmt.Errorf("MOOX_STORAGE_CLI %q is not executable", configured)
	}
	if executable, err := os.Executable(); err == nil {
		dir := filepath.Dir(executable)
		for _, candidate := range []string{
			filepath.Join(dir, "moox-storage-cli"),
			filepath.Join(dir, "moox-storage-primary-cli"),
			filepath.Join(dir, "..", "storage-primary", "bin", "moox-storage-primary-cli"),
		} {
			if isExecutableFile(candidate) {
				return candidate, nil
			}
		}
	}
	if path, err := exec.LookPath("moox-storage-cli"); err == nil {
		return path, nil
	}
	if path, err := exec.LookPath("moox-storage-primary-cli"); err == nil {
		return path, nil
	}
	return "", errors.New("moox-storage-cli was not found; set MOOX_STORAGE_CLI or install the storage CLI beside moox-cli")
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}
