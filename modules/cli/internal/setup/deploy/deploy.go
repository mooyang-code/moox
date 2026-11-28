package deploy

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/bundle"
	setupdataformat "github.com/mooyang-code/moox/modules/cli/internal/setup/dataformat"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/deployscan"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/fetchunit"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/identitybundle"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
)

//go:embed install.sh
var installScript string

//go:embed ready.py
var readyScript string

// DefaultReadyTimeout 是一次部署等组件就绪的时限。
const DefaultReadyTimeout = 3 * time.Minute

// Dial 连接一台主机。
type Dial func(context.Context, topology.Host) (setupssh.Client, error)

// Options 是一次部署的参数。
type Options struct {
	Config *topology.Config
	// Secrets 只在两处用到：控制机部署时的初始管理员，以及打开了 logging.cls 时的写入密钥。
	Secrets *topology.Secrets
	// HostID 是要部署的主机；Components 为空表示这台主机上的全部组件。
	HostID     string
	Components []string
	// RepositoryRoot 是源码或产物包目录：配置模板与单元模板从这里读。
	RepositoryRoot string
	// BinaryDirectory 是本机预先编译好的 Linux 二进制目录（源码目录部署）；Bundle 非空且它为空时由目标机按清单下载组件包。
	BinaryDirectory string
	Bundle          *bundle.Manifest
	// OperatorDirectory 是这台电脑的操作员身份目录（~/.config/moox）。
	OperatorDirectory string
	// RotateHostKey 显式要求换掉这台主机的签名密钥：旧私钥立即作废，这台主机上的组件都要重启。控制机之外的主机才支持。
	RotateHostKey bool
	// Version、Commit 写进 version.json。
	Version, Commit string
	Dial            Dial
	// SyncTopology 把 moox.toml 的整份主机与部署声明（含启停）写进 Admin；部署控制机时由 bootstrap 做，不调用它。
	// 部署其他主机时最先调用，不依赖目标机：主机连不上也照常写登记（只出站主机的暂停接入靠这一点）。
	SyncTopology func(ctx context.Context) error
	// ObjectStoreSecret 读归档 worker 外部对象存储的凭据文件内容（配了 [archive_object_store] 时）。
	ObjectStoreSecret func(ctx context.Context, secretID string) ([]byte, error)
	// Log 接收进度；ReadyTimeout 为 0 用默认值。
	Log          io.Writer
	ReadyTimeout time.Duration
}

// ComponentResult 是一个组件这次部署的结果。Action：started（新装或原来没在运行）、restarted、unchanged、
// disabled（moox.toml 声明停用：只装文件，单元停掉且不开机自启）。
type ComponentResult struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	Ready  bool   `json:"ready"`
	Detail string `json:"detail,omitempty"`
}

// Result 是一次部署的结果。
type Result struct {
	Host       string            `json:"host"`
	Version    string            `json:"version"`
	Components []ComponentResult `json:"components"`
	// Removed 是整台部署时停掉并删除的、不再属于这台主机的组件。
	Removed  []string `json:"removed,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

func (o Options) logf(format string, args ...any) {
	if o.Log != nil {
		fmt.Fprintf(o.Log, format+"\n", args...)
	}
}

// scope 决定这次部署哪些组件、是不是整台部署。换主机签名密钥时按整台部署处理：指纹里有签名公钥，本机每个组件的配置都变了，
// 要按启动顺序（网关在前）重启并记进 version.json，否则下一次整台部署会把没选中的组件再重启一遍。
func (o Options) scope(hostName string, hostComponents []string) (selected []string, full bool, err error) {
	if len(o.Components) == 0 {
		return hostComponents, true, nil
	}
	for _, id := range o.Components {
		if !slices.Contains(hostComponents, id) {
			return nil, false, fmt.Errorf("component_not_on_host: 主机 %s 上没有组件 %s（这台主机有：%s）", hostName, id, strings.Join(hostComponents, "、"))
		}
	}
	if o.RotateHostKey {
		o.logf("%s: 要换主机签名密钥，按整台部署处理（忽略 --component）", hostName)
		return hostComponents, true, nil
	}
	return slices.Clone(o.Components), false, nil
}

// LingerCommand 是没开 linger 时要用户在目标机上执行的命令。
func LingerCommand(user string) string { return "sudo loginctl enable-linger " + user }

// JournaldCommand 是 journald 没有持久化时建议执行的命令。
const JournaldCommand = "sudo mkdir -p /var/log/journal && sudo systemctl restart systemd-journald"

// Deploy 把一台主机部署到 moox.toml 描述的样子。
func Deploy(ctx context.Context, options Options) (Result, error) {
	result := Result{Host: options.HostID, Version: options.Version}
	cfg := options.Config
	if cfg == nil || options.Dial == nil || options.RepositoryRoot == "" {
		return result, errors.New("部署需要配置、SSH 连接与源码或产物包目录")
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return result, err
	}
	hostComponents, err := cfg.HostComponents(options.HostID)
	if err != nil {
		return result, err
	}
	host, control, layout := cfg.HostByID(options.HostID), cfg.ControlHost(), Layout{Root: cfg.Paths.DeployRoot}
	selected, full, err := options.scope(host.Name, hostComponents)
	if err != nil {
		return result, err
	}
	if host.Name != control.Name {
		if options.SyncTopology == nil {
			return result, errors.New("部署控制机之外的主机要先把 moox.toml 的主机与部署同步到 Admin")
		}
		if err := options.SyncTopology(ctx); err != nil {
			return result, fmt.Errorf("把 moox.toml 的主机与部署同步到 Admin: %w", err)
		}
	}
	if !host.Enabled {
		return stopDisabledHost(ctx, options, host, selected, result)
	}

	// 预检：linger 没开就停下；journald 没持久化只提醒。
	reports, err := deployscan.Scan(ctx, cfg, deployscan.Dial(options.Dial), deployscan.Options{Host: host.Name})
	if err != nil {
		return result, err
	}
	report := reports[0]
	if !report.Linger {
		return result, fmt.Errorf("linger_required: 主机 %s 的用户 %s 没开 linger，登出后服务会被停掉。在这台主机上执行：%s，然后重跑", host.Name, host.Username, LingerCommand(host.Username))
	}
	if !report.Journald {
		result.Warnings = append(result.Warnings, fmt.Sprintf("主机 %s 的 journald 没有持久化，重启后看不到之前的标准输出日志。建议在这台主机上执行：%s", host.Name, JournaldCommand))
	}
	existing, err := ParseVersion(report.Version)
	if err != nil {
		return result, fmt.Errorf("version_invalid: 主机 %s 的 %s 无法解析，确认后删掉它再重跑", host.Name, layout.Version())
	}
	catalogSHA := bundle.CatalogSHA256()
	// 只部署个别组件时，服务目录变了要把本机的主机网关一起升级：路由是网关用内嵌目录算的。
	if !full && existing != nil && existing.CatalogSHA256 != catalogSHA && !slices.Contains(selected, "host-gateway") {
		selected = append(selected, "host-gateway")
		options.logf("服务目录和主机 %s 上记的不一样，主机网关一起升级", host.Name)
	}
	if selected, err = catalog.StartOrder(selected); err != nil {
		return result, err
	}
	formatReport, err := setupdataformat.CheckHostNow(host.Name, selected, report.DataFormats)
	if err != nil {
		return result, err
	}
	if err := setupdataformat.IncompatibleError(formatReport); err != nil {
		return result, err
	}

	target, err := options.Dial(ctx, host)
	if err != nil {
		return result, err
	}
	defer target.Close()
	if err := checkPlatform(ctx, target, options.Bundle != nil && options.BinaryDirectory == ""); err != nil {
		return result, err
	}
	if output, err := target.Run(ctx, append([]string{"bash", "-s", "--", "prepare", layout.Root}, hostComponents...), strings.NewReader(installScript)); err != nil {
		return result, fmt.Errorf("target_prepare_failed: 主机 %s：%s", host.Name, lastLine(output.Stderr))
	}

	// 二进制：先放到位（控制机接下来要用新的 moox-admin-cli）。
	next := Version{Version: options.Version, Commit: options.Commit, CatalogSHA256: catalogSHA, DeployedAt: time.Now().UTC().Format(time.RFC3339), Components: map[string]ComponentVersion{}}
	if existing != nil {
		for id, component := range existing.Components {
			if !full || slices.Contains(hostComponents, id) {
				next.Components[id] = component
			}
		}
	}
	binaries, err := options.binaries(catalog, selected)
	if err != nil {
		return result, err
	}
	if err := options.installBinaries(ctx, target, layout, catalog, selected, binaries, existing); err != nil {
		return result, err
	}

	// 身份材料。
	identity, issued, err := options.identity(ctx, target, catalog, host, control, selected, existing == nil)
	if err != nil {
		return result, err
	}
	if redeployWholeHost(issued.hostKey, full, existing != nil) {
		options.logf("%s: 签发了新的主机签名密钥，改为整台部署", host.Name)
		options.Components = nil
		return Deploy(ctx, options)
	}
	request := Request{Config: cfg, Catalog: catalog, Host: host, Components: selected, HostComponents: hostComponents, ReadSource: options.readSource, Identity: identity}
	if cfg.Logging.CLS.Enabled {
		if options.Secrets == nil {
			return result, errors.New("config_invalid: moox.toml 打开了 logging.cls.enabled，部署要读 moox.secrets.toml 里的写入密钥")
		}
		request.CLS = options.Secrets.Logging.CLS
	}
	if slices.Contains(selected, "archive-worker") && cfg.ArchiveObjectStore != nil {
		if options.ObjectStoreSecret == nil {
			return result, errors.New("归档 worker 用外部对象存储时，部署要能从 Admin 读它的密钥")
		}
		if request.ObjectStoreEnv, err = options.ObjectStoreSecret(ctx, cfg.ArchiveObjectStore.SecretID); err != nil {
			return result, fmt.Errorf("读取归档对象存储密钥: %w", err)
		}
		defer clear(request.ObjectStoreEnv)
	}
	rendered, err := Render(request)
	if err != nil {
		return result, err
	}

	// 装配置并按顺序重启有变化的组件。
	var list bytes.Buffer
	for _, id := range selected {
		component := ComponentVersion{Binaries: binaries[id], Fingerprint: rendered.Fingerprints[id]}
		flag := func(value bool) string {
			if value {
				return "1"
			}
			return "0"
		}
		fmt.Fprintf(&list, "%s %s %s\n", id, flag(existing.changed(id, component)), flag(cfg.UnitEnabled(host.Name, id)))
		next.Components[id] = component
	}
	versionRaw, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return result, err
	}
	entries := map[string]File{"components.list": {Data: list.Bytes(), Mode: 0o600}, "version.json": {Data: append(versionRaw, '\n'), Mode: 0o600}}
	for name, file := range rendered.Files {
		entries[name] = file
	}
	for name, unit := range rendered.Units {
		entries["units/"+name] = File{Data: unit, Mode: 0o644}
	}
	if full {
		var keep bytes.Buffer
		for _, id := range hostComponents {
			fmt.Fprintln(&keep, UnitName(id))
		}
		entries["units.keep"] = File{Data: keep.Bytes(), Mode: 0o600}
	}
	output, err := runInstall(ctx, target, layout, "apply", entries, nil)
	if err != nil {
		return result, fmt.Errorf("install_failed: 主机 %s：%s", host.Name, lastLine(output.Stderr))
	}
	actions := map[string]string{}
	for _, line := range strings.Split(output.Stdout, "\n") {
		fields := strings.Fields(line)
		switch {
		case len(fields) == 3 && fields[0] == "component":
			actions[fields[1]] = fields[2]
		case len(fields) == 2 && fields[0] == "removed":
			result.Removed = append(result.Removed, strings.TrimPrefix(fields[1], "moox-"))
		}
	}
	for _, id := range selected {
		action, ok := actions[id]
		if !ok {
			return result, fmt.Errorf("install_failed: 主机 %s 的安装脚本没有报告组件 %s 的结果", host.Name, id)
		}
		result.Components = append(result.Components, ComponentResult{ID: id, Action: action})
		options.logf("%s %s: %s", host.Name, id, action)
	}
	return result, waitComponents(ctx, target, catalog, host.Name, &result, options.ReadyTimeout)
}

// stopDisabledHost 处理 moox.toml 里停用的主机：登记已经写进 Admin（网关随之只认它的 host-gateway），
// 这里只尽力停掉除 host-gateway 外的单元，不装文件。主机连不上只记警告：登记已经生效，进程等主机能连上后再部署一次停掉。
func stopDisabledHost(ctx context.Context, options Options, host topology.Host, selected []string, result Result) (Result, error) {
	target, err := options.Dial(ctx, host)
	if err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("主机 %s 已停用，登记已写入 Admin；它现在连不上（%v），上面的进程没有停，能连上后再部署一次", host.Name, err))
		return result, nil
	}
	defer target.Close()
	for _, id := range selected {
		if id == "host-gateway" {
			continue
		}
		if _, err := Operate(ctx, target, id, OperationStop); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("主机 %s 已停用，停 %s 失败：%v", host.Name, id, err))
			continue
		}
		result.Components = append(result.Components, ComponentResult{ID: id, Action: "disabled"})
		options.logf("%s %s: disabled", host.Name, id)
	}
	return result, nil
}

// waitComponents 等这次装的、没被停用的组件就绪，把结果写回 result；到时限还有没就绪的返回错误。
func waitComponents(ctx context.Context, target setupssh.Client, catalog servicecatalog.Catalog, host string, result *Result, timeout time.Duration) error {
	ids := []string{}
	for _, component := range result.Components {
		if component.Action != "disabled" {
			ids = append(ids, component.ID)
		}
	}
	pending, err := WaitReady(ctx, target, catalog, ids, timeout)
	if err != nil {
		return err
	}
	failed := []string{}
	for i := range result.Components {
		component := &result.Components[i]
		if detail, waiting := pending[component.ID]; waiting {
			component.Detail = detail
			failed = append(failed, component.ID+"（"+detail+"）")
		} else {
			component.Ready = component.Action != "disabled"
		}
	}
	if len(failed) != 0 {
		return fmt.Errorf("not_ready: 主机 %s 上这些组件到时限还没就绪：%s。看日志：moox-cli setup logs --host %s --component <组件>", host, strings.Join(failed, "、"), host)
	}
	return nil
}

// WaitReady 经 SSH 在目标机上等一组组件就绪，返回到时限还没就绪的组件与原因。
func WaitReady(ctx context.Context, target setupssh.Client, catalog servicecatalog.Catalog, ids []string, timeout time.Duration) (map[string]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if timeout <= 0 {
		timeout = DefaultReadyTimeout
	}
	type check struct {
		ID   string `json:"id"`
		Port int    `json:"port"`
		Unit string `json:"unit"`
	}
	checks := []check{}
	for _, id := range ids {
		component, ok := catalog.Component(id)
		if !ok {
			return nil, fmt.Errorf("组件 %s 不在服务目录里", id)
		}
		checks = append(checks, check{ID: id, Port: component.Health.Port, Unit: UnitName(id)})
	}
	request, err := json.Marshal(map[string]any{"checks": checks, "timeout": timeout.Seconds()})
	if err != nil {
		return nil, err
	}
	output, err := target.Run(ctx, []string{"python3", "-c", readyScript, string(request)}, nil)
	if err != nil {
		return nil, fmt.Errorf("ready_check_failed: 目标机上的就绪检查没有跑完：%s", lastLine(output.Stderr))
	}
	var answer struct {
		Pending []struct {
			ID     string `json:"id"`
			Detail string `json:"detail"`
		} `json:"pending"`
	}
	if err := json.Unmarshal([]byte(output.Stdout), &answer); err != nil {
		return nil, errors.New("ready_check_failed: 目标机上的就绪检查结果无法解析")
	}
	pending := map[string]string{}
	for _, item := range answer.Pending {
		pending[item.ID] = item.Detail
	}
	return pending, nil
}

// checkPlatform 确认目标机是 Linux；按清单下载组件包时发布只有 amd64。
func checkPlatform(ctx context.Context, target setupssh.Client, downloads bool) error {
	output, err := target.Run(ctx, []string{"uname", "-sm"}, nil)
	if err != nil {
		return errors.New("target_platform_unknown: 查不到目标机的系统与架构")
	}
	fields := strings.Fields(output.Stdout)
	if len(fields) != 2 || fields[0] != "Linux" {
		return errors.New("target_platform_unsupported: 目标机要是 Linux")
	}
	if downloads && fields[1] != "x86_64" {
		return fmt.Errorf("target_arch_unsupported: 发布只有 linux/amd64，目标机是 %s；用 --binary-dir 指定自己编译的二进制", fields[1])
	}
	return nil
}

func (o Options) readSource(name string) ([]byte, error) {
	if !fs.ValidPath(name) {
		return nil, fmt.Errorf("模板路径 %q 不合法", name)
	}
	return os.ReadFile(filepath.Join(o.RepositoryRoot, filepath.FromSlash(name)))
}

// binaries 算出每个组件 bin/ 下文件的摘要：源码目录部署读本机文件，产物包目录部署取清单。
func (o Options) binaries(catalog servicecatalog.Catalog, selected []string) (map[string]map[string]string, error) {
	result := map[string]map[string]string{}
	for _, id := range selected {
		component, _ := catalog.Component(id)
		if o.BinaryDirectory == "" {
			if o.Bundle == nil {
				return nil, errors.New("binary_dir_required: 在源码目录里部署要用 --binary-dir 指定预先编译好的 Linux 二进制目录（scripts/build/build-linux-local.sh 的输出）")
			}
			artifact, ok := o.Bundle.Component(id)
			if !ok {
				return nil, fmt.Errorf("bundle_incomplete: 产物包 %s 没有组件 %s 的包", o.Bundle.Release, id)
			}
			result[id] = artifact.Binaries
			continue
		}
		result[id] = map[string]string{}
		for _, name := range bundle.Binaries(component) {
			digest, err := linuxBinaryDigest(filepath.Join(o.BinaryDirectory, name))
			if err != nil {
				return nil, fmt.Errorf("binary_missing: 组件 %s 的 %s：%w", id, name, err)
			}
			result[id][name] = digest
		}
	}
	return result, nil
}

// linuxBinaryDigest 读一个本机编译好的 Linux 二进制的摘要；不是 ELF 文件直接报错，免得把本机系统的程序传上去。
func linuxBinaryDigest(name string) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", fmt.Errorf("读不到 %s", name)
	}
	defer file.Close()
	magic := make([]byte, 4)
	if _, err := io.ReadFull(file, magic); err != nil || !bytes.Equal(magic, []byte("\x7fELF")) {
		return "", fmt.Errorf("%s 不是 Linux 程序（先用 scripts/build/build-linux-local.sh 编译）", name)
	}
	hash := sha256.New()
	hash.Write(magic)
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// installBinaries 把和上次部署记录不一样的二进制放到目标机的 bin/。
func (o Options) installBinaries(ctx context.Context, target setupssh.Client, layout Layout, catalog servicecatalog.Catalog, selected []string, binaries map[string]map[string]string, existing *Version) error {
	changed := []string{}
	for _, id := range selected {
		if existing == nil || !mapsEqual(existing.Components[id].Binaries, binaries[id]) {
			changed = append(changed, id)
		}
	}
	if len(changed) == 0 {
		return nil
	}
	if o.BinaryDirectory != "" {
		local := map[string]string{}
		for _, id := range changed {
			for name := range binaries[id] {
				local["bin/"+name] = filepath.Join(o.BinaryDirectory, name)
			}
		}
		o.logf("%s: 上传 %d 个组件的二进制", o.HostID, len(changed))
		if output, err := runInstall(ctx, target, layout, "binaries", nil, local); err != nil {
			return fmt.Errorf("install_failed: 主机 %s 放二进制失败：%s", o.HostID, lastLine(output.Stderr))
		}
		return nil
	}
	files, err := fetchunit.ComponentFiles(*o.Bundle, changed)
	if err != nil {
		return err
	}
	o.logf("%s: 下载 %d 个组件包", o.HostID, len(files))
	urls := fetchunit.Sources(o.Bundle.Release)
	err = fetchunit.Run(ctx, target, fetchunit.Request{StagingDir: layout.Staging(), Release: o.Bundle.Release, Files: files, URLs: urls})
	if errors.Is(err, fetchunit.ErrNoSource) {
		// 目标机连不上下载源：由这台电脑下载再推送。
		o.logf("%s: 目标机下载失败（%v），改由这台电脑下载后推送", o.HostID, err)
		cache, cacheErr := os.UserCacheDir()
		if cacheErr != nil {
			return cacheErr
		}
		for _, file := range files {
			local, localErr := fetchunit.Local(ctx, urls, file, filepath.Join(cache, "moox", "downloads"))
			if localErr != nil {
				return localErr
			}
			if err = uploadFile(ctx, target, local, fetchunit.StagingPath(layout.Staging(), file.SHA256, file.Name)); err != nil {
				return err
			}
		}
	}
	if err != nil {
		return err
	}
	var list bytes.Buffer
	for _, file := range files {
		fmt.Fprintln(&list, fetchunit.StagingPath(layout.Staging(), file.SHA256, file.Name))
	}
	if output, err := runInstall(ctx, target, layout, "binaries", map[string]File{"packages.list": {Data: list.Bytes(), Mode: 0o600}}, nil); err != nil {
		return fmt.Errorf("install_failed: 主机 %s 放二进制失败：%s", o.HostID, lastLine(output.Stderr))
	}
	return nil
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func uploadFile(ctx context.Context, target setupssh.Client, local, remote string) error {
	file, err := os.Open(local)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if output, err := target.Run(ctx, []string{"mkdir", "-p", path.Dir(remote)}, nil); err != nil {
		return fmt.Errorf("目标机上建目录失败：%s", lastLine(output.Stderr))
	}
	return target.Upload(ctx, file, info.Size(), remote, 0o600)
}

// runInstall 把一组文件打成包传到目标机，再执行安装脚本的一个步骤。entries 是内存里的文件，localFiles 是本机文件（包内路径 → 本机路径）。
func runInstall(ctx context.Context, target setupssh.Client, layout Layout, mode string, entries map[string]File, localFiles map[string]string) (setupssh.Result, error) {
	temporary, err := os.CreateTemp("", "moox-deploy-*.tar.gz")
	if err != nil {
		return setupssh.Result{}, err
	}
	defer func() { _ = temporary.Close(); _ = os.Remove(temporary.Name()) }()
	compressed := gzip.NewWriter(temporary)
	archive := tar.NewWriter(compressed)
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		file := entries[name]
		if err := archive.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: int64(file.Mode.Perm()), Size: int64(len(file.Data)), Format: tar.FormatPAX}); err != nil {
			return setupssh.Result{}, err
		}
		if _, err := archive.Write(file.Data); err != nil {
			return setupssh.Result{}, err
		}
	}
	names = names[:0]
	for name := range localFiles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := addLocalFile(archive, name, localFiles[name]); err != nil {
			return setupssh.Result{}, err
		}
	}
	if err := errors.Join(archive.Close(), compressed.Close()); err != nil {
		return setupssh.Result{}, err
	}
	size, err := temporary.Seek(0, io.SeekCurrent)
	if err != nil {
		return setupssh.Result{}, err
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		return setupssh.Result{}, err
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return setupssh.Result{}, err
	}
	pack := path.Join(layout.Root, ".deploy-"+hex.EncodeToString(suffix)+".tar.gz")
	if err := target.Upload(ctx, temporary, size, pack, 0o600); err != nil {
		return setupssh.Result{}, err
	}
	return target.Run(ctx, []string{"bash", "-s", "--", mode, layout.Root, pack}, strings.NewReader(installScript))
}

func addLocalFile(archive *tar.Writer, name, local string) error {
	file, err := os.Open(local)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if err := archive.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o755, Size: info.Size(), Format: tar.FormatPAX}); err != nil {
		return err
	}
	_, err = io.Copy(archive, file)
	return err
}

// issuedKeys 记这次部署有没有新签发主机签名密钥。
type issuedKeys struct {
	hostKey bool
}

// identity 取这台主机的身份材料。控制机先执行 bootstrap（登记这台电脑的 moox-cli 公钥），其余主机先把登记同步到 Admin 再让控制机签发。
// 目标机上的签名私钥和 Admin 登记的公钥一致时不换密钥，身份包里只有重新签发的主机证书。
func (o Options) identity(ctx context.Context, target setupssh.Client, catalog servicecatalog.Catalog, host, control topology.Host, selected []string, fresh bool) (Identity, issuedKeys, error) {
	cfg, layout := o.Config, Layout{Root: o.Config.Paths.DeployRoot}
	var issued issuedKeys
	fail := func(err error) (Identity, issuedKeys, error) { return Identity{}, issued, err }
	roles, server := []string{}, slices.Contains(selected, "eventbus")
	for _, id := range selected {
		component, _ := catalog.Component(id)
		roles = append(roles, component.EventBusRoles()...)
	}
	sort.Strings(roles)
	roles = slices.Compact(roles)

	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return fail(err)
	}
	stage := path.Join(layout.Root, ".export-"+hex.EncodeToString(suffix))
	source := target
	if host.Name != control.Name {
		var err error
		if source, err = o.Dial(ctx, control); err != nil {
			return fail(err)
		}
		defer source.Close()
	}
	// 签发出来的身份材料用完就从控制机上删掉。
	defer func() { _, _ = source.Run(context.WithoutCancel(ctx), []string{"rm", "-rf", "--", stage}, nil) }()

	current := readCurrentKeys(ctx, target, layout, slices.Contains(cfg.Deployments[host.Name], "collector"))
	var metadata hostbundle.Metadata
	if host.Name == control.Name {
		if o.Secrets == nil {
			return fail(errors.New("部署控制机要读 moox.secrets.toml 里的初始管理员"))
		}
		if o.RotateHostKey {
			return fail(errors.New("rotate_unsupported: 控制机的签名密钥不单独更换：删掉控制机上的 " + layout.Secret(hostbundle.HostKeyFile) + " 再重新部署控制机，会签发新的"))
		}
		operator, err := identitybundle.ReadOperatorKey(o.OperatorDirectory)
		if err != nil {
			return fail(fmt.Errorf("operator_key_missing: 这台电脑没有 moox-cli 的签名私钥（%w）。先运行 moox-cli setup operator", err))
		}
		// 全新的控制机会生成新的 CA：这台电脑上记着的旧指纹与根证书对不上，先删掉。moox-cli 的私钥留着，bootstrap 登记的就是它。
		if fresh {
			if err := identitybundle.RemoveOperatorTrust(o.OperatorDirectory); err != nil {
				return fail(err)
			}
		}
		if metadata, err = bootstrapControl(ctx, source, cfg, o.Secrets.Admin, identitybundle.PublicKey(operator), current, stage); err != nil {
			return fail(err)
		}
		// bootstrap 不改已经登记的公钥：控制机上登记的是另一台电脑的，就要显式换过来。
		if metadata.OperatorPublicKey != identitybundle.PublicKey(operator) {
			return fail(errors.New("operator_key_mismatch: 控制机上登记的 moox-cli 公钥不是这台电脑的。换了操作电脑的话运行 moox-cli setup operator --register（原来那台电脑随即不能再用）"))
		}
	} else {
		pin, err := identitybundle.ReadOperatorPin(o.OperatorDirectory)
		if err != nil || pin.ControlHostID != control.Name {
			return fail(fmt.Errorf("control_not_ready: 这台电脑没有控制机 %s 的根证书指纹，先运行 moox-cli setup deploy --host %s", control.Name, control.Name))
		}
		if metadata, err = exportHostBundle(ctx, source, cfg, host.Name, current, o.RotateHostKey, stage); err != nil {
			return fail(err)
		}
	}
	var clients hostbundle.ClientMetadata
	if len(roles) != 0 {
		var err error
		if clients, err = exportEventBus(ctx, source, cfg, roles, server, stage); err != nil {
			return fail(err)
		}
	}
	ca := metadata.CA.SHA256
	if host.Name != control.Name {
		pin, _ := identitybundle.ReadOperatorPin(o.OperatorDirectory)
		ca = pin.CA
	}
	identity, err := fetchIdentity(ctx, source, metadata, materialOptions(cfg, host, ca), clients, roles)
	if err != nil {
		return fail(err)
	}
	if host.Name == control.Name {
		// 控制机的身份包经 SSH 取回并校验过：把它的根证书与指纹记到这台电脑，之后取别的主机的身份包都按它校验。
		if err := identitybundle.InstallOperatorTrust(o.OperatorDirectory, metadata, identity.Secrets[hostbundle.CAFile]); err != nil {
			return fail(fmt.Errorf("operator_trust_failed: 这台电脑没有记下控制机的根证书：%w。原因是身份目录的路径或权限时按上面的提示改（路径里有符号链接就用 --operator-dir 指到实际路径）；原因是根证书与已记下的不一致、线上是另一套部署清空后重装的，先删掉 %s 下的 %s 与 %s 再重跑",
				err, o.OperatorDirectory, identitybundle.OperatorPinFile, identitybundle.OperatorCAFile))
		}
	}
	issued.hostKey = metadata.SignKeyIssued
	if metadata.SignKeyIssued {
		o.logf("%s: 签发了新的主机签名密钥", host.Name)
	}
	return identity, issued, nil
}

// lastLine 取远端标准错误的最后一行作为原因（脚本与 moox-admin-cli 的报错不含秘密），截到 300 字节。
func lastLine(stderr string) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	line := strings.TrimSpace(lines[len(lines)-1])
	if len(line) > 300 {
		line = line[:300]
	}
	if line == "" {
		return "没有输出"
	}
	return line
}

// redeployWholeHost 报告只部署个别组件的这一次要不要改成整台部署：已经装过的主机上发现私钥丢了或对不上、签发了新的，
// 已装的其余组件也要换上新私钥，所以从头按整台再做一遍（新私钥还没落到目标机，那一遍会再签发一次）。
// 全新主机没有已装的组件，签发私钥是必然的，按选中的组件装即可（首次安装先只给控制机装 admin 与网关）。
func redeployWholeHost(issuedHostKey, full, installed bool) bool {
	return issuedHostKey && !full && installed
}
