package command

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/packages/security"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const skillConfigIdentity = "moox-skill"

type skillSecretReader func(string) ([]byte, error)

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
			if err := writeAtomic0600(output, raw); err != nil {
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
	transport, err := dialSetupHost(ctx, snapshot.Manifest.ControlHost)
	if err != nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: connect control host: %w", err)
	}
	defer transport.Close()

	read := func(path string) ([]byte, error) {
		return readRemoteSkillSecret(ctx, transport, path)
	}
	return buildSkillDataAccessConfig(ctx, snapshot, space, read)
}

func buildSkillDataAccessConfig(
	_ context.Context,
	snapshot *setupconfig.Snapshot,
	spaceID string,
	read skillSecretReader,
) (dataAccessConfig, error) {
	if snapshot == nil || read == nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: dependencies are required")
	}
	spaceID = strings.TrimSpace(spaceID)
	if spaceID != "crypto_market" {
		return dataAccessConfig{}, fmt.Errorf("skill_config: unsupported space %q", spaceID)
	}
	var selected *setupconfig.SCFFetcherSpace
	for index := range snapshot.Manifest.SCFFetcher.Spaces {
		candidate := &snapshot.Manifest.SCFFetcher.Spaces[index]
		if strings.TrimSpace(candidate.SpaceID) == spaceID {
			selected = candidate
			break
		}
	}
	if selected == nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: scf_fetcher has no configuration for space %q", spaceID)
	}
	target := strings.TrimSpace(selected.StorageRPCGatewayTarget)
	if target == "" {
		return dataAccessConfig{}, fmt.Errorf("skill_config: space %q storage_rpc_gateway_target is required", spaceID)
	}
	targetNode := strings.TrimSpace(selected.StorageGatewayNodeID)
	if targetNode == "" {
		return dataAccessConfig{}, fmt.Errorf("skill_config: space %q storage_gateway_node_id is required", spaceID)
	}
	if _, err := findSetupHost(snapshot.Manifest, targetNode); err != nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: storage gateway node %q is not a configured host", targetNode)
	}

	controlRoot := snapshot.Manifest.Paths.Resolved().ControlRoot
	gatewayRaw, err := read(filepath.Join(controlRoot, "secrets/gateway-moox-skill.key"))
	if err != nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: Gateway credential unavailable")
	}
	gatewaySecret, err := normalizeSkillGatewaySecret(gatewayRaw)
	if err != nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: Gateway credential invalid")
	}

	storageRaw, err := read(filepath.Join(controlRoot, "secrets/storage-internal-auth.env"))
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

	return dataAccessConfig{
		Version: 1,
		Gateway: dataGatewayConfig{
			Target: target, TargetNode: targetNode, KeyID: skillConfigIdentity,
			Caller: skillConfigIdentity, Secret: gatewaySecret,
		},
		Storage: dataStorageAuthConfig{AppID: skillConfigIdentity, AppKey: storageAppKey},
		DataTypes: map[string]dataTypeConfig{
			"crypto": {
				DefaultExchange: "binance",
				Exchanges: map[string]exchangeConfig{
					"binance": {
						SpaceID: "crypto_market", SeriesTag: "venue:binance",
						KlineDatasets: map[string]string{"1m": "binance_spot_kline_1m"},
					},
				},
			},
		},
	}, nil
}

func normalizeSkillGatewaySecret(raw []byte) (string, error) {
	if len(raw) == 0 || len(raw) > 4096 {
		return "", fmt.Errorf("invalid credential")
	}
	secret := strings.TrimSpace(string(raw))
	if secret == "" || strings.ContainsAny(secret, "\r\n") || !storageSecretValuePattern.MatchString(secret) {
		return "", fmt.Errorf("invalid credential")
	}
	return secret, nil
}

func readRemoteSkillSecret(ctx context.Context, transport setupssh.Client, path string) ([]byte, error) {
	if transport == nil || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("remote secret unavailable")
	}
	result, err := transport.Run(ctx, []string{
		"sh", "-lc",
		`set -eu
path="$1"
[ -f "$path" ]
[ ! -L "$path" ]
mode=$(stat -c '%a' "$path")
[ "$mode" = 600 ]
size=$(wc -c <"$path")
[ "$size" -gt 0 ]
[ "$size" -le 4096 ]
cat "$path"`,
		"moox-read-skill-secret", path,
	}, nil)
	if err != nil || len(result.Stdout) == 0 || len(result.Stdout) > 4096 {
		return nil, fmt.Errorf("remote secret unavailable")
	}
	return []byte(result.Stdout), nil
}
