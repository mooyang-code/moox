package deploy

// 身份材料：每台主机一把 ed25519 签名私钥（{部署根}/secrets/host.key，本机全部组件共用），由控制机上的
// moox-admin-cli 生成、随身份包经这台电脑中转到目标机，Admin 只登记公钥。操作电脑的 moox-cli 私钥只在本机生成，
// 首装时由控制机的 bootstrap 登记公钥。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/identitybundle"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
	"google.golang.org/protobuf/encoding/protojson"
)

// adminPaths 是控制机上 moox-admin-cli 要用的几个位置。
type adminPaths struct{ binary, database, master, pki string }

func controlAdminPaths(layout Layout) adminPaths {
	return adminPaths{
		binary: layout.Bin("moox-admin-cli"), database: path.Join(layout.Data("admin"), "admin.db"),
		master: layout.Secret("admin-master.key"), pki: layout.Secret("pki"),
	}
}

// runAdmin 在控制机上执行一条 moox-admin-cli 命令，把标准输出的一个 JSON 文档解到 out。
func runAdmin(ctx context.Context, control setupssh.Client, what string, argv []string, stdin io.Reader, out any) error {
	output, err := control.Run(ctx, argv, stdin)
	if err != nil {
		return fmt.Errorf("control_admin_failed: 控制机上%s失败：%s", what, lastLine(output.Stderr))
	}
	decoder := json.NewDecoder(strings.NewReader(output.Stdout))
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("control_admin_failed: 控制机上%s的输出无法解析", what)
	}
	return nil
}

// currentKeys 是目标机上现有签名私钥对应的公钥（没有私钥、或读不出来时为空）。
type currentKeys struct{ host, scfCollector string }

// arguments 是传给 moox-admin-cli 的参数：控制机据此判断沿用还是重新签发。
func (c currentKeys) arguments() []string {
	var args []string
	if c.host != "" {
		args = append(args, "--current-sign-public-key", c.host)
	}
	if c.scfCollector != "" {
		args = append(args, "--current-scf-collector-public-key", c.scfCollector)
	}
	return args
}

// readCurrentKeys 在目标机上算出现有私钥的公钥：私钥不离开目标机，只把公钥带回来。主机网关的命令行工具每台主机都有；
// 工具或私钥不在（全新的主机）时公钥为空，控制机会签发新的。
func readCurrentKeys(ctx context.Context, target setupssh.Client, layout Layout, collector bool) currentKeys {
	read := func(name string) string {
		output, err := target.Run(ctx, []string{layout.Bin("moox-host-gateway-cli"), "sign-public-key", "--key-file", layout.Secret(name)}, nil)
		if err != nil {
			return ""
		}
		public := strings.TrimSpace(output.Stdout)
		if _, err := gatewayauth.ParsePublicKey(public); err != nil {
			return ""
		}
		return public
	}
	keys := currentKeys{host: read(hostbundle.HostKeyFile)}
	if collector {
		keys.scfCollector = read(hostbundle.SCFCollectorKeyFile)
	}
	return keys
}

// bootstrapControl 在控制机上执行 moox-admin-cli bootstrap：建库、CA、主密钥、登记这台电脑的 moox-cli 公钥，
// 再执行 eventbus-credentials ensure 与 user ensure 补 EventBus 凭据与初始管理员，这些只建不改；主机与部署连同启停状态按 moox.toml 整份覆盖。返回控制机自己的身份包清单。
func bootstrapControl(ctx context.Context, control setupssh.Client, cfg *topology.Config, admin topology.Admin, operatorPublicKey string, current currentKeys, stage string) (hostbundle.Metadata, error) {
	var metadata hostbundle.Metadata
	layout := Layout{Root: cfg.Paths.DeployRoot}
	paths := controlAdminPaths(layout)
	plan, err := cfg.Declared()
	if err != nil {
		return metadata, err
	}
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(plan)
	if err != nil {
		return metadata, err
	}
	defer clear(raw)
	if output, err := control.Run(ctx, []string{"mkdir", "-p", "-m", "0700", stage, path.Join(stage, "bundles"), paths.pki}, nil); err != nil {
		return metadata, fmt.Errorf("control_admin_failed: 控制机上建目录失败：%s", lastLine(output.Stderr))
	}
	topologyFile := path.Join(stage, "topology.json")
	if err := control.Upload(ctx, bytes.NewReader(raw), int64(len(raw)), topologyFile, 0o600); err != nil {
		return metadata, err
	}
	argv := append([]string{paths.binary, "bootstrap", "--topology-file", topologyFile, "--db-path", paths.database,
		"--encryption-key-file", paths.master, "--pki-dir", paths.pki, "--output-dir", path.Join(stage, "bundles"),
		"--operator-public-key", operatorPublicKey}, current.arguments()...)
	if err := runAdmin(ctx, control, "初始化 Admin", argv, nil, &metadata); err != nil {
		return metadata, err
	}
	var ensured struct {
		Status string `json:"status"`
	}
	if err := runAdmin(ctx, control, "生成 EventBus 凭据", []string{paths.binary, "eventbus-credentials", "ensure", "--db-path", paths.database,
		"--encryption-key-file", paths.master}, nil, &ensured); err != nil || ensured.Status != "ok" {
		return metadata, errors.Join(err, errors.New("control_admin_failed: 控制机上没有生成 EventBus 凭据"))
	}
	if err := admin.Validate(); err != nil {
		return metadata, err
	}
	var user struct {
		Status string `json:"status"`
	}
	if err := runAdmin(ctx, control, "创建管理员", []string{paths.binary, "user", "ensure", "--db-path", paths.database, "--username", admin.Username, "--password-stdin"},
		strings.NewReader(admin.Password+"\n"), &user); err != nil || user.Status != "ok" {
		return metadata, errors.Join(err, errors.New("control_admin_failed: 控制机上没有创建管理员"))
	}
	return metadata, nil
}

// exportHostBundle 让控制机为一台已登记的主机签发身份包。rotate 为真时换掉这台主机的签名密钥。
func exportHostBundle(ctx context.Context, control setupssh.Client, cfg *topology.Config, hostID string, current currentKeys, rotate bool, stage string) (hostbundle.Metadata, error) {
	var metadata hostbundle.Metadata
	paths := controlAdminPaths(Layout{Root: cfg.Paths.DeployRoot})
	if output, err := control.Run(ctx, []string{"mkdir", "-p", "-m", "0700", stage, path.Join(stage, "bundles")}, nil); err != nil {
		return metadata, fmt.Errorf("control_admin_failed: 控制机上建目录失败：%s", lastLine(output.Stderr))
	}
	argv := append([]string{paths.binary, "host-bundle", "--host-id", hostID, "--control-host-id", cfg.ControlHost().Name,
		"--db-path", paths.database, "--encryption-key-file", paths.master, "--pki-dir", paths.pki, "--output-dir", path.Join(stage, "bundles")}, current.arguments()...)
	if rotate {
		argv = append(argv, "--rotate-sign-key")
	}
	err := runAdmin(ctx, control, "签发主机 "+hostID+" 的身份", argv, nil, &metadata)
	return metadata, err
}

// exportEventBus 让控制机导出一组 EventBus 客户端角色；server 为真时带上 broker 的用户表（只给 EventBus 所在主机）。
// 导出里没有证书：EventBus 用所在主机的主机证书，客户端用身份包里的根证书校验。
func exportEventBus(ctx context.Context, control setupssh.Client, cfg *topology.Config, roles []string, server bool, stage string) (hostbundle.ClientMetadata, error) {
	var metadata hostbundle.ClientMetadata
	paths := controlAdminPaths(Layout{Root: cfg.Paths.DeployRoot})
	argv := []string{paths.binary, "eventbus-credentials", "export-clients", "--db-path", paths.database, "--encryption-key-file", paths.master,
		"--node-id", cfg.ControlHost().Name, "--roles", strings.Join(roles, ","), "--output-dir", path.Join(stage, "eventbus")}
	if server {
		argv = append(argv, "--server")
	}
	err := runAdmin(ctx, control, "导出 EventBus 凭据", argv, nil, &metadata)
	return metadata, err
}

// materialOptions 是校验一台主机身份包用的期望值。
func materialOptions(cfg *topology.Config, host topology.Host, ca string) identitybundle.Options {
	return identitybundle.Options{
		HostID: host.Name, ControlHostID: cfg.ControlHost().Name, Address: host.Address, PrivateAddress: host.PrivateAddress,
		Outbound: host.Reachability != "", Collector: slices.Contains(cfg.Deployments[host.Name], "collector"), ExpectedCA: ca,
	}
}

// fetchIdentity 把控制机签发的身份包与 EventBus 材料取回这台电脑、校验，再按部署根布局整理成 Identity。
// 私钥只在内存里经过这台电脑，不落盘；EventBus 材料的临时目录用完即删。
func fetchIdentity(ctx context.Context, control setupssh.Client, bundle hostbundle.Metadata, options identitybundle.Options, clients hostbundle.ClientMetadata, roles []string) (Identity, error) {
	identity := Identity{SignPublicKey: bundle.SignPublicKey, SCFCollectorPublicKey: bundle.SCFCollectorPublicKey, Secrets: map[string][]byte{}, EventBus: map[string][]byte{}}
	files, err := identitybundle.Fetch(ctx, control.Download, bundle, options)
	if err != nil {
		return identity, fmt.Errorf("identity_invalid: 主机 %s 的身份包校验没有通过：%w", options.HostID, err)
	}
	// 身份包里的文件名就是 secrets/ 下的文件名。
	for name, raw := range files {
		identity.Secrets[name] = raw
	}
	if len(roles) == 0 {
		return identity, nil
	}
	temporary, err := os.MkdirTemp("", "moox-identity-")
	if err != nil {
		return identity, err
	}
	defer os.RemoveAll(temporary)
	// macOS 的临时目录在 /var 下，/var 是指向 /private/var 的符号链接；身份材料只认不经符号链接的真实路径。
	if temporary, err = filepath.EvalSymlinks(temporary); err != nil {
		return identity, err
	}
	localClients := filepath.Join(temporary, "eventbus")
	if err := identitybundle.FetchEventBusClients(ctx, control.Download, clients, roles, localClients); err != nil {
		return identity, fmt.Errorf("identity_invalid: EventBus 凭据校验没有通过：%w", err)
	}
	clientRoot, err := fsutil.OpenPhysicalRoot(localClients, true)
	if err != nil {
		return identity, err
	}
	defer clientRoot.Close()
	for _, file := range clients.Files {
		raw, err := fsutil.ReadPrivate(clientRoot, file.Path, file.Size)
		if err != nil {
			return identity, err
		}
		identity.EventBus[file.Path] = raw
	}
	return identity, nil
}

// IdentityKey 是 Admin 里登记的一个不属于主机的身份的公钥。
type IdentityKey struct {
	Identity  string `json:"identity"`
	PublicKey string `json:"public_key"`
}

// ListIdentityKeys 经 SSH 在控制机上列出登记着的身份公钥（公开信息）。
func ListIdentityKeys(ctx context.Context, control setupssh.Client, cfg *topology.Config) ([]IdentityKey, error) {
	paths := controlAdminPaths(Layout{Root: cfg.Paths.DeployRoot})
	var listing struct {
		Status     string        `json:"status"`
		Identities []IdentityKey `json:"identities"`
	}
	if err := runAdmin(ctx, control, "列出身份公钥", []string{paths.binary, "keys", "list", "--db-path", paths.database}, nil, &listing); err != nil || listing.Status != "ok" {
		return nil, errors.Join(err, errors.New("control_admin_failed: 控制机上没有列出身份公钥"))
	}
	return listing.Identities, nil
}

// RegisterIdentity 经 SSH 在控制机上登记（或替换）一个不属于主机的身份的公钥（如 moox-cli）。
// 信任根是这台电脑到控制机的 SSH：能 SSH 上控制机就能登记。各网关在下一次取快照时（几秒内）认新公钥，旧私钥随即作废。
func RegisterIdentity(ctx context.Context, control setupssh.Client, cfg *topology.Config, identity, publicKey string) error {
	if _, err := gatewayauth.ParsePublicKey(publicKey); err != nil {
		return err
	}
	paths := controlAdminPaths(Layout{Root: cfg.Paths.DeployRoot})
	var result struct {
		Status string `json:"status"`
	}
	if err := runAdmin(ctx, control, "登记 "+identity+" 的公钥", []string{paths.binary, "keys", "set", "--identity", identity, "--public-key", publicKey, "--db-path", paths.database}, nil, &result); err != nil || result.Status != "ok" {
		return errors.Join(err, fmt.Errorf("control_admin_failed: 控制机上没有登记 %s 的公钥", identity))
	}
	return nil
}
