package command

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func newSetupBuildLinuxCommand(_ setupDeps) *cobra.Command {
	var module, sourceDir string
	cmd := &cobra.Command{
		Use:   "build-linux",
		Short: "在本机 Docker 里构建需要 CGO 的 Linux 二进制",
		Long: "Storage 的 Linux 制品依赖 DuckDB（CGO），在本机的 Docker 编译镜像里构建；" +
			"需要先启动 Docker（macOS 可用 colima start）。产物写到源码目录的 bin/。纯 Go 的服务和前端不需要这个命令。",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := runSetupBuildLinux(cmd.Context(), module, sourceDir); err != nil {
				return err
			}
			return writeSetupJSON(cmd, map[string]any{"status": "ok", "module": module})
		},
	}
	cmd.Flags().StringVar(&module, "module", "storage", "storage 或 storage-primary")
	cmd.Flags().StringVar(&sourceDir, "source-dir", "", "构建源码目录（默认当前仓库，可指定独立 worktree）")
	return cmd
}

func runSetupBuildLinux(ctx context.Context, module, sourceDir string) error {
	module = strings.TrimSpace(module)
	switch module {
	case "storage", "storage-primary":
	default:
		return fmt.Errorf("unsupported linux CGO module %q", module)
	}
	configRoot, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err := linuxBuildSource(configRoot, sourceDir)
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, filepath.Join(root, "scripts", "build", "build-linux-local.sh"), "--out", filepath.Join(root, "bin"), module)
	command.Dir = root
	command.Stdout = os.Stderr
	command.Stderr = os.Stderr
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
	for _, path := range []string{"go.work", ".go-version", "scripts/build/build-linux-local.sh"} {
		info, err := os.Stat(filepath.Join(root, path))
		if err != nil || !info.Mode().IsRegular() {
			return "", fmt.Errorf("build source directory is missing %s", path)
		}
	}
	return root, nil
}
