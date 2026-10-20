package configinit

import (
	"fmt"

	"github.com/BurntSushi/toml"
)

// 服务器角色。录入页按用途收集服务器，不让用户挑组件；角色到组件的映射取自示例模板的放置段。
const (
	RoleStorage  = "storage"
	RoleControl  = "control"
	RoleOverseas = "overseas"
	RoleFactor   = "factor"
)

// Role 是页面上一类服务器的说明。只放给用户看的内容，主机 ID、组件等底层信息不下发到页面。
type Role struct {
	Key     string   `json:"key"`
	Title   string   `json:"title"`
	Purpose string   `json:"purpose"`
	Specs   []string `json:"specs"`
	// Recommend 用来和识别出的实例规格比较，低于建议时页面提醒（不拦截）。
	Recommend Recommend `json:"recommend"`
	Optional  bool      `json:"optional"`
	// Toggle 是可选角色的开关文案。
	Toggle *Toggle `json:"toggle,omitempty"`
	// Share 是可以和其他服务器共用时的选项（只有管理台服务器有）。
	Share []ShareOption `json:"share,omitempty"`
	// SSHUser 是 SSH 用户名的预填值。
	SSHUser string `json:"ssh_user"`

	exampleHost string
	hostID      string
	tlsMode     string
	components  []string
}

// Recommend 是建议配置；0 表示不比较。
type Recommend struct {
	CPU      int `json:"cpu"`
	MemoryGB int `json:"memory_gb"`
	DiskGB   int `json:"disk_gb"`
}

// Toggle 是可选角色的开关。
type Toggle struct {
	Label string `json:"label"`
	Hint  string `json:"hint"`
}

// ShareOption 是管理台服务器的部署方式。Value 为空表示单独一台。
type ShareOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Hint  string `json:"hint"`
}

// roleCatalog 按页面顺序列出四类服务器。建议配置参考线上现用的机器（2026-10 实测）。
var roleCatalog = []Role{
	{
		Key: RoleStorage, Title: "数据存储服务器", exampleHost: "storage",
		Purpose:   "保存行情、因子和策略数据，对内存和磁盘要求最高。",
		Specs:     []string{"4 核 · 16 GB · 100 GB SSD", "带宽 5 Mbps 以上"},
		Recommend: Recommend{CPU: 4, MemoryGB: 16, DiskGB: 100},
	},
	{
		Key: RoleControl, Title: "管理台服务器", exampleHost: "control",
		Purpose:   "运行管理台和调度服务，资源要求不高。",
		Specs:     []string{"4 核 · 4 GB · 40 GB"},
		Recommend: Recommend{CPU: 4, MemoryGB: 4, DiskGB: 40},
		Share: []ShareOption{
			{Value: "", Label: "单独一台服务器", Hint: "推荐，互不影响"},
			{Value: RoleStorage, Label: "和数据存储服务器共用", Hint: "省一台机器，存储压力大时管理台会变慢"},
			{Value: RoleOverseas, Label: "和海外服务器共用", Hint: "管理台在境外，国内打开可能较慢"},
		},
	},
	{
		Key: RoleOverseas, Title: "海外服务器", exampleHost: "overseas", Optional: true,
		Purpose:   "接入海外交易所的行情并执行下单，建议放在香港。",
		Specs:     []string{"2 核 · 2 GB · 50 GB", "带宽按流量计费"},
		Recommend: Recommend{CPU: 2, MemoryGB: 2, DiskGB: 50},
		Toggle:    &Toggle{Label: "我要使用加密货币行情", Hint: "加密货币需要海外服务器；只做 A 股、港股、美股可以不配置。"},
	},
	{
		Key: RoleFactor, Title: "因子计算机", Optional: true, hostID: "factor",
		Purpose: "计算因子，需要较高性能。可以是这台电脑，也可以是家里的另一台机器（Linux、macOS 或 Windows），不要求是腾讯云。",
		Specs:   []string{"8 核以上 · 16 GB 以上"},
		Toggle:  &Toggle{Label: "现在配置", Hint: "以后也可以再加。"},
	},
}

// loadRoles 从示例模板读出各角色的组件放置、主机 ID 与默认值，避免在代码里再抄一份。
func loadRoles() ([]Role, error) {
	var example struct {
		Hosts map[string]struct {
			TLSMode string `toml:"tls_mode"`
			SSH     struct {
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
			role.hostID = role.exampleHost
			role.tlsMode = host.TLSMode
			role.SSHUser = host.SSH.Username
			role.components = append([]string(nil), placed...)
		}
		roles[i] = role
	}
	return roles, nil
}

func roleByKey(roles []Role, key string) Role {
	for _, role := range roles {
		if role.Key == key {
			return role
		}
	}
	panic("unknown role " + key)
}
