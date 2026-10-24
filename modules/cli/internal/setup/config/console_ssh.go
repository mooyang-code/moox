package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// 控制台终端登录用的凭据种类。
const (
	ConsoleSSHPassword   = "password"
	ConsoleSSHPrivateKey = "private_key"
)

// ConsoleSSH 是同步给控制台的主机 SSH 登录信息。
type ConsoleSSH struct {
	Port       int
	User       string
	AuthType   string
	Secret     string
	Passphrase string
}

// ConsoleSSH 返回要同步给控制台的 SSH 信息：只能出站的主机、没有配置密码或私钥的主机返回 nil（控制台里置灰）。
// 私钥优先于密码；私钥文件在本机读取，路径支持 ~ 开头。
func (h Host) ConsoleSSH() (*ConsoleSSH, error) {
	if reachability, _ := servicecatalog.ParseReachability(h.Reachability); reachability == servicecatalog.ReachabilityOutbound {
		return nil, nil
	}
	if h.PrivateKeyFile != "" {
		raw, err := os.ReadFile(expandHome(h.PrivateKeyFile))
		if err != nil {
			return nil, fmt.Errorf("主机 %s 的 ssh.private_key_file 读取失败: %w", h.Name, err)
		}
		if strings.TrimSpace(string(raw)) == "" {
			return nil, fmt.Errorf("主机 %s 的 ssh.private_key_file 是空文件", h.Name)
		}
		return &ConsoleSSH{Port: h.Port, User: h.Username, AuthType: ConsoleSSHPrivateKey, Secret: string(raw), Passphrase: h.PrivateKeyPassphrase}, nil
	}
	if h.Password != "" {
		return &ConsoleSSH{Port: h.Port, User: h.Username, AuthType: ConsoleSSHPassword, Secret: h.Password}, nil
	}
	return nil, nil
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}
