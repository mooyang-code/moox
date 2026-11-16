package deploy

// 这个文件是外部入口还是独立的 access 组件时的临时做法，外部入口并入主机网关（批 4 T16）后整个删除：
// access 校验外部身份（scf-collector、moox-skill、collector-operator）用一份公钥表，放在 secrets/access/ 下。
// 公钥表由部署从控制机列出登记的身份公钥后生成；之后有身份登记或换了公钥，用 SyncAccessIdentities 把它推到各台 access。
// 并入主机网关之后公钥随快照下发，就没有这份文件了。

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"fmt"
	"path"
	"slices"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// accessVerificationFile 是公钥表在 secrets/ 下的相对路径。
const accessVerificationFile = "access/access-verification.json"

// accessVerification 从控制机列出登记着的外部身份公钥，生成 access 的公钥表。
func accessVerification(ctx context.Context, control setupssh.Client, cfg *topology.Config, catalog servicecatalog.Catalog) ([]byte, error) {
	keys, err := ListIdentityKeys(ctx, control, cfg)
	if err != nil {
		return nil, err
	}
	identities := map[string]ed25519.PublicKey{}
	for _, key := range keys {
		// moox-cli 不经外部入口，不进这张表。
		if !catalog.ExternalIdentity(key.Identity) {
			continue
		}
		public, err := gatewayauth.ParsePublicKey(key.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("control_admin_failed: 控制机上 %s 的公钥不合法", key.Identity)
		}
		identities[key.Identity] = public
	}
	return gatewayauth.EncodeIdentityFile(identities)
}

// SyncAccessIdentities 把最新的外部身份公钥表推到已经装了 access 的主机并重启 access。登记或更换了外部身份的公钥之后调用。
// 返回没推成的主机与原因（主机暂时连不上时不拦住调用方，之后对那台主机重新部署 access 即可）。
func SyncAccessIdentities(ctx context.Context, dial Dial, control setupssh.Client, cfg *topology.Config) (failed map[string]error) {
	failed = map[string]error{}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return map[string]error{"": err}
	}
	table, err := accessVerification(ctx, control, cfg, catalog)
	if err != nil {
		return map[string]error{cfg.ControlHost().Name: err}
	}
	layout := Layout{Root: cfg.Paths.DeployRoot}
	target := layout.Secret(accessVerificationFile)
	for _, host := range cfg.Hosts() {
		if !slices.Contains(cfg.Deployments[host.Name], "access") {
			continue
		}
		if err := pushAccessTable(ctx, dial, host, table, target); err != nil {
			failed[host.Name] = err
		}
	}
	return failed
}

func pushAccessTable(ctx context.Context, dial Dial, host topology.Host, table []byte, target string) error {
	client, err := dial(ctx, host)
	if err != nil {
		return err
	}
	defer client.Close()
	// 还没装过 access 的主机没有这个目录：等它部署时会带上最新的表。
	if _, err := client.Run(ctx, []string{"test", "-d", path.Dir(target)}, nil); err != nil {
		return nil
	}
	if err := client.Upload(ctx, bytes.NewReader(table), int64(len(table)), target+".new", 0o600); err != nil {
		return err
	}
	if output, err := client.Run(ctx, []string{"mv", "-f", target + ".new", target}, nil); err != nil {
		return fmt.Errorf("替换公钥表失败：%s", lastLine(output.Stderr))
	}
	// try-restart 只重启正在运行的：被停用的 access 不会被拉起来。
	if output, err := client.Run(ctx, []string{"systemctl", "--user", "try-restart", UnitName("access")}, nil); err != nil {
		return fmt.Errorf("重启 access 失败：%s", lastLine(output.Stderr))
	}
	return nil
}
