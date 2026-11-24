package configinit

import (
	"context"
	"errors"
	"fmt"
	"time"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/tlstrust"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
)

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

// RefreshConsoleCA 用配置里的 SSH 登录管理台服务器，读取部署在那里的 MooX CA 根证书（公开证书，管理台的 HTTPS
// 证书由它签发），校验是 CA 证书后更新本机缓存。它在 {部署根}/secrets/moox-ca.crt，每台主机都有一份。
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
	result, err := client.Run(ctx, []string{"sh", "-c", `cat -- "$1/secrets/moox-ca.crt"`, "moox-read-moox-ca", cfg.Paths.DeployRoot}, nil)
	if err != nil {
		return fmt.Errorf("读取根证书失败")
	}
	return tlstrust.FileCAStore{}.Save(host.Address, []byte(result.Stdout))
}
