package configinit

import (
	"fmt"

	"github.com/BurntSushi/toml"
)

// 服务器角色。录入页按角色收集服务器，不让用户逐个挑组件；角色到组件的映射取自示例模板的放置段。
const (
	RoleStorage  = "storage"
	RoleOverseas = "overseas"
	RoleControl  = "control"
	RoleFactor   = "factor"
)

// Role 是页面上一类服务器的说明与默认值。Groups 是给用户看的中文功能分组，组件 ID 不展示。
type Role struct {
	Key      string      `json:"key"`
	Title    string      `json:"title"`
	Purpose  string      `json:"purpose"`
	Specs    []string    `json:"specs"`
	Groups   []RoleGroup `json:"groups"`
	Optional bool        `json:"optional"`
	// CanShareWith 列出可以共用同一台服务器的角色（只有管理台主控可以和别的角色混部）。
	CanShareWith []string `json:"can_share_with,omitempty"`
	// AddressHint 是地址输入框的提示（因子执行服务器可以填局域网 IP）。
	AddressHint string   `json:"address_hint"`
	Defaults    Defaults `json:"defaults"`
	// exampleHost 是示例模板里对应的主机 ID，组件放置从它读取；为空表示该角色不放置 MooX 组件。
	exampleHost string
	components  []string
}

// RoleGroup 是一组功能的中文名称和一句话说明。
type RoleGroup struct {
	Name string `json:"name"`
	Desc string `json:"desc"`
}

// Defaults 是该角色主机的预填值（不含地址和密码）。
type Defaults struct {
	ID       string `json:"id"`
	Region   string `json:"region"`
	Provider string `json:"provider"`
	TLSMode  string `json:"tls_mode"`
	SSHPort  int    `json:"ssh_port"`
	SSHUser  string `json:"ssh_user"`
}

// roleCatalog 按页面展示顺序列出四类服务器。推荐规格参考线上现用的机器（2026-10 实测）。
var roleCatalog = []Role{
	{
		Key: RoleStorage, Title: "数据存储服务器", exampleHost: "storage",
		Purpose: "保存一段时间的在线行情、因子和策略数据，并对外提供数据读写。对内存、磁盘和带宽要求最高。",
		Specs:   []string{"4 核 CPU", "16GB 内存", "100GB 以上 SSD 云盘", "公网带宽 5Mbps 以上", "线上现用：4 核 16GB / 100GB"},
		Groups: []RoleGroup{
			{"时序数据存储", "行情、因子、策略数据的写入与保存"},
			{"数据视图", "给管理台和策略提供快速查询"},
			{"外部接入", "采集云函数、因子引擎经此读写数据"},
		},
		AddressHint: "公网 IP 或域名",
	},
	{
		Key: RoleOverseas, Title: "海外服务器", exampleHost: "compute-1",
		Purpose: "海外交易所的数据接入与策略下单执行，建议选择香港地域。",
		Specs:   []string{"2 核 CPU", "2GB 内存起", "50GB 云盘", "带宽按流量计费即可", "线上现用：2 核 2GB / 50GB（香港）"},
		Groups: []RoleGroup{
			{"交易执行", "把策略信号下单到交易所"},
			{"出口代理", "采集云函数经此访问海外交易所"},
			{"外部接入", "海外链路的签名入口"},
		},
		AddressHint: "公网 IP 或域名",
	},
	{
		Key: RoleControl, Title: "管理台主控服务器", exampleHost: "control",
		Purpose:      "部署管理台和调度类服务，资源要求不高。可以和上面的服务器共用一台，建议独立部署。",
		Specs:        []string{"4 核 CPU", "4GB 内存", "40GB 以上云盘", "线上现用：4 核 4GB / 40GB"},
		CanShareWith: []string{RoleStorage, RoleOverseas},
		Groups: []RoleGroup{
			{"管理台", "网页、管理后台与 HTTPS 入口"},
			{"消息与监控", "服务之间的消息总线、监控告警"},
			{"行情采集调度", "调度云函数定时采集行情"},
			{"因子与策略", "因子管理、策略运行"},
		},
		AddressHint: "公网 IP 或域名（管理台网址用它）",
	},
	{
		Key: RoleFactor, Title: "因子执行服务器", Optional: true,
		Purpose: "运行因子引擎做因子计算，需要较高性能。可以填局域网 IP，建议用家里的机器；它同时用作 Linux 编译机（从源码编译存储服务时需要）。",
		Specs:   []string{"Linux（x86_64）", "8 核以上 CPU，越多越好", "16GB 以上内存", "线上现用：88 核家用工作站"},
		Groups: []RoleGroup{
			{"因子计算", "按周期计算因子并写回存储"},
			{"源码编译", "编译需要 CGO 的存储服务"},
		},
		AddressHint: "局域网 IP 也可以，例如 192.168.0.103",
		Defaults:    Defaults{ID: "factor", SSHPort: 22},
	},
}

// loadRoles 从示例模板读出各角色的组件放置与主机默认值，避免在代码里再抄一份。
func loadRoles() ([]Role, error) {
	var example struct {
		Hosts map[string]struct {
			Region   string `toml:"region"`
			Provider string `toml:"provider"`
			TLSMode  string `toml:"tls_mode"`
			SSH      struct {
				Port     int    `toml:"port"`
				Username string `toml:"username"`
			} `toml:"ssh"`
		} `toml:"hosts"`
		Placements map[string][]string `toml:"placements"`
	}
	if _, err := toml.Decode(exampleTemplate, &example); err != nil {
		return nil, err
	}
	roles := make([]Role, len(roleCatalog))
	for i, role := range roleCatalog {
		if role.exampleHost != "" {
			host, ok := example.Hosts[role.exampleHost]
			placed, placedOK := example.Placements[role.exampleHost]
			if !ok || !placedOK {
				return nil, fmt.Errorf("示例配置里没有主机 %s 或它的放置，请同步模板", role.exampleHost)
			}
			port := host.SSH.Port
			if port == 0 {
				port = 22
			}
			role.Defaults = Defaults{ID: role.exampleHost, Region: host.Region, Provider: host.Provider, TLSMode: host.TLSMode, SSHPort: port, SSHUser: host.SSH.Username}
			role.components = append([]string(nil), placed...)
		}
		roles[i] = role
	}
	return roles, nil
}

func roleByKey(roles []Role, key string) (Role, bool) {
	for _, role := range roles {
		if role.Key == key {
			return role, true
		}
	}
	return Role{}, false
}
