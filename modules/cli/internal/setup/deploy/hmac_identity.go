package deploy

// 这个文件是 HMAC 鉴权阶段的临时做法，集中放在这里：
//   - 身份包里每个调用方的 HMAC 密钥落到 secrets/caller-<调用方>.key（T14 换成每主机一把 ed25519 私钥后删除）；
//   - access 组件的校验文件落到 secrets/access/（T16 外部入口并入主机网关后删除）；
//   - 因子引擎、归档 worker 的外部身份密钥，以及操作电脑的 moox-cli 密钥，经 SSH 从控制机取（T14 删除）。
// 身份材料由控制机上的 moox-admin-cli 导出，校验与本地落盘用 unitbundle 里现成的函数。

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
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
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitbundle"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
)

// operatorFiles 是操作电脑身份目录里由部署写入的文件（caller-moox-cli-*.key 另按前缀匹配）。
var operatorFiles = []string{"gateway-client.yaml", "gateway-bootstrap.json", "moox-ca.crt"}

// RemoveOperatorIdentity 删除这台电脑上的操作员身份。清空全部主机之后调用：新装的控制机会生成新的 CA 与密钥，旧身份留着会对不上。
func RemoveOperatorIdentity(directory string) error {
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if slices.Contains(operatorFiles, name) || strings.HasPrefix(name, "caller-moox-cli-") && strings.HasSuffix(name, ".key") {
			if err := os.Remove(filepath.Join(directory, name)); err != nil {
				return err
			}
		}
	}
	return nil
}

// adminPaths 是控制机上 moox-admin-cli 要用的几个位置。
type adminPaths struct{ binary, database, master, pki string }

func controlAdminPaths(layout Layout) adminPaths {
	return adminPaths{
		binary: layout.Bin("moox-admin-cli"), database: path.Join(layout.Data("admin"), "admin.db"),
		master: layout.Secret("admin-master.key"), pki: layout.Secret("pki"),
	}
}

// bootstrapTopology 是写进空库的全部主机与部署（含管理台终端用的 SSH 登录信息，所以按私有文件处理）。
func bootstrapTopology(cfg *topology.Config) (hostbundle.Topology, error) {
	result := hostbundle.Topology{Version: 1, ControlHostID: cfg.ControlHost().Name}
	for _, host := range cfg.Hosts() {
		components := slices.Clone(cfg.Deployments[host.Name])
		slices.Sort(components)
		entry := hostbundle.Host{HostID: host.Name, Label: host.Label, Address: host.Address, PrivateAddress: host.PrivateAddress, Region: host.Region, Components: components, Reachability: host.Reachability}
		ssh, err := host.ConsoleSSH()
		if err != nil {
			return result, err
		}
		if ssh != nil {
			entry.SSH = &hostbundle.SSH{Port: ssh.Port, User: ssh.User, AuthType: "private_key", Secret: ssh.PrivateKey}
		}
		result.Hosts = append(result.Hosts, entry)
	}
	return result, nil
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

// bootstrapControl 在控制机上执行 moox-admin-cli bootstrap：建库、CA、主密钥、登记全部主机与部署、各调用方密钥，
// 再补 EventBus 凭据与初始管理员。都是只建不改，重复执行不改已有内容。返回控制机自己的身份包（带操作电脑的身份）。
func bootstrapControl(ctx context.Context, control setupssh.Client, cfg *topology.Config, admin topology.Admin, stage string) (hostbundle.Metadata, error) {
	var metadata hostbundle.Metadata
	layout := Layout{Root: cfg.Paths.DeployRoot}
	paths := controlAdminPaths(layout)
	plan, err := bootstrapTopology(cfg)
	if err != nil {
		return metadata, err
	}
	raw, err := json.Marshal(plan)
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
	if err := runAdmin(ctx, control, "初始化 Admin", []string{paths.binary, "bootstrap", "--topology-file", topologyFile, "--db-path", paths.database,
		"--encryption-key-file", paths.master, "--pki-dir", paths.pki, "--output-dir", path.Join(stage, "bundles")}, nil, &metadata); err != nil {
		return metadata, err
	}
	var ensured struct {
		Status string `json:"status"`
	}
	if err := runAdmin(ctx, control, "生成 EventBus 凭据", []string{paths.binary, "eventbus-credentials", "ensure", "--db-path", paths.database,
		"--encryption-key-file", paths.master, "--node-id", plan.ControlHostID}, nil, &ensured); err != nil || ensured.Status != "ok" {
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

// exportHostBundle 让控制机为一台已登记的主机导出身份包。
func exportHostBundle(ctx context.Context, control setupssh.Client, cfg *topology.Config, hostID, stage string) (hostbundle.Metadata, error) {
	var metadata hostbundle.Metadata
	paths := controlAdminPaths(Layout{Root: cfg.Paths.DeployRoot})
	if output, err := control.Run(ctx, []string{"mkdir", "-p", "-m", "0700", stage, path.Join(stage, "bundles")}, nil); err != nil {
		return metadata, fmt.Errorf("control_admin_failed: 控制机上建目录失败：%s", lastLine(output.Stderr))
	}
	err := runAdmin(ctx, control, "导出主机 "+hostID+" 的身份", []string{paths.binary, "host-bundle", "--host-id", hostID, "--control-host-id", cfg.ControlHost().Name,
		"--db-path", paths.database, "--encryption-key-file", paths.master, "--pki-dir", paths.pki, "--output-dir", path.Join(stage, "bundles")}, nil, &metadata)
	return metadata, err
}

// exportEventBus 让控制机导出一组 EventBus 客户端角色；server 为真时带上 broker 的证书、私钥和用户表（只给 EventBus 所在主机）。
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
func materialOptions(cfg *topology.Config, host topology.Host, ca, hash string, operator bool) unitbundle.Options {
	control := cfg.ControlHost()
	components := slices.Clone(cfg.Deployments[host.Name])
	slices.Sort(components)
	return unitbundle.Options{
		HostID: host.Name, ControlHostID: control.Name, Address: host.Address, PrivateAddress: host.PrivateAddress, ControlAddress: control.Address,
		Outbound: host.Reachability != "", Components: components, ExpectedCA: ca, ExpectedHash: hash, AllowOperator: operator,
	}
}

// fetchIdentity 把控制机导出的身份包与 EventBus 材料取回本机临时目录、校验，再按新布局整理成 Identity。临时目录用完即删。
func fetchIdentity(ctx context.Context, control setupssh.Client, bundle hostbundle.Metadata, options unitbundle.Options, clients hostbundle.ClientMetadata, roles []string) (Identity, error) {
	identity := Identity{KeyIDs: map[string]string{}, Secrets: map[string][]byte{}, EventBus: map[string][]byte{}}
	temporary, err := os.MkdirTemp("", "moox-identity-")
	if err != nil {
		return identity, err
	}
	defer os.RemoveAll(temporary)
	local := filepath.Join(temporary, "bundle")
	if _, err := unitbundle.Fetch(ctx, control.Download, bundle, options, local); err != nil {
		return identity, fmt.Errorf("identity_invalid: 主机 %s 的身份包校验没有通过：%w", options.HostID, err)
	}
	root, err := fsutil.OpenPhysicalRoot(local, true)
	if err != nil {
		return identity, err
	}
	defer root.Close()
	for _, credential := range bundle.Credentials {
		caller := credential.Caller
		if caller == "host-gateway@"+options.HostID {
			caller = "host-gateway"
		}
		identity.KeyIDs[caller] = credential.KeyID
	}
	for _, file := range bundle.Files {
		raw, err := fsutil.ReadPrivate(root, file.Path, file.Size)
		if err != nil {
			return identity, err
		}
		switch {
		case file.Path == bundle.ConfigFile:
			if identity.Gateway, err = hostgatewayconfig.Decode(bytes.NewReader(raw)); err != nil {
				return identity, err
			}
		case file.Path == "certs/moox-ca.crt":
			identity.Secrets["moox-ca.crt"] = raw
		case file.Path == "certs/host-gateway/server.crt":
			identity.Secrets["host.crt"] = raw
		case file.Path == "certs/host-gateway/server.key":
			identity.Secrets["host.tls.key"] = raw
		case strings.HasPrefix(file.Path, "secrets/"):
			identity.Secrets[strings.TrimPrefix(file.Path, "secrets/")] = raw
		}
		// operator/ 下是操作电脑的身份，不放到目标机。
	}
	if len(roles) == 0 {
		return identity, nil
	}
	localClients := filepath.Join(temporary, "eventbus")
	if err := unitbundle.FetchEventBusClients(ctx, control.Download, clients, roles, localClients); err != nil {
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

// ExportCaller 在控制机上用 moox-admin-cli 只读导出一个外部调用方的签名密钥（给本机的 skill 等用）。
// 密钥写到控制机部署根下的临时目录，读回后立即删除。
func ExportCaller(ctx context.Context, control setupssh.Client, cfg *topology.Config, caller string) (gatewayauth.Credentials, error) {
	switch caller {
	case "scf-collector", "factor-engine", "archive-worker", "moox-skill":
	default:
		return gatewayauth.Credentials{}, errors.New("unknown external caller")
	}
	layout := Layout{Root: cfg.Paths.DeployRoot}
	paths := controlAdminPaths(layout)
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return gatewayauth.Credentials{}, err
	}
	output := path.Join(layout.Root, ".export-"+hex.EncodeToString(suffix))
	defer func() { _, _ = control.Run(context.WithoutCancel(ctx), []string{"rm", "-rf", "--", output}, nil) }()
	if result, err := control.Run(ctx, []string{"mkdir", "-m", "0700", output}, nil); err != nil {
		return gatewayauth.Credentials{}, fmt.Errorf("control_admin_failed: 控制机上建目录失败：%s", lastLine(result.Stderr))
	}
	var metadata struct {
		Caller  string `json:"caller"`
		KeyID   string `json:"key_id"`
		KeyFile string `json:"key_file"`
	}
	if err := runAdmin(ctx, control, "导出 "+caller+" 的签名密钥", []string{paths.binary, "keys", "export", "--caller", caller, "--output-dir", output,
		"--db-path", paths.database, "--encryption-key-file", paths.master}, nil, &metadata); err != nil {
		return gatewayauth.Credentials{}, err
	}
	if metadata.Caller != caller || metadata.KeyID == "" || metadata.KeyFile != path.Join(output, "caller-"+caller+".key") {
		return gatewayauth.Credentials{}, errors.New("control_admin_failed: 控制机返回的签名密钥信息不对")
	}
	raw, err := setupssh.ReadPrivateFile(ctx, control, metadata.KeyFile, 4096)
	if err != nil {
		return gatewayauth.Credentials{}, errors.New("control_admin_failed: 读不到导出的签名密钥")
	}
	defer clear(raw)
	key := strings.TrimSpace(string(raw))
	if len(key) < 32 {
		return gatewayauth.Credentials{}, errors.New("control_admin_failed: 导出的签名密钥不合法")
	}
	return gatewayauth.Credentials{Caller: caller, KeyID: metadata.KeyID, Secret: key}, nil
}
