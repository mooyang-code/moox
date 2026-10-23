package command

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// Storage 内部写入身份（MOOX_STORAGE_PRIMARY_AUTH_SECRET）的本机副本。
//
// 导入历史数据、导出数据行、采集任务的 Storage 盘点都要这个身份。它原先只在 Storage 主机上，用户每次要登录主机去拉；
// 现在 `setup init`（以及 `setup storage-auth`）从 Storage 主机读取一次，存到本机操作员目录 `~/.config/moox/` 下的
// 0600 文件里（和 SSH 私钥同级，见《秘密管理与初始化录入设计》）：不放进 moox.toml——它的设计就是不含秘密、可提交、可被 Agent 读取。
// 解析顺序：环境变量 → 命令行指定的文件（存在时）→ 本机操作员目录里的副本。

const operatorStorageAuthFileName = "storage-internal-auth.env"

// operatorStorageAuthPath 返回本机副本的路径；MOOX_OPERATOR_DIR 可改目录（测试、多套环境）。
func operatorStorageAuthPath() (string, error) {
	dir := strings.TrimSpace(os.Getenv("MOOX_OPERATOR_DIR"))
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config", "moox")
	}
	return filepath.Join(dir, operatorStorageAuthFileName), nil
}

// saveOperatorStorageAuth 把 Primary 角色密钥写进本机副本（0600，原子替换）；内容没变就不写。
func saveOperatorStorageAuth(primarySecret string) (string, error) {
	primarySecret = strings.TrimSpace(primarySecret)
	if len(primarySecret) < 32 || !storageSecretValuePattern.MatchString(primarySecret) {
		return "", errors.New("storage_role_secrets_invalid")
	}
	path, err := operatorStorageAuthPath()
	if err != nil {
		return "", err
	}
	content := "MOOX_STORAGE_PRIMARY_AUTH_SECRET=" + primarySecret + "\n"
	if existing, err := os.ReadFile(path); err == nil && string(existing) == content {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".storage-auth-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return path, os.Rename(tmp.Name(), path)
}

// resolveStoragePrimarySecret 按「环境变量 → 命令行指定的文件 → 本机操作员目录副本」取 Primary 角色密钥。
func resolveStoragePrimarySecret(path string) (string, error) {
	if secret := strings.TrimSpace(os.Getenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET")); secret != "" {
		return secret, nil
	}
	candidates := []string{filepath.Clean(path)}
	if local, err := operatorStorageAuthPath(); err == nil {
		candidates = append(candidates, local)
	}
	for _, candidate := range candidates {
		raw, err := os.ReadFile(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("读取 Storage 鉴权文件失败: %w", err)
		}
		secret, err := parseStoragePrimarySecretFile(raw)
		if err != nil {
			return "", fmt.Errorf("Storage 鉴权文件 %s 无效", candidate)
		}
		return secret, nil
	}
	return "", errors.New("找不到 Storage 内部写入身份：设置环境变量 MOOX_STORAGE_PRIMARY_AUTH_SECRET，或用 --storage-auth-file 指定文件，" +
		"或执行一次 `moox-cli setup storage-auth` 从 Storage 主机读取并保存到本机操作员目录（之后不用再取）")
}

// newSetupStorageAuthCommand 从 Storage 主机读取 Primary 角色密钥，存到本机操作员目录；已有部署只需执行一次。
func newSetupStorageAuthCommand(deps setupDeps) *cobra.Command {
	var file, storageHost string
	cmd := &cobra.Command{
		Use:   "storage-auth",
		Short: "把 Storage 内部写入身份保存到本机操作员目录（导入、导出数据用，只需一次）",
		Long: `经 SSH 从 Storage 主机的私密运行环境读取 Primary 角色密钥，保存到本机 ~/.config/moox/storage-internal-auth.env（0600）。
此后 moox-cli import、data rows export、collector 的 Storage 盘点不用再设环境变量或指定 --storage-auth-file。
密钥不会打印。` + "`setup init`" + `读取 Storage 主机时也会顺带保存。`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			host, err := resolveStorageDeploymentHost(snapshot.Manifest, storageHost)
			if err != nil {
				return err
			}
			transport, err := dialSetupHost(cmd.Context(), host)
			if err != nil {
				return err
			}
			defer transport.Close()
			secrets, err := readRemoteStorageRoleSecrets(cmd.Context(), transport, snapshot.Manifest.Paths.Resolved().StorageRoot)
			if err != nil {
				return err
			}
			path, err := saveOperatorStorageAuth(secrets.Primary)
			if err != nil {
				return err
			}
			return writeSetupJSON(cmd, map[string]string{"status": "saved", "path": path})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&storageHost, "storage-host", "", "Storage 主机名称（只有一台时可省略）")
	return cmd
}

// parseStoragePrimarySecretFile 解析鉴权文件：本机副本只有 Primary 一行，旧部署器生成的文件还带 View 一行，两种都接受。
func parseStoragePrimarySecretFile(raw []byte) (string, error) {
	if secret, err := collectorStoragePrimaryAuthSecret(raw); err == nil {
		return secret, nil
	}
	normalized, err := normalizeSecretEnv(string(raw), []string{"MOOX_STORAGE_PRIMARY_AUTH_SECRET"})
	if err != nil {
		return "", err
	}
	if value, ok := strings.CutPrefix(strings.TrimSpace(normalized), "MOOX_STORAGE_PRIMARY_AUTH_SECRET="); ok && value != "" {
		return value, nil
	}
	return "", errors.New("primary auth secret is missing")
}
