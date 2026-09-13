package command

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/spf13/cobra"
)

func newSetupBuildLinuxCommand(deps setupDeps) *cobra.Command {
	var file, module string
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
			if err := runSetupBuildLinux(cmd.Context(), snapshot, file, module); err != nil {
				return err
			}
			return writeSetupJSON(cmd, map[string]any{"status": "ok", "module": module})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&module, "module", "storage", "storage、factor 或 factor-engine")
	return cmd
}

func runSetupBuildLinux(ctx context.Context, snapshot *setupconfig.Snapshot, file, module string) error {
	module = strings.TrimSpace(module)
	switch module {
	case "storage", "factor", "factor-engine":
	default:
		return fmt.Errorf("unsupported linux CGO module %q", module)
	}
	if snapshot == nil || !snapshot.Manifest.HasCompileHost() {
		return fmt.Errorf("compile_host is required")
	}
	root, err := os.Getwd()
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
	command.Dir = root
	command.Stdout = os.Stderr
	command.Stderr = os.Stderr
	command.Env = append(os.Environ(),
		"MOOX_SSH_PASSWORD="+snapshot.Manifest.CompileHost.Password,
		"MOOX_CLI="+cli,
		"CONFIG="+configPath,
		"MOOX_STORAGE_BUILD_HOST="+snapshot.Manifest.CompileHost.Name,
		"MOOX_STORAGE_BUILD_HOST_ROLE=compile",
		"MOOX_LINUX_CGO_TARGET="+module,
	)
	return command.Run()
}
