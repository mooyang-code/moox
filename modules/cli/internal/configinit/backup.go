package configinit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/consoleclient"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"
)

// 管理后台上配置备份接口所在的服务（catalog 里 trpc.moox.admin.Setup 的 console_name）。
const backupService = "setup"

// newConsole 建立管理台连接；测试里替换成指向假管理台的客户端。
var newConsole = consoleclient.New

// BackupResult 只说明备份到了哪里、什么时候，不含任何配置值。
type BackupResult struct {
	Console   string    `json:"console"`
	UpdatedAt time.Time `json:"updated_at"`
}

// BackupFile 读取（并解开）本机的 moox.toml，用其中的管理员账号登录管理台，
// 用管理员密码加密整份配置后上传。服务端只保存密文，没有管理员密码解不开。
func BackupFile(ctx context.Context, path string, keys configcrypt.KeyStore) (BackupResult, error) {
	if keys == nil {
		keys = configcrypt.Default()
	}
	raw, err := readConfigFile(path)
	if err != nil {
		return BackupResult{}, err
	}
	plain := raw
	if configcrypt.IsEncrypted(raw) {
		if plain, err = configcrypt.Decrypt(raw, keys); err != nil {
			return BackupResult{}, err
		}
	}
	manifest, err := setupconfig.Parse(plain)
	if err != nil {
		return BackupResult{}, fmt.Errorf("当前的 moox.toml 校验没有通过：%w", err)
	}
	return backupPlain(ctx, plain, manifest)
}

// backupPlain 用配置里的管理员账号登录管理台（管理台服务器的地址），上传用管理员密码加密的备份。
func backupPlain(ctx context.Context, plain []byte, manifest setupconfig.Manifest) (BackupResult, error) {
	address := manifest.ControlHost().Address
	if address == "" {
		return BackupResult{}, errors.New("配置里没有管理台服务器，无法备份")
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	client, err := newConsole(consoleclient.Options{Address: address})
	if err != nil {
		return BackupResult{}, err
	}
	if err := client.Login(ctx, manifest.Admin.Username, manifest.Admin.Password); err != nil {
		var untrusted *consoleclient.UntrustedError
		if errors.As(err, &untrusted) {
			return BackupResult{}, fmt.Errorf("这台电脑不信任管理台的 HTTPS 证书；运行 moox-cli setup trust-browser 安装信任后再同步")
		}
		return BackupResult{}, err
	}
	envelope, err := configcrypt.SealBackup(plain, manifest.Admin.Password)
	if err != nil {
		return BackupResult{}, err
	}
	var response struct {
		RetInfo struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		} `json:"ret_info"`
		UpdatedAt consoleclient.Int64 `json:"updated_at"`
	}
	if err := client.Call(ctx, backupService, "PutConfigBackup", map[string]string{"envelope": envelope}, &response); err != nil {
		return BackupResult{}, err
	}
	if err := consoleclient.RetError(response.RetInfo.Code, response.RetInfo.Msg, "上传配置备份"); err != nil {
		return BackupResult{}, err
	}
	return BackupResult{Console: address, UpdatedAt: time.Unix(int64(response.UpdatedAt), 0)}, nil
}

// fetchBackup 登录管理台，取回配置备份并用管理员密码解开，返回明文配置。
func fetchBackup(ctx context.Context, address, fingerprint, username, password string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	client, err := newConsole(consoleclient.Options{Address: address, Fingerprint: fingerprint})
	if err != nil {
		return nil, err
	}
	if err := client.Login(ctx, username, password); err != nil {
		return nil, err
	}
	var response struct {
		RetInfo struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		} `json:"ret_info"`
		Envelope string `json:"envelope"`
	}
	if err := client.Call(ctx, backupService, "GetConfigBackup", struct{}{}, &response); err != nil {
		return nil, err
	}
	if response.RetInfo.Msg == "config_backup_missing" {
		return nil, errors.New("管理台上还没有配置备份：需要在原来的电脑上同步一次配置（打开配置页保存，或运行 moox-cli config backup）")
	}
	if err := consoleclient.RetError(response.RetInfo.Code, response.RetInfo.Msg, "取回配置备份"); err != nil {
		return nil, err
	}
	plain, err := configcrypt.OpenBackup(response.Envelope, password)
	if err != nil {
		return nil, err
	}
	if _, err := setupconfig.Parse(plain); err != nil {
		return nil, fmt.Errorf("取回的配置没有通过校验：%w", err)
	}
	return plain, nil
}
