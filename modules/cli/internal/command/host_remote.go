package command

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os/exec"
	"strconv"
	"sync"
	"time"

	doctorcli "github.com/mooyang-code/moox/modules/cli/internal/doctor"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// hostRemote 是命令行访问各台主机的统一连接层，按 moox.toml 装配：
//   - 在主机上执行命令、上传下载文件（经 SSH，复用同一台主机的连接）；
//   - 诊断用：读取 systemd 用户服务状态、经 SSH 转发请求目标机上只听本机的健康端口。
//
// 只能出站的主机（因子机）只有在家里的局域网才 SSH 得上，连不上时错误里附带不在局域网时的查看办法。

// hostRemote 是诊断用的远程能力；close 释放 SSH 连接。
type hostRemote struct {
	manifest    *topology.Config
	localNodeID string
	dial        func(context.Context, topology.Host) (setupssh.Client, error)
	runLocal    func(ctx context.Context, argv []string) (string, error)

	mu      sync.Mutex
	clients map[string]setupssh.Client
}

func newHostRemote(manifest *topology.Config, localNodeID string) *hostRemote {
	return &hostRemote{
		manifest: manifest, localNodeID: localNodeID, dial: dialHost, runLocal: runLocalCommand,
		clients: map[string]setupssh.Client{},
	}
}

// dialHost 用 moox.toml 里这台主机的密钥登录方式连 SSH：配了 private_key_file 就用它，没配用 ssh-agent 与默认私钥。
func dialHost(ctx context.Context, host topology.Host) (setupssh.Client, error) {
	target := setupssh.Target{Name: host.Name, Address: host.Address, Port: host.Port, Username: host.Username}
	return setupssh.Dial(ctx, target, "", setupssh.Options{Timeout: 15 * time.Second, IdentityFiles: host.IdentityFiles()})
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
		if reachability, _ := servicecatalog.ParseReachability(host.Reachability); reachability == servicecatalog.ReachabilityOutbound {
			return nil, fmt.Errorf("%w；%s", err, outboundSSHHint(hostID))
		}
		return nil, err
	}
	r.clients[hostID] = client
	return client, nil
}

// outboundSSHHint 是只能出站的主机 SSH 连不上时的提示：它只接受家里局域网内的 SSH。
func outboundSSHHint(hostID string) string {
	return fmt.Sprintf("%s 只能出站，SSH 只有在家里的局域网才连得上，doctor 与 /readyz 检查只能在局域网里做；"+
		"不在局域网时看 Monitor 的上报状态（moox-cli doctor alerts）和 moox-cli host agent status --host %s", hostID, hostID)
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

// HealthProbe 返回请求组件健康端口的探测器：健康端口只听 127.0.0.1，本节点直接请求，
// 其他主机经 SSH 转发到目标机的 127.0.0.1。SSH 连不上时返回 doctorcli.ErrNodeUnreachable。
func (r *hostRemote) HealthProbe() doctorcli.HealthProbe {
	local := doctorcli.LocalHealthProbe(r.localNodeID)
	return func(ctx context.Context, nodeID string, port int, path string) (doctorcli.ProbeResult, error) {
		if nodeID == r.localNodeID {
			return local(ctx, nodeID, port, path)
		}
		client, err := r.client(ctx, nodeID)
		if err != nil {
			return doctorcli.ProbeResult{}, fmt.Errorf("%w %s: %w", doctorcli.ErrNodeUnreachable, nodeID, err)
		}
		forwardCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		listener, err := client.ForwardLocal(forwardCtx, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			return doctorcli.ProbeResult{}, fmt.Errorf("%w %s: %w", doctorcli.ErrNodeUnreachable, nodeID, err)
		}
		_, localPort, err := net.SplitHostPort(listener.Addr().String())
		if err != nil {
			return doctorcli.ProbeResult{}, err
		}
		forwarded, _ := strconv.Atoi(localPort)
		return doctorcli.HTTPProber{}.Get(ctx, doctorcli.LoopbackURL(forwarded, path))
	}
}

func (r *hostRemote) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, client := range r.clients {
		_ = client.Close()
		delete(r.clients, id)
	}
}

// openHostRemote 读取 moox.toml 装配连接层；读不到配置时返回错误。
func openHostRemote(file string, localNodeID string) (*hostRemote, error) {
	cfg, err := topology.Load(defaultFlag(file, defaultSetupFile))
	if err != nil {
		return nil, err
	}
	return newHostRemote(cfg, localNodeID), nil
}

// loadHostRemote 同 openHostRemote，读不到配置时返回 nil（诊断退回只用 Monitor 事实与本机探测）。
func loadHostRemote(file string, localNodeID string) *hostRemote {
	remote, err := openHostRemote(file, localNodeID)
	if err != nil {
		return nil
	}
	return remote
}
