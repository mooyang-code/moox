// Package model 定义 SSH 服务使用的数据结构。主机本身记在 t_hosts，SSH 凭据记在 t_secrets，这里只有一次连接需要的合成视图。
package model

import (
	"net"
	"strconv"

	secretmodel "github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
)

// Target 是一次 SSH 连接需要的全部信息，由主机登记（t_hosts）和 SSH 凭据（t_secrets）合成。
type Target struct {
	HostID     string
	Address    string
	Port       int
	User       string
	Credential secretmodel.SSHCredential
}

// Endpoint 返回“地址:端口”，主机密钥指纹按它记录：地址或端口变了，旧指纹自然失效。
func (t Target) Endpoint() string {
	return net.JoinHostPort(t.Address, strconv.Itoa(t.Port))
}
