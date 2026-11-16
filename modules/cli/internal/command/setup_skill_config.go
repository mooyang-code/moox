package command

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/deploy"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/identitybundle"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func newSetupExportSkillConfigCommand(deps setupDeps) *cobra.Command {
	var file, space, output string
	var source setupSource
	cmd := &cobra.Command{
		Use:   "export-skill-config",
		Short: "导出 Skill 使用的最小数据访问配置",
		RunE: func(cmd *cobra.Command, _ []string) error {
			space = strings.TrimSpace(space)
			output = strings.TrimSpace(output)
			if space == "" {
				return fmt.Errorf("--space-id is required")
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
			if err := config.validateSkill(); err != nil {
				return fmt.Errorf("skill_config_invalid: %w", err)
			}
			raw, err := yaml.Marshal(config)
			if err != nil {
				return fmt.Errorf("encode skill config: %w", err)
			}
			// moox-skill 的私钥只在这台电脑上：存在操作员身份目录里（已有就复用，所以反复导出、重新打包 Skill 都是同一把），
			// 公钥登记到控制机；导出时把私钥抄一份到输出文件旁边的私有目录，随 Skill 包一起走。
			if err := source.resolveOperatorDir(); err != nil {
				return err
			}
			master := filepath.Join(source.operatorDir, "moox-skill.key")
			notes, err := ensureAndRegisterIdentity(cmd.Context(), deps, snapshot, "moox-skill", master)
			if err != nil {
				return err
			}
			keyFile := filepath.Join(filepath.Dir(output), config.GatewayClient.KeyFile)
			if err := copySkillKey(master, keyFile); err != nil {
				return err
			}
			if err := writeSkillConfigAtomic0600(output, raw, os.Rename); err != nil {
				return fmt.Errorf("write skill config: %w", err)
			}
			return writeSetupJSON(cmd, map[string]any{"status": "exported", "output": output, "key_file": keyFile, "notes": notes})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&space, "space-id", "", "要导出的 SCF Space ID")
	cmd.Flags().StringVar(&output, "output", "", "Skill 数据访问配置输出路径")
	cmd.Flags().StringVar(&source.operatorDir, "operator-dir", "", "这台电脑的操作员身份目录（默认 ~/.config/moox），moox-skill 的私钥存在这里")
	_ = cmd.MarkFlagRequired("space-id")
	_ = cmd.MarkFlagRequired("output")
	return cmd
}

// skillKeyFile 是 moox-skill 的私钥相对输出文件的位置。
var skillKeyFile = filepath.Join(".moox-skill-keys", "moox-skill.key")

// defaultSetupExportSkillConfig 组出 Skill 的数据访问配置：读哪些数据集、经哪台主机的外部入口、用哪把私钥签名。不连任何主机。
func defaultSetupExportSkillConfig(_ context.Context, snapshot *setupSnapshot, space string) (dataAccessConfig, error) {
	if snapshot == nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: setup snapshot is required")
	}
	klineDatasets, err := loadSkillKlineDatasets(filepath.Join(defaultSetupConfigDir, "collection-tasks.yaml"))
	if err != nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: %w", err)
	}
	config, err := buildSkillDataAccessConfig(space, klineDatasets)
	if err != nil {
		return dataAccessConfig{}, err
	}
	host := snapshot.Manifest.StorageHost()
	if host.Name == "" {
		host = snapshot.Manifest.ControlHost()
	}
	config.GatewayClient = &gatewayclient.ExternalFileConfig{Address: net.JoinHostPort(host.Address, "11004"), InstanceID: "access@" + host.Name, Caller: "moox-skill", KeyFile: skillKeyFile}
	return config, nil
}

// copySkillKey 把 moox-skill 的私钥抄到输出文件旁边的私有目录（0700 目录、0600 文件）。
func copySkillKey(master, target string) error {
	directory := filepath.Dir(target)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(directory); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("Skill 签名私钥所在的目录 %s 必须是权限 0700 的目录，不能是符号链接", directory)
	}
	key, err := gatewayauth.ReadSigningKey(master)
	if err != nil {
		return err
	}
	return writeSkillConfigAtomic0600(target, []byte(gatewayauth.EncodeSigningKey(key)+"\n"), os.Rename)
}

// ensureAndRegisterIdentity 给一个外部身份准备签名私钥并登记公钥：私钥文件已有就复用、没有才在本机生成（所在目录只有自己能进），
// 公钥经 SSH 登记到控制机（替换原来登记的，一个身份只有一把），再把公钥表推到各台外部入口。返回要转告用户的提醒。
func ensureAndRegisterIdentity(ctx context.Context, deps setupDeps, snapshot *setupSnapshot, identity, keyFile string) ([]string, error) {
	directory := filepath.Dir(keyFile)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(directory); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return nil, fmt.Errorf("签名私钥所在的目录 %s 必须是权限 0700 的目录，不能是符号链接", directory)
	}
	key, _, err := identitybundle.EnsureSigningKeyFile(keyFile)
	if err != nil {
		return nil, err
	}
	if err := deps.registerIdentity(ctx, snapshot, identity, identitybundle.PublicKey(key)); err != nil {
		return nil, err
	}
	return deps.syncAccessIdentities(ctx, snapshot), nil
}

// defaultSetupSyncAccessIdentities 把最新的外部身份公钥表推到各台 access；推不到的主机只提醒，不算失败。
func defaultSetupSyncAccessIdentities(ctx context.Context, snapshot *setupSnapshot) []string {
	control, err := dialSetupHost(ctx, snapshot.Manifest.ControlHost())
	if err != nil {
		return []string{fmt.Sprintf("公钥已登记，但没连上控制机去更新外部入口的公钥表（%v）。对装了 access 的主机运行 moox-cli setup deploy --host <主机> --component access", err)}
	}
	defer control.Close()
	notes := []string{}
	for host, err := range deploy.SyncAccessIdentities(ctx, dialSetupHost, control, snapshot.Manifest) {
		notes = append(notes, fmt.Sprintf("公钥已登记，但主机 %s 上的 access 还没拿到（%v）。运行 moox-cli setup deploy --host %s --component access", host, err, host))
	}
	return notes
}

func newSetupRegisterIdentityCommand(deps setupDeps) *cobra.Command {
	var file, identity, keyFile string
	cmd := &cobra.Command{
		Use:   "register-identity",
		Short: "给不属于主机的外部身份准备签名私钥并登记公钥（如采集名单干预工具的 collector-operator）",
		Long: `外部身份（collector-operator、moox-skill）的私钥只在使用它的电脑上：这条命令在 --key-file 生成一把（已有就复用），
经 SSH 把公钥登记到控制机，并更新各外部入口的公钥表。一个身份只有一把公钥，再登记一次就是替换，旧私钥随即不能用。
moox-skill 平时用 setup export-skill-config（它会顺带登记）；moox-cli 自己的密钥用 setup operator。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if identity != "collector-operator" && identity != "moox-skill" {
				return fmt.Errorf("--identity 只能是 collector-operator 或 moox-skill")
			}
			path, err := filepath.Abs(strings.TrimSpace(keyFile))
			if err != nil || strings.TrimSpace(keyFile) == "" {
				return fmt.Errorf("--key-file is required")
			}
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			notes, err := ensureAndRegisterIdentity(cmd.Context(), deps, snapshot, identity, path)
			if err != nil {
				return err
			}
			return writeSetupJSON(cmd, map[string]any{"status": "registered", "identity": identity, "key_file": path, "notes": notes})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "moox.toml 的路径")
	cmd.Flags().StringVar(&identity, "identity", "", "外部身份：collector-operator 或 moox-skill")
	cmd.Flags().StringVar(&keyFile, "key-file", "", "签名私钥文件（没有就生成；所在目录要只有自己能进）")
	_ = cmd.MarkFlagRequired("identity")
	_ = cmd.MarkFlagRequired("key-file")
	return cmd
}

func buildSkillDataAccessConfig(spaceID string, klineDatasets skillKlineDatasets) (dataAccessConfig, error) {
	if klineDatasets == nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: dependencies are required")
	}
	spaceID = strings.TrimSpace(spaceID)
	if strings.EqualFold(spaceID, "crypto") {
		spaceID = "crypto"
	}
	if spaceID != "crypto" {
		return dataAccessConfig{}, fmt.Errorf("skill_config: unsupported space %q", spaceID)
	}
	binanceKline, err := klineDatasets("crypto", "binance_spot")
	if err != nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: %w", err)
	}
	if len(binanceKline) == 0 {
		return dataAccessConfig{}, fmt.Errorf("skill_config: no Binance spot kline collection task is configured")
	}
	stockKline, err := klineDatasets("stockcn", "cn_hs300")
	if err != nil {
		return dataAccessConfig{}, fmt.Errorf("skill_config: %w", err)
	}

	return dataAccessConfig{
		Version: 1,
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
