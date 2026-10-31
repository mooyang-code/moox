package command

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	doctorcli "github.com/mooyang-code/moox/modules/cli/internal/doctor"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// hostRemote 是命令行访问各台主机的统一连接层，按 moox.toml 与操作员网关装配：
//   - 在主机上执行命令、上传下载文件（经 SSH，复用同一台主机的连接）；
//   - 把主机的本机端口映射到操作机的回环端口（直连主机经 SSH 转发，只能出站的主机经 control 中转），
//     供 lifecycle、system、replay、import 访问模块的健康端口，不需要手工建隧道；
//   - 诊断用：读取 systemd 用户服务状态、对出站主机做中转健康探测。

type relayDialer interface {
	DialRelay(ctx context.Context, hostID string, port int) (net.Conn, error)
}

// hostRemote 是诊断用的远程能力；close 释放 SSH 连接。
type hostRemote struct {
	manifest    setupconfig.Manifest
	localNodeID string
	dial        func(context.Context, setupconfig.Host) (setupssh.Client, error)
	runLocal    func(ctx context.Context, argv []string) (string, error)
	relay       relayDialer

	mu        sync.Mutex
	clients   map[string]setupssh.Client
	endpoints map[string]string // "主机:端口" → 本机回环地址
	listeners []net.Listener
}

func newHostRemote(manifest setupconfig.Manifest, localNodeID string, gateway any) *hostRemote {
	remote := &hostRemote{
		manifest: manifest, localNodeID: localNodeID, dial: dialSetupHost, runLocal: runLocalCommand,
		clients: map[string]setupssh.Client{}, endpoints: map[string]string{},
	}
	if relay, ok := gateway.(relayDialer); ok {
		remote.relay = relay
	}
	return remote
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

// LocalEndpoint 返回操作机回环地址上的一个端口（host:port 形式）：连接它等于连接主机 hostID 的本机端口 port。
// 只能出站的主机经 control 中转，其余主机经 SSH 端口转发；同一目标重复调用返回同一个地址。
func (r *hostRemote) LocalEndpoint(ctx context.Context, hostID string, port int) (string, error) {
	key := fmt.Sprintf("%s:%d", hostID, port)
	r.mu.Lock()
	if address := r.endpoints[key]; address != "" {
		r.mu.Unlock()
		return address, nil
	}
	r.mu.Unlock()
	var listener net.Listener
	if r.outbound(hostID) {
		if r.relay == nil {
			return "", fmt.Errorf("主机 %s 只能出站，需要经 control 中转，但当前网关不支持中转", hostID)
		}
		var err error
		if listener, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return "", err
		}
		go serveRelay(listener, r.relay, hostID, port)
	} else {
		client, err := r.client(ctx, hostID)
		if err != nil {
			return "", err
		}
		if listener, err = client.ForwardLocal(ctx, fmt.Sprintf("127.0.0.1:%d", port)); err != nil {
			return "", fmt.Errorf("到主机 %s 的端口 %d 转发失败: %w", hostID, port, err)
		}
	}
	address := listener.Addr().String()
	r.mu.Lock()
	r.endpoints[key] = address
	r.listeners = append(r.listeners, listener)
	r.mu.Unlock()
	return address, nil
}

// serveRelay 把本机回环端口上的每个连接经 control 中转到主机 hostID 的本机端口 port。
func serveRelay(listener net.Listener, relay relayDialer, hostID string, port int) {
	for {
		local, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer local.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			remote, err := relay.DialRelay(ctx, hostID, port)
			cancel()
			if err != nil {
				return
			}
			defer remote.Close()
			done := make(chan struct{}, 2)
			go func() { _, _ = io.Copy(remote, local); done <- struct{}{} }()
			go func() { _, _ = io.Copy(local, remote); done <- struct{}{} }()
			<-done
		}()
	}
}

func (r *hostRemote) outbound(nodeID string) bool {
	reachability, _ := servicecatalog.ParseReachability(r.manifest.HostByID(nodeID).Reachability)
	return reachability == servicecatalog.ReachabilityOutbound
}

// Systemd 返回读取 systemd 用户服务状态的检查器。
func (r *hostRemote) Systemd() doctorcli.SystemdChecker {
	return doctorcli.NewSystemdChecker(r.Run)
}

// Relay 返回出站主机的中转健康探测器；操作员网关不支持中转时为 nil。
func (r *hostRemote) Relay(auth doctorcli.HealthAuth) *doctorcli.RelayProber {
	if r.relay == nil {
		return nil
	}
	return &doctorcli.RelayProber{Auth: auth, Outbound: r.outbound, Dial: r.relay.DialRelay}
}

func (r *hostRemote) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, definition := range r.manifest.HostCatalog {
		definition.SSH.Password = ""
		r.manifest.HostCatalog[id] = definition
	}
	for _, listener := range r.listeners {
		_ = listener.Close()
	}
	r.listeners = nil
	r.endpoints = map[string]string{}
	for id, client := range r.clients {
		_ = client.Close()
		delete(r.clients, id)
	}
}

// openHostRemote 读取 moox.toml 装配连接层；读不到配置时返回错误。
func openHostRemote(file string, localNodeID string, gateway any) (*hostRemote, error) {
	absolute, err := filepath.Abs(defaultFlag(file, "./moox.toml"))
	if err != nil {
		return nil, err
	}
	snapshot, err := setupconfig.Load(absolute, filepath.Dir(absolute))
	if err != nil {
		return nil, err
	}
	return newHostRemoteFromSnapshot(snapshot, localNodeID, gateway), nil
}

// loadHostRemote 同 openHostRemote，读不到配置时返回 nil（诊断退回只用 Monitor 事实与本机探测）。
func loadHostRemote(file string, localNodeID string, gateway any) *hostRemote {
	remote, err := openHostRemote(file, localNodeID, gateway)
	if err != nil {
		return nil
	}
	return remote
}

// newHostRemoteFromSnapshot 先复制主机目录（SSH 密码要留到建立连接时使用），再清掉快照里的其余凭据；
// 复制出来的密码在 Close 时清除。
func newHostRemoteFromSnapshot(snapshot *setupconfig.Snapshot, localNodeID string, gateway any) *hostRemote {
	manifest := snapshot.Manifest
	catalog := maps.Clone(manifest.HostCatalog)
	clearSetupSecrets(snapshot)
	manifest.HostCatalog = catalog
	return newHostRemote(manifest, localNodeID, gateway)
}
