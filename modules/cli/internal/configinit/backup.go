package configinit

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/consoleclient"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/tlstrust"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
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

// ConfigPair 是两份配置的明文与解析结果。
type ConfigPair struct {
	ConfigText  []byte
	SecretsText []byte
	Config      topology.Config
	Secrets     topology.Secrets
}

// parsePair 用 CLI 自己的严格解码与校验解析两份配置，并核对两份之间的关联。
func parsePair(config, secrets []byte) (ConfigPair, error) {
	cfg, err := topology.Parse(config)
	if err != nil {
		return ConfigPair{}, fmt.Errorf("moox.toml 校验没有通过：%w", err)
	}
	sec, err := topology.ParseSecrets(secrets)
	if err != nil {
		return ConfigPair{}, fmt.Errorf("moox.secrets.toml 校验没有通过：%w", err)
	}
	if err := sec.CheckFor(&cfg); err != nil {
		return ConfigPair{}, err
	}
	return ConfigPair{ConfigText: config, SecretsText: secrets, Config: cfg, Secrets: sec}, nil
}

// backupBundle 是备份信封里的明文：两份文件原样放在一起。
type backupBundle struct {
	Config  string `json:"moox.toml"`
	Secrets string `json:"moox.secrets.toml"`
}

// BackupFile 读取本机的 moox.toml 与同目录的 moox.secrets.toml（加密的先解开），用秘密文件里的管理员账号登录管理台，
// 用管理员密码加密两份文件后上传。服务端只保存密文，没有管理员密码解不开。
func BackupFile(ctx context.Context, path string, keys configcrypt.KeyStore) (BackupResult, error) {
	if keys == nil {
		keys = configcrypt.Default()
	}
	config, err := readConfigFile(path, false)
	if err != nil {
		return BackupResult{}, err
	}
	raw, err := readConfigFile(topology.SecretsPathFor(path), true)
	if err != nil {
		return BackupResult{}, err
	}
	secrets := raw
	if configcrypt.IsEncrypted(raw) {
		if secrets, err = configcrypt.Decrypt(raw, keys); err != nil {
			return BackupResult{}, err
		}
	}
	pair, err := parsePair(config, secrets)
	if err != nil {
		return BackupResult{}, err
	}
	return backupPlain(ctx, pair)
}

// backupPlain 用秘密文件里的管理员账号登录管理台（管理台服务器的地址），上传用管理员密码加密的两份文件。
func backupPlain(ctx context.Context, pair ConfigPair) (BackupResult, error) {
	address := pair.Config.ControlHost().Address
	if address == "" {
		return BackupResult{}, errors.New("配置里没有管理台服务器，无法备份")
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	client, err := loginForBackup(ctx, pair)
	var untrusted *consoleclient.UntrustedError
	if errors.As(err, &untrusted) {
		// 本机缓存的管理台 CA 可能是旧的（控制机重新初始化后会换 CA）：用配置里的 SSH 读一次控制机上发布的根证书再试。
		if refreshErr := refreshConsoleCA(ctx, &pair.Config); refreshErr != nil {
			return BackupResult{}, fmt.Errorf("这台电脑不信任管理台的 HTTPS 证书，也没能从管理台服务器取回它的根证书：%w", refreshErr)
		}
		client, err = loginForBackup(ctx, pair)
	}
	if errors.As(err, &untrusted) {
		return BackupResult{}, errors.New("管理台的 HTTPS 证书和它发布的根证书对不上，没有备份")
	}
	if err != nil {
		return BackupResult{}, err
	}
	plain, err := json.Marshal(backupBundle{Config: string(pair.ConfigText), Secrets: string(pair.SecretsText)})
	if err != nil {
		return BackupResult{}, err
	}
	envelope, err := configcrypt.SealBackup(plain, pair.Secrets.Admin.Password)
	clear(plain)
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

func loginForBackup(ctx context.Context, pair ConfigPair) (*consoleclient.Client, error) {
	address := pair.Config.ControlHost().Address
	client, err := newConsole(consoleclient.Options{Address: address, RootCAs: consoleRoots(address)})
	if err != nil {
		return nil, err
	}
	return client, client.Login(ctx, pair.Secrets.Admin.Username, pair.Secrets.Admin.Password)
}

// refreshConsoleCA 在测试里替换。
var refreshConsoleCA = RefreshConsoleCA

// RefreshConsoleCA 用配置里的 SSH 登录管理台服务器，读取部署在那里的 MooX CA 根证书（公开证书，管理台的 HTTPS
// 证书由它签发），校验是 CA 证书后更新本机缓存。旧部署里它随 host 单元投放在 {部署根}/host/current/certs/moox-ca.crt。
func RefreshConsoleCA(ctx context.Context, cfg *topology.Config) error {
	if cfg == nil {
		return errors.New("没有部署配置，无法连接管理台服务器")
	}
	host := cfg.ControlHost()
	client, err := setupssh.Dial(ctx, setupssh.Target{Name: host.Name, Address: host.Address, Port: host.Port, Username: host.Username}, "", setupssh.Options{Timeout: 15 * time.Second, IdentityFiles: host.IdentityFiles()})
	if err != nil {
		return err
	}
	defer client.Close()
	result, err := client.Run(ctx, []string{"sh", "-c", `cat -- "$1/host/current/certs/moox-ca.crt"`, "moox-read-moox-ca", cfg.Paths.DeployRoot}, nil)
	if err != nil {
		return fmt.Errorf("读取根证书失败")
	}
	return tlstrust.FileCAStore{}.Save(host.Address, []byte(result.Stdout))
}

// consoleRoots 返回校验管理台证书用的根证书：系统信任，加上部署时缓存在本机的 MooX CA 根证书（如果有）。
// 原来部署的电脑上就有这张根证书，备份不需要再改系统信任。
func consoleRoots(address string) *x509.CertPool {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if path := tlstrust.CAPath(address); path != "" {
		if pem, err := os.ReadFile(path); err == nil {
			pool.AppendCertsFromPEM(pem)
		}
	}
	return pool
}

// fetchBackup 登录管理台，取回配置备份并用管理员密码解开，返回两份文件的明文。
func fetchBackup(ctx context.Context, address, fingerprint, username, password string) (ConfigPair, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	client, err := newConsole(consoleclient.Options{Address: address, Fingerprint: fingerprint})
	if err != nil {
		return ConfigPair{}, err
	}
	if err := client.Login(ctx, username, password); err != nil {
		return ConfigPair{}, err
	}
	var response struct {
		RetInfo struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		} `json:"ret_info"`
		Envelope string `json:"envelope"`
	}
	if err := client.Call(ctx, backupService, "GetConfigBackup", struct{}{}, &response); err != nil {
		return ConfigPair{}, err
	}
	if response.RetInfo.Msg == "config_backup_missing" {
		return ConfigPair{}, errors.New("管理台上还没有配置备份：需要在原来的电脑上同步一次配置（打开配置页保存，或运行 moox-cli config backup）")
	}
	if err := consoleclient.RetError(response.RetInfo.Code, response.RetInfo.Msg, "取回配置备份"); err != nil {
		return ConfigPair{}, err
	}
	plain, err := configcrypt.OpenBackup(response.Envelope, password)
	if err != nil {
		return ConfigPair{}, err
	}
	var bundle backupBundle
	err = json.Unmarshal(plain, &bundle)
	clear(plain)
	if err != nil || bundle.Config == "" || bundle.Secrets == "" {
		return ConfigPair{}, errors.New("管理台上的配置备份不是两份文件的格式：请在原来的电脑上重新同步一次（运行 moox-cli config backup）")
	}
	pair, err := parsePair([]byte(bundle.Config), []byte(bundle.Secrets))
	if err != nil {
		return ConfigPair{}, fmt.Errorf("取回的配置没有通过校验：%w", err)
	}
	return pair, nil
}
