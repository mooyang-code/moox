package command

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/security"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const skillConfigIdentity = "moox-skill"

// skillSecretReader 返回 moox-skill 需要的密钥：外部调用方签名密钥（CallerKey JSON）和存储内部签名密钥文件，都取自 control。
type skillSecretReader func(context.Context) (callerKey, storageAuth []byte, err error)

// skillAccessEndpoint 返回 moox-skill 使用的外部接入：操作员机器不在任何 VPC 内，固定用 access@storage 的公网地址。
type skillAccessEndpoint func(context.Context) (servicecatalog.AccessEndpoint, error)

func newSetupExportSkillConfigCommand(deps setupDeps) *cobra.Command {
	var file, space, output string
	cmd := &cobra.Command{
		Use:   "export-skill-config",
		Short: "导出 Skill 使用的最小数据访问配置",
		RunE: func(cmd *cobra.Command, _ []string) error {
			space = strings.TrimSpace(space)
			output = strings.TrimSpace(output)
			if space == "" {
				return fmt.Errorf("必须指定 --space")
			}
			if output == "" {
				return fmt.Errorf("必须指定 --output")
			}
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			if err := rejectInputOutputCollision(file, output); err != nil {
				return fmt.Errorf("写入 Skill 配置: %w", err)
			}

			config, err := deps.exportSkillConfig(cmd.Context(), snapshot, space)
			if err != nil {
				return err
			}
			if err := config.validate(); err != nil {
				return fmt.Errorf("Skill 配置无效: %w", err)
			}
			raw, err := yaml.Marshal(config)
			if err != nil {
				return fmt.Errorf("编码 Skill 配置: %w", err)
			}
			if err := snapshot.VerifyUnchanged(); err != nil {
				return fmt.Errorf("config_changed")
			}
			if err := writeSkillConfigAtomic0600(output, raw, os.Rename); err != nil {
				return fmt.Errorf("写入 Skill 配置: %w", err)
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
		return dataAccessConfig{}, fmt.Errorf("skill_config: 缺少 moox.toml 快照")
	}
	read := func(ctx context.Context) ([]byte, []byte, error) {
		control := snapshot.Manifest.ControlHost()
		transport, err := dialSetupHost(ctx, control)
		if err != nil {
			return nil, nil, fmt.Errorf("连接 control: %w", err)
		}
		defer transport.Close()
		callerKey, err := ensureControlPrincipalKey(ctx, transport, control.Root, skillConfigIdentity)
		if err != nil {
			return nil, nil, err
		}
		storageAuth, err := readControlFile(ctx, transport, control.Root+"/secrets/storage-internal-auth.env")
		if err != nil {
			return nil, nil, err
		}
		return callerKey, storageAuth, nil
	}
	access := func(ctx context.Context) (servicecatalog.AccessEndpoint, error) {
		gateway, err := openControlGateway(snapshot.Manifest)
		if err != nil {
			return servicecatalog.AccessEndpoint{}, err
		}
		defer gateway.Close()
		view, err := gateway.Directory(ctx)
		if err != nil {
			return servicecatalog.AccessEndpoint{}, fmt.Errorf("读取控制面的服务目录: %w", err)
		}
		return view.Directory.AccessEndpointForRegion("", servicecatalog.AccessFallbackHostID)
	}
	klineDatasets, err := loadSkillKlineDatasets(filepath.Join(defaultSetupConfigDir, "collection-tasks.yaml"))
	if err != nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: %w", err)
	}
	return buildSkillDataAccessConfig(ctx, snapshot, space, access, read, klineDatasets)
}

func buildSkillDataAccessConfig(
	ctx context.Context,
	snapshot *setupconfig.Snapshot,
	spaceID string,
	access skillAccessEndpoint,
	read skillSecretReader,
	klineDatasets skillKlineDatasets,
) (dataAccessConfig, error) {
	if snapshot == nil || access == nil || read == nil || klineDatasets == nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: 缺少外部接入或密钥的读取方式")
	}
	if spaceID = strings.ToLower(strings.TrimSpace(spaceID)); spaceID != "crypto" {
		return dataAccessConfig{}, fmt.Errorf("skill_config: 不支持空间 %q", spaceID)
	}
	if len(snapshot.Manifest.HostsOf("storage-primary")) == 0 {
		return dataAccessConfig{}, fmt.Errorf("skill_config: moox.toml 的部署表中没有存储主服务")
	}
	endpoint, err := access(ctx)
	if err != nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: 选择外部接入: %w", err)
	}
	keyRaw, storageRaw, err := read(ctx)
	if err != nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: 读取 moox-skill 的签名密钥失败: %w", err)
	}
	callerKey, err := skillCallerKeyValue(keyRaw)
	if err != nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: %w", err)
	}
	primarySecret, err := collectorStoragePrimaryAuthSecret(storageRaw)
	if err != nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: Storage 应用层密钥无效")
	}
	storageAppKey := security.HMACSHA256Hex(primarySecret, []byte(skillConfigIdentity))
	binanceKline, err := klineDatasets("crypto", "binance_spot")
	if err != nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: %w", err)
	}
	if len(binanceKline) == 0 {
		return dataAccessConfig{}, fmt.Errorf("skill_config: 没有配置 Binance 现货 K 线采集任务")
	}
	stockKline, err := klineDatasets("stockcn", "cn_a_share")
	if err != nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: %w", err)
	}
	return dataAccessConfig{
		Version: 1,
		Access:  dataAccessEndpoint{Address: endpoint.Address, ID: endpoint.ID, Caller: skillConfigIdentity, Key: callerKey},
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

// skillCallerKeyValue 把 moox-skill 的密钥文件转换为 <key_id>:<secret>。
func skillCallerKeyValue(raw []byte) (string, error) {
	key, err := gatewayauth.ParseCallerKey(raw)
	if err != nil {
		return "", fmt.Errorf("解析 moox-skill 的签名密钥: %w", err)
	}
	credentials, err := key.Credentials()
	if err != nil {
		return "", err
	}
	if credentials.Caller != skillConfigIdentity {
		return "", fmt.Errorf("签名密钥属于调用方 %s，不是 %s", credentials.Caller, skillConfigIdentity)
	}
	return credentials.KeyID + ":" + credentials.Secret, nil
}

func writeSkillConfigAtomic0600(path string, content []byte, rename func(string, string) error) (err error) {
	if rename == nil {
		return fmt.Errorf("缺少重命名函数")
	}
	if info, statErr := os.Lstat(path); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("输出文件 %q 不能是符号链接（symlink）", path)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("输出文件 %q 必须是普通文件", path)
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
