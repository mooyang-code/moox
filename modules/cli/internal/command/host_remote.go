package command

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os/exec"
	"path/filepath"
	"sync"

	doctorcli "github.com/mooyang-code/moox/modules/cli/internal/doctor"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
)

// hostRemote 是命令行访问各台主机的统一连接层，按 moox.toml 装配：
//   - 在主机上执行命令、上传下载文件（经 SSH，复用同一台主机的连接）；
//   - 诊断用：读取 systemd 用户服务状态。

// hostRemote 是诊断用的远程能力；close 释放 SSH 连接。
type hostRemote struct {
	manifest    setupconfig.Manifest
	localNodeID string
	dial        func(context.Context, setupconfig.Host) (setupssh.Client, error)
	runLocal    func(ctx context.Context, argv []string) (string, error)

	mu      sync.Mutex
	clients map[string]setupssh.Client
}

func newHostRemote(manifest setupconfig.Manifest, localNodeID string) *hostRemote {
	return &hostRemote{
		manifest: manifest, localNodeID: localNodeID, dial: dialSetupHost, runLocal: runLocalCommand,
		clients: map[string]setupssh.Client{},
	}
}

func runLocalCommand(ctx context.Context, argv []string) (string, error) {
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
	if err != nil {
		if _, exit := err.(*exec.ExitError); exit {
			return string(out), nil
		}
		return "", err
	}
	return string(out), nil
}

// Run 在节点上执行命令：本节点直接执行，其他主机复用到该主机的 SSH 连接。
func (r *hostRemote) Run(ctx context.Context, nodeID string, argv []string) (string, error) {
	if nodeID == r.localNodeID {
		return r.runLocal(ctx, argv)
	}
	result, err := r.Exec(ctx, nodeID, argv, nil)
	return result.Stdout, err
}

// client 返回到主机的 SSH 连接；同一台主机只建立一次。
func (r *hostRemote) client(ctx context.Context, hostID string) (setupssh.Client, error) {
	host := r.manifest.HostByID(hostID)
	if host.Name == "" {
		return nil, fmt.Errorf("moox.toml 里没有节点 %s，无法远程检查", hostID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if client := r.clients[hostID]; client != nil {
		return client, nil
	}
	client, err := r.dial(ctx, host)
	if err != nil {
		return nil, err
	}
	r.clients[hostID] = client
	return client, nil
}

// Exec 经 SSH 在主机上执行命令（不区分是否本机）；退出码非 0 时返回错误，结果里仍有输出与退出码。
func (r *hostRemote) Exec(ctx context.Context, hostID string, argv []string, stdin io.Reader) (setupssh.Result, error) {
	client, err := r.client(ctx, hostID)
	if err != nil {
		return setupssh.Result{}, err
	}
	return client.Run(ctx, argv, stdin)
}

// Upload 把本地文件传到主机；目标先写临时文件再原子替换。
func (r *hostRemote) Upload(ctx context.Context, hostID string, src io.Reader, size int64, dst string, mode fs.FileMode) error {
	client, err := r.client(ctx, hostID)
	if err != nil {
		return err
	}
	return client.Upload(ctx, src, size, dst, mode)
}

// Download 把主机上的文件读到 dst。
func (r *hostRemote) Download(ctx context.Context, hostID, src string, dst io.Writer) (int64, error) {
	client, err := r.client(ctx, hostID)
	if err != nil {
		return 0, err
	}
	return client.Download(ctx, src, dst)
}

// Systemd 返回读取 systemd 用户服务状态的检查器。
func (r *hostRemote) Systemd() doctorcli.SystemdChecker {
	return doctorcli.NewSystemdChecker(r.Run)
}

func (r *hostRemote) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, definition := range r.manifest.HostCatalog {
		definition.SSH.Password = ""
		r.manifest.HostCatalog[id] = definition
	}
	for id, client := range r.clients {
		_ = client.Close()
		delete(r.clients, id)
	}
}

// openHostRemote 读取 moox.toml 装配连接层；读不到配置时返回错误。
func openHostRemote(file string, localNodeID string) (*hostRemote, error) {
	absolute, err := filepath.Abs(defaultFlag(file, "./moox.toml"))
	if err != nil {
		return nil, err
	}
	snapshot, err := setupconfig.Load(absolute, filepath.Dir(absolute))
	if err != nil {
		return nil, err
	}
	return newHostRemoteFromSnapshot(snapshot, localNodeID), nil
}

// loadHostRemote 同 openHostRemote，读不到配置时返回 nil（诊断退回只用 Monitor 事实与本机探测）。
func loadHostRemote(file string, localNodeID string) *hostRemote {
	remote, err := openHostRemote(file, localNodeID)
	if err != nil {
		return nil
	}
	return remote
}

// newHostRemoteFromSnapshot 先复制主机目录（SSH 密码要留到建立连接时使用），再清掉快照里的其余凭据；
// 复制出来的密码在 Close 时清除。
func newHostRemoteFromSnapshot(snapshot *setupconfig.Snapshot, localNodeID string) *hostRemote {
	manifest := snapshot.Manifest
	catalog := maps.Clone(manifest.HostCatalog)
	clearSetupSecrets(snapshot)
	manifest.HostCatalog = catalog
	return newHostRemote(manifest, localNodeID)
}
