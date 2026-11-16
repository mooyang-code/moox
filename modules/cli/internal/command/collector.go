package command

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/collectorpackager"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/bundle"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/fetchunit"
	"github.com/spf13/cobra"
)

type collectorPackageOptions struct {
	CollectorRoot    string
	SpaceID          string
	PackageConfigDir string
	Version          string
	Out              string
	ConfigDir        string
	Entrypoint       string
	CLSLogsetID      string
	CLSTopicID       string
	EventBusCAPEM    []byte
	EventBusCAFile   string
}

var collectorPackageFlags collectorPackageOptions

var collectorCmd = &cobra.Command{
	Use:   "collector",
	Short: "采集：任务、运行状态、补采与合成、标的、采集方式、云函数、云账号与代码包",
	Run: func(cmd *cobra.Command, args []string) {
		_ = cmd.Help()
	},
}

var collectorFunctionCmd = &cobra.Command{
	Use:   "function",
	Short: "采集云函数：代码包构建与上传、云上状态、删除、出口探测",
	Run: func(cmd *cobra.Command, args []string) {
		_ = cmd.Help()
	},
}

var collectorFunctionPackageCmd = &cobra.Command{
	Use:   "package",
	Short: "构建数据采集器 SCF zip 包",
	RunE: func(cmd *cobra.Command, args []string) error {
		result, err := packageCollectorFunction(cmd.Context(), collectorPackageFlags)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "package=%s\n", result.Path)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(collectorCmd)
	collectorCmd.AddCommand(collectorFunctionCmd)
	collectorFunctionCmd.AddCommand(collectorFunctionPackageCmd)
	addCollectorPackageFlags(collectorFunctionPackageCmd, &collectorPackageFlags)
	collectorFunctionPackageCmd.Flags().StringVar(&collectorPackageFlags.SpaceID, "space-id", "", "space id; selects the matching SCF config directory")
}

func addCollectorPackageFlags(cmd *cobra.Command, opts *collectorPackageOptions) {
	cmd.Flags().StringVar(&opts.CollectorRoot, "collector-root", "", "collector module root")
	cmd.Flags().StringVar(&opts.Version, "version", "dev", "collector package version")
	cmd.Flags().StringVar(&opts.Out, "out", "", "output zip path")
	cmd.Flags().StringVar(&opts.ConfigDir, "config", "", "collector config directory")
	cmd.Flags().StringVar(&opts.CLSTopicID, "cls-topic-id", "", "central CLS topic id used by SCF warning/error logs")
	cmd.Flags().StringVar(&opts.EventBusCAFile, "eventbus-ca-file", "", "public EventBus CA PEM to package (no credentials)")
}

func packageCollectorFunction(ctx context.Context, opts collectorPackageOptions) (*collectorpackager.BuildSCFPackageResult, error) {
	if len(opts.EventBusCAPEM) == 0 && strings.TrimSpace(opts.EventBusCAFile) != "" {
		var err error
		opts.EventBusCAPEM, err = os.ReadFile(opts.EventBusCAFile)
		if err != nil {
			return nil, fmt.Errorf("read public EventBus CA file: %w", err)
		}
	}
	// EventBus CA 可选：采集计划函数不连 EventBus，只有旧链路的包才携带。
	if len(opts.EventBusCAPEM) > 0 {
		if err := collectorpackager.ValidateEventBusCAPEM(opts.EventBusCAPEM); err != nil {
			return nil, fmt.Errorf("public EventBus CA is invalid: %w", err)
		}
	}
	collectorRoot, err := resolveCollectorRoot(opts.CollectorRoot)
	if err != nil {
		return nil, err
	}
	version := opts.Version
	if version == "" {
		version = "dev"
	}
	outPath := opts.Out
	if outPath == "" {
		spaceID := strings.TrimSpace(opts.SpaceID)
		if spaceID == "" {
			return nil, fmt.Errorf("--space-id is required when --out is omitted")
		}
		outPath = filepath.Join(collectorRoot, fmt.Sprintf("collector-scf-%s-%s.zip", spaceID, version))
	}
	configDir := opts.ConfigDir
	if configDir == "" {
		spaceID := strings.TrimSpace(opts.SpaceID)
		if spaceID == "" {
			return nil, fmt.Errorf("--space-id is required when --config is omitted")
		}
		configDir = filepath.Join(collectorRoot, "configs", filepath.FromSlash(defaultFlag(opts.PackageConfigDir, defaultCollectorPackageConfigDir(spaceID))))
	}
	binaryPath := filepath.Join(os.TempDir(), fmt.Sprintf("moox-collector-scf-%d", time.Now().UnixNano()), "main")
	if err := os.MkdirAll(filepath.Dir(binaryPath), 0o755); err != nil {
		return nil, err
	}
	defer os.RemoveAll(filepath.Dir(binaryPath))

	entrypoint := defaultFlag(opts.Entrypoint, "market_data")
	if err := buildCollectorLinuxBinary(ctx, collectorRoot, binaryPath, version, entrypoint); err != nil {
		return nil, err
	}
	return collectorpackager.BuildSCFPackage(collectorpackager.BuildSCFPackageOptions{
		BinaryPath:    binaryPath,
		ConfigDir:     configDir,
		OutPath:       outPath,
		EventBusCAPEM: opts.EventBusCAPEM,
	})
}

func defaultCollectorPackageConfigDir(spaceID string) string {
	spaceID = strings.ToLower(strings.TrimSpace(spaceID))
	if spaceID == "crypto" {
		return filepath.ToSlash(filepath.Join("scf", "market_data"))
	}
	return filepath.ToSlash(filepath.Join("scf", spaceID))
}

func buildCollectorLinuxBinary(ctx context.Context, collectorRoot, outPath, version, entrypoint string) error {
	if entrypoint != "market_data" {
		return fmt.Errorf("unsupported collector SCF entrypoint %q", entrypoint)
	}
	// 产物包目录里按清单下载预编译的函数程序，不需要本机 Go。
	root := filepath.Join(collectorRoot, "..", "..")
	if manifest, ok, err := bundle.Load(root); err != nil {
		return err
	} else if ok {
		artifact := manifest.CollectorSCF
		cache, err := os.UserCacheDir()
		if err != nil {
			return err
		}
		local, err := fetchunit.Local(ctx, fetchunit.Sources(manifest.Release), fetchunit.File{Name: artifact.File, SHA256: artifact.SHA256, Size: artifact.Size}, filepath.Join(cache, "moox", "downloads"))
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(local)
		if err != nil {
			return err
		}
		return os.WriteFile(outPath, raw, 0o755)
	}
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags", fmt.Sprintf("-s -w -X main.Version=%s", version), "-o", outPath, "./cmd/scf/"+entrypoint)
	cmd.Dir = collectorRoot
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build collector linux/amd64 binary: %w\n%s", err, output)
	}
	return nil
}

func resolveCollectorRoot(explicit string) (string, error) {
	var candidates []string
	if explicit != "" {
		candidates = append(candidates, explicit)
	} else if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, cwd, filepath.Join(cwd, "modules", "collector"), filepath.Join(cwd, "..", "collector"))
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(candidate, "cmd", "scf", "market_data", "main.go")); err == nil {
			return filepath.Abs(candidate)
		}
	}
	if explicit != "" {
		return "", fmt.Errorf("collector root not found：%s 下没有 cmd/scf/market_data/main.go，核对 --source-dir 或 --collector-root", explicit)
	}
	return "", fmt.Errorf("collector root not found：当前目录不是仓库根或 Collector 模块目录，带 --collector-root（setup 命令带 --source-dir）")
}
