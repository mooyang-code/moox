package command

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/packages/security"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const skillConfigIdentity = "moox-skill"

type skillSecretReader func(context.Context, setupconfig.Host, string) ([]byte, error)

func newSetupExportSkillConfigCommand(deps setupDeps) *cobra.Command {
	var file, space, output string
	cmd := &cobra.Command{
		Use:   "export-skill-config",
		Short: "导出 Skill 使用的最小数据访问配置",
		RunE: func(cmd *cobra.Command, _ []string) error {
			space = strings.TrimSpace(space)
			output = strings.TrimSpace(output)
			if space == "" {
				return fmt.Errorf("--space is required")
			}
			if output == "" {
				return fmt.Errorf("--output is required")
			}
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			if err := rejectInputOutputCollision(file, output); err != nil {
				return fmt.Errorf("write skill config: %w", err)
			}

			config, err := deps.exportSkillConfig(cmd.Context(), snapshot, space)
			if err != nil {
				return err
			}
			if err := config.validate(); err != nil {
				return fmt.Errorf("skill_config_invalid: %w", err)
			}
			raw, err := yaml.Marshal(config)
			if err != nil {
				return fmt.Errorf("encode skill config: %w", err)
			}
			if err := snapshot.VerifyUnchanged(); err != nil {
				return fmt.Errorf("config_changed")
			}
			if err := writeSkillConfigAtomic0600(output, raw, os.Rename); err != nil {
				return fmt.Errorf("write skill config: %w", err)
			}
			return writeSetupJSON(cmd, map[string]string{"status": "exported", "output": output})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&space, "space", "", "要导出的 SCF Space ID")
	cmd.Flags().StringVar(&output, "output", "", "Skill 数据访问配置输出路径")
	_ = cmd.MarkFlagRequired("space")
	_ = cmd.MarkFlagRequired("output")
	return cmd
}

func defaultSetupExportSkillConfig(ctx context.Context, snapshot *setupconfig.Snapshot, space string) (dataAccessConfig, error) {
	if snapshot == nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: setup snapshot is required")
	}
	transports := make(map[string]setupssh.Client)
	defer func() {
		for _, transport := range transports {
			_ = transport.Close()
		}
	}()
	transportFor := func(ctx context.Context, host setupconfig.Host) (setupssh.Client, error) {
		key := host.Name + "\x00" + host.Address
		transport := transports[key]
		if transport == nil {
			var err error
			transport, err = dialSetupHost(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("connect deployment host: %w", err)
			}
			transports[key] = transport
		}
		return transport, nil
	}
	read := func(ctx context.Context, host setupconfig.Host, path string) ([]byte, error) {
		transport, err := transportFor(ctx, host)
		if err != nil {
			return nil, err
		}
		return readRemoteSkillSecret(ctx, transport, path)
	}

	klineDatasets, err := loadSkillKlineDatasets(filepath.Join(defaultSetupConfigDir, "collection-tasks.yaml"))
	if err != nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: %w", err)
	}
	return buildSkillDataAccessConfig(ctx, snapshot, space, read, klineDatasets)
}

func buildSkillDataAccessConfig(
	ctx context.Context,
	snapshot *setupconfig.Snapshot,
	spaceID string,
	read skillSecretReader,
	klineDatasets skillKlineDatasets,
) (dataAccessConfig, error) {
	if snapshot == nil || read == nil || klineDatasets == nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: dependencies are required")
	}
	spaceID = strings.TrimSpace(spaceID)
	if strings.EqualFold(spaceID, "crypto") {
		spaceID = "crypto"
	}
	if spaceID != "crypto" {
		return dataAccessConfig{}, fmt.Errorf("skill_config: unsupported space %q", spaceID)
	}
	var selected *setupconfig.SCFFetcherSpace
	for index := range snapshot.Manifest.SCFFetcher.Spaces {
		candidate := &snapshot.Manifest.SCFFetcher.Spaces[index]
		candidateSpaceID := strings.TrimSpace(candidate.SpaceID)
		if strings.EqualFold(candidateSpaceID, "crypto") {
			candidateSpaceID = "crypto"
		}
		if candidateSpaceID == spaceID {
			selected = candidate
			break
		}
	}
	if selected == nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: scf_fetcher has no configuration for space %q", spaceID)
	}
	storageHost := snapshot.Manifest.ControlHost
	if snapshot.Manifest.HasStorageHost() {
		storageHost = snapshot.Manifest.StorageHost
	}
	storageRoot := snapshot.Manifest.Paths.Resolved().StorageRoot

	storageRaw, err := read(ctx, storageHost, filepath.Join(storageRoot, "secrets/storage-internal-auth.env"))
	if err != nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: Storage auth unavailable")
	}
	primarySecret, err := collectorStoragePrimaryAuthSecret(storageRaw)
	if err != nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: Storage auth invalid")
	}
	storageAppKey := security.HMACSHA256Hex(primarySecret, []byte(skillConfigIdentity))
	primarySecret = ""
	storageRaw = nil
	binanceKline, err := klineDatasets("crypto", "binance_spot")
	if err != nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: %w", err)
	}
	if len(binanceKline) == 0 {
		return dataAccessConfig{}, fmt.Errorf("skill_config: no Binance spot kline collection task is configured")
	}
	stockKline, err := klineDatasets("stockcn", "cn_a_share")
	if err != nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: %w", err)
	}

	return dataAccessConfig{
		Version: 1,
		Storage: dataStorageAuthConfig{AppID: skillConfigIdentity, AppKey: storageAppKey},
		DataTypes: map[string]dataTypeConfig{
			"crypto": {
				DefaultExchange: "binance",
				Exchanges: map[string]exchangeConfig{
					"binance": {
						SpaceID: "crypto", SeriesTag: "venue:binance|market:spot|source:spot_http",
						KlineDatasets: binanceKline,
					},
				},
			},
			"stockcn": {
				DefaultExchange: "stockcn",
				Exchanges: map[string]exchangeConfig{
					"stockcn": {
						SpaceID: "stockcn", SeriesTag: "default",
						KlineDatasets: stockKline,
					},
				},
			},
		},
	}, nil
}

func readRemoteSkillSecret(ctx context.Context, transport setupssh.Client, path string) ([]byte, error) {
	if transport == nil || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("remote secret unavailable")
	}
	result, err := transport.Run(ctx, []string{
		"sh", "-lc",
		`set -eu
path="$1"
file_id() { stat -c '%d:%i:%s:%Y' "$1" 2>/dev/null || stat -f '%d:%i:%z:%m' "$1"; }
file_hash() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}';
  else shasum -a 256 "$1" | awk '{print $1}'; fi
}
attempt=1
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT HUP INT TERM
while [ "$attempt" -le 3 ]; do
  [ -f "$path" ] && [ ! -L "$path" ] || exit 1
  mode=$(stat -c '%a' "$path" 2>/dev/null || stat -f '%Lp' "$path")
  [ "$mode" = 600 ] || exit 1
  size=$(wc -c <"$path")
  [ "$size" -gt 0 ] && [ "$size" -le 4096 ] || exit 1
  before="$(file_id "$path"):$(file_hash "$path")"
  cp "$path" "$tmp"
  after="$(file_id "$path"):$(file_hash "$path")"
  if [ "$before" = "$after" ]; then cat "$tmp"; exit 0; fi
  attempt=$((attempt + 1))
done
exit 1`,
		"moox-read-skill-secret", path,
	}, nil)
	if err != nil || len(result.Stdout) == 0 || len(result.Stdout) > 4096 {
		return nil, fmt.Errorf("remote secret unavailable")
	}
	return []byte(result.Stdout), nil
}

func writeSkillConfigAtomic0600(path string, content []byte, rename func(string, string) error) (err error) {
	if rename == nil {
		return fmt.Errorf("rename dependency is required")
	}
	if info, statErr := os.Lstat(path); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("output %q must not be a symlink", path)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("output %q must be a regular file", path)
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		if err != nil {
			_ = os.Remove(tempPath)
		}
	}()
	if err = temp.Chmod(0o600); err != nil {
		return err
	}
	if _, err = temp.Write(content); err != nil {
		return err
	}
	if err = temp.Sync(); err != nil {
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	if err = rename(tempPath, path); err != nil {
		return err
	}
	return nil
}
