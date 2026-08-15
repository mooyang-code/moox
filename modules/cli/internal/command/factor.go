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

var factorCmd = &cobra.Command{
	Use:     "factor",
	Aliases: []string{"因子"},
	Short:   "因子服务维护工具",
	Run: func(cmd *cobra.Command, args []string) {
		_ = cmd.Help()
	},
}

var factorClearQueueCmd = &cobra.Command{
	Use:                "clear-queue [flags]",
	Aliases:            []string{"清空积压", "reset-consumer"},
	Short:              "清空 Factor durable consumer 的历史积压并重启 Factor",
	DisableFlagParsing: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runFactorMaintenanceBinary(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr(), args)
	},
}

func init() {
	rootCmd.AddCommand(factorCmd)
	factorCmd.AddCommand(factorClearQueueCmd)
}

func runFactorMaintenanceBinary(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args []string) error {
	path, err := resolveFactorMaintenanceBinary()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		args = []string{"--help"}
	}
	cmd := exec.CommandContext(ctx, path, append([]string{"clear-queue"}, args...)...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("moox-factor-cli clear-queue failed with exit code %d", exitErr.ExitCode())
		}
		return fmt.Errorf("run moox-factor-cli clear-queue: %w", err)
	}
	return nil
}

func resolveFactorMaintenanceBinary() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("MOOX_FACTOR_CLI")); configured != "" {
		if isExecutableFile(configured) {
			return configured, nil
		}
		return "", fmt.Errorf("MOOX_FACTOR_CLI %q is not executable", configured)
	}
	if executable, err := os.Executable(); err == nil {
		dir := filepath.Dir(executable)
		for _, candidate := range []string{
			filepath.Join(dir, "moox-factor-cli"),
			filepath.Join(dir, "..", "factor", "bin", "moox-factor-cli"),
			filepath.Join(dir, "..", "..", "factor", "bin", "moox-factor-cli"),
		} {
			if isExecutableFile(candidate) {
				return candidate, nil
			}
		}
	}
	if path, err := exec.LookPath("moox-factor-cli"); err == nil {
		return path, nil
	}
	return "", errors.New("moox-factor-cli was not found; set MOOX_FACTOR_CLI or install the Factor CLI beside moox-cli")
}
