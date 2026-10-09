package command

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/spf13/cobra"
)

func newSetupBuildLinuxCommand(deps setupDeps) *cobra.Command {
	var file, module, sourceDir string
	var prepareOnly bool
	cmd := &cobra.Command{
		Use:   "build-linux",
		Short: "在 compile_host 上构建需要 CGO 的 Linux 二进制",
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			if err := snapshot.VerifyUnchanged(); err != nil {
				return fmt.Errorf("config_changed")
			}
			if prepareOnly {
				return prepareCompileHost(cmd, snapshot, sourceDir)
			}
			if err := runSetupBuildLinux(cmd.Context(), snapshot, file, module, sourceDir); err != nil {
				return err
			}
			return writeSetupJSON(cmd, map[string]any{"status": "ok", "module": module})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&module, "module", "storage", "storage 或 storage-primary")
	cmd.Flags().StringVar(&sourceDir, "source-dir", "", "构建源码目录（默认当前仓库，可指定独立 worktree）")
	cmd.Flags().BoolVar(&prepareOnly, "prepare-only", false, "仅预置固定 Go 工具链并检查 C/C++ 编译器，不构建或部署服务")
	return cmd
}

func prepareCompileHost(cmd *cobra.Command, snapshot *setupconfig.Snapshot, sourceDir string) error {
	if !snapshot.Manifest.HasCompileHost() {
		return fmt.Errorf("compile_host is required")
	}
	configRoot, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err := linuxBuildSource(configRoot, sourceDir)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(root, ".go-version"))
	if err != nil {
		return err
	}
	version := strings.TrimSpace(string(raw))
	if !regexp.MustCompile(`^1\.[0-9]+\.[0-9]+$`).MatchString(version) {
		return fmt.Errorf("invalid pinned Go version")
	}
	script, err := os.ReadFile(filepath.Join(root, "scripts/ci/prepare-go-toolchain.sh"))
	if err != nil {
		return err
	}
	transport, err := dialSetupHost(cmd.Context(), snapshot.Manifest.CompileHost)
	if err != nil {
		return err
	}
	defer transport.Close()
	if _, err := transport.Run(cmd.Context(), []string{"bash", "-s", "--", version}, strings.NewReader(string(script))); err != nil {
		return fmt.Errorf("compile_toolchain_prepare_failed: %w", err)
	}
	return writeSetupJSON(cmd, map[string]any{"status": "ok", "host": snapshot.Manifest.CompileHost.Name, "go_version": version, "prepared_only": true})
}

func runSetupBuildLinux(ctx context.Context, snapshot *setupconfig.Snapshot, file, module, sourceDir string) error {
	module = strings.TrimSpace(module)
	switch module {
	case "storage", "storage-primary":
	default:
		return fmt.Errorf("unsupported linux CGO module %q", module)
	}
	if snapshot == nil || !snapshot.Manifest.HasCompileHost() {
		return fmt.Errorf("compile_host is required")
	}
	configRoot, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err := linuxBuildSource(configRoot, sourceDir)
	if err != nil {
		return err
	}
	configPath, err := filepath.Abs(file)
	if err != nil {
		return err
	}
	cli, err := os.Executable()
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, filepath.Join(root, "scripts", "build", "build-storage-linux.sh"))
	// The manifest stays in its original repository. Only source is synced
	// from the selected worktree; never copy credentials into that worktree.
	command.Dir = configRoot
	command.Stdout = os.Stderr
	command.Stderr = os.Stderr
	command.Env = append(os.Environ(),
		"MOOX_SSH_PASSWORD="+snapshot.Manifest.CompileHost.Password,
		"MOOX_CLI="+cli,
		"MOOX_CONFIG_ROOT="+configRoot,
		"CONFIG="+configPath,
		"MOOX_STORAGE_BUILD_HOST="+snapshot.Manifest.CompileHost.Name,
		"MOOX_STORAGE_BUILD_HOST_ROLE=compile",
		"MOOX_LINUX_CGO_TARGET="+module,
	)
	return command.Run()
}

func linuxBuildSource(configRoot, sourceDir string) (string, error) {
	if sourceDir == "" {
		sourceDir = configRoot
	}
	root, err := filepath.Abs(sourceDir)
	if err != nil {
		return "", fmt.Errorf("invalid build source directory")
	}
	for _, path := range []string{"go.work", ".go-version", "scripts/build/build-storage-linux.sh"} {
		info, err := os.Stat(filepath.Join(root, path))
		if err != nil || !info.Mode().IsRegular() {
			return "", fmt.Errorf("build source directory is missing %s", path)
		}
	}
	return root, nil
}
