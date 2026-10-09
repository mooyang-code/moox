package servicecatalog

import (
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
)

const (
	// AccessComponentID 是外部接入的组件 ID。
	AccessComponentID = "access"
	// AccessPort 是外部接入的入站端口（tRPC 明文 + 签名），每台主机统一使用。
	AccessPort = 11004
	// AccessFallbackHostID 是 SCF 所在地域没有外部接入时使用的外部接入所在主机，走它的公网地址。
	AccessFallbackHostID = "storage"
)

// AccessEndpoint 是外部调用方连接的外部接入：地址（host:port）和实例 ID（access@<主机>）。
type AccessEndpoint struct {
	Address string
	ID      string
	// HostID 是外部接入所在的主机。
	HostID string
	// Private 表示地址是该主机的私网地址，函数需要绑定该主机所在的 VPC 才能访问。
	Private bool
}

// AccessEndpointForRegion 为某个地域的 SCF 函数选择外部接入（设计文档 3.7）：
//   - 同地域的主机上有外部接入、且主机登记了私网地址时，用它的私网地址；函数需要事先绑定该主机所在的 VPC；
//   - 否则用 fallbackHost 上外部接入的公网地址。
//
// 同地域有多台时按主机 ID 取第一台，保证结果稳定。
func (d Directory) AccessEndpointForRegion(region, fallbackHost string) (AccessEndpoint, error) {
	region = strings.ToLower(strings.TrimSpace(region))
	hosts := d.ComponentHostIDs(AccessComponentID)
	if region != "" {
		for _, hostID := range hosts {
			host, ok := d.Host(hostID)
			if ok && host.PrivateAddress != "" && strings.ToLower(strings.TrimSpace(host.Region)) == region {
				return newAccessEndpoint(hostID, host.PrivateAddress, true), nil
			}
		}
	}
	fallbackHost = strings.TrimSpace(fallbackHost)
	if fallbackHost == "" {
		return AccessEndpoint{}, fmt.Errorf("地域 %q 没有外部接入，且没有指定兜底主机", region)
	}
	if !slices.Contains(hosts, fallbackHost) {
		return AccessEndpoint{}, fmt.Errorf("地域 %q 没有外部接入，兜底主机 %s 上也没有启用的外部接入", region, fallbackHost)
	}
	host, ok := d.Host(fallbackHost)
	if !ok || host.Address == "" {
		return AccessEndpoint{}, fmt.Errorf("服务目录中没有兜底主机 %s 的公网地址", fallbackHost)
	}
	return newAccessEndpoint(fallbackHost, host.Address, false), nil
}

func newAccessEndpoint(hostID, address string, private bool) AccessEndpoint {
	return AccessEndpoint{
		Address: net.JoinHostPort(address, strconv.Itoa(AccessPort)), ID: AccessComponentID + "@" + hostID,
		HostID: hostID, Private: private,
	}
}
