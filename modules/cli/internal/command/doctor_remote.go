package command

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"sync"

	doctorcli "github.com/mooyang-code/moox/modules/cli/internal/doctor"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// 诊断外部程序（systemd 用户服务）与只能出站的主机需要两样东西：在节点上执行命令（本机直接执行，其他主机经 SSH），
// 以及经 control 中转的拨号（只能出站的主机）。两者都由命令入口按 moox.toml 与操作员网关装配。

type relayDialer interface {
	DialRelay(ctx context.Context, hostID string, port int) (net.Conn, error)
}

// doctorRemote 是诊断用的远程能力；close 释放 SSH 连接。
type doctorRemote struct {
	manifest    setupconfig.Manifest
	localNodeID string
	dial        func(context.Context, setupconfig.Host) (setupssh.Client, error)
	runLocal    func(ctx context.Context, argv []string) (string, error)
	relay       relayDialer

	mu      sync.Mutex
	clients map[string]setupssh.Client
}

func newDoctorRemote(manifest setupconfig.Manifest, localNodeID string, gateway any) *doctorRemote {
	remote := &doctorRemote{
		manifest: manifest, localNodeID: localNodeID, dial: dialSetupHost, runLocal: runLocalCommand,
		clients: map[string]setupssh.Client{},
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
func (r *doctorRemote) Run(ctx context.Context, nodeID string, argv []string) (string, error) {
	if nodeID == r.localNodeID {
		return r.runLocal(ctx, argv)
	}
	host := r.manifest.HostByID(nodeID)
	if host.Name == "" {
		return "", fmt.Errorf("moox.toml 里没有节点 %s，无法远程检查", nodeID)
	}
	r.mu.Lock()
	client := r.clients[nodeID]
	if client == nil {
		var err error
		if client, err = r.dial(ctx, host); err != nil {
			r.mu.Unlock()
			return "", err
		}
		r.clients[nodeID] = client
	}
	r.mu.Unlock()
	result, err := client.Run(ctx, argv, nil)
	return result.Stdout, err
}

func (r *doctorRemote) outbound(nodeID string) bool {
	reachability, _ := servicecatalog.ParseReachability(r.manifest.HostByID(nodeID).Reachability)
	return reachability == servicecatalog.ReachabilityOutbound
}

// Systemd 返回读取 systemd 用户服务状态的检查器。
func (r *doctorRemote) Systemd() doctorcli.SystemdChecker {
	return doctorcli.NewSystemdChecker(r.Run)
}

// Relay 返回出站主机的中转健康探测器；操作员网关不支持中转时为 nil。
func (r *doctorRemote) Relay(auth doctorcli.HealthAuth) *doctorcli.RelayProber {
	if r.relay == nil {
		return nil
	}
	return &doctorcli.RelayProber{Auth: auth, Outbound: r.outbound, Dial: r.relay.DialRelay}
}

func (r *doctorRemote) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, client := range r.clients {
		_ = client.Close()
		delete(r.clients, id)
	}
}

// loadDoctorRemote 读取 moox.toml 装配远程能力；读不到配置时返回 nil（诊断退回只用 Monitor 事实与本机探测）。
func loadDoctorRemote(file string, localNodeID string, gateway any) *doctorRemote {
	absolute, err := filepath.Abs(defaultFlag(file, "./moox.toml"))
	if err != nil {
		return nil
	}
	snapshot, err := setupconfig.Load(absolute, filepath.Dir(absolute))
	if err != nil {
		return nil
	}
	defer clearSetupSecrets(snapshot)
	return newDoctorRemote(snapshot.Manifest, localNodeID, gateway)
}
