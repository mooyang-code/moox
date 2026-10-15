package configinit

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
)

// ConfigForm 是配置页「修改配置」里可以改的项：只放常用设置，用中文表单呈现，不暴露配置文件原文。
// 其余高级设置（因子、云函数计划、出口代理、路径等）保存时原样保留。
type ConfigForm struct {
	Admin struct {
		Username string `json:"username"`
		Password string `json:"password"`
	} `json:"admin"`
	Tencent struct {
		SecretID  string `json:"secret_id"`
		SecretKey string `json:"secret_key"`
	} `json:"tencent"`
	Notification struct {
		ChannelType string `json:"channel_type"`
		WebhookURL  string `json:"webhook_url"`
	} `json:"notification"`
	Hosts     []FormHost    `json:"hosts"`
	Retention FormRetention `json:"retention"`
	Logs      struct {
		MaxSizeMB   int `json:"max_size_mb"`
		BackupCount int `json:"backup_count"`
	} `json:"logs"`
}

// FormHost 是一台主机的 SSH 登录方式；用途、地址、地域只读（改这些需要重新部署）。
type FormHost struct {
	ID          string   `json:"id"`
	Uses        []string `json:"uses,omitempty"`
	Address     string   `json:"address,omitempty"`
	Region      string   `json:"region,omitempty"`
	SSHUser     string   `json:"ssh_user"`
	SSHPort     int      `json:"ssh_port"`
	SSHPassword string   `json:"ssh_password"`
}

// FormRetention 是数据保留期。取值为 <n>h、<n>d 或 forever；空间里留空表示跟随默认。
type FormRetention struct {
	ViewBars    uint64            `json:"view_bars"`
	Frequencies []string          `json:"frequencies,omitempty"`
	Defaults    map[string]string `json:"defaults"`
	Spaces      []FormSpace       `json:"spaces"`
}

// FormSpace 是某个市场单独设置的保留期。
type FormSpace struct {
	ID     string            `json:"id"`
	Name   string            `json:"name,omitempty"`
	Values map[string]string `json:"values"`
}

// retentionFrequencies 是保留期表格的行顺序（与示例模板一致）。
var retentionFrequencies = []string{"30s", "1m", "5m", "15m", "30m", "1h", "4h", "1d", "1w", "1mo"}

var spaceNames = map[string]string{"crypto": "加密货币", "stockcn": "A 股", "stockhk": "港股", "stockus": "美股", "mooxsys": "系统数据"}

// formFrom 从当前配置读出表单的值。
func formFrom(m setupconfig.Manifest) ConfigForm {
	var form ConfigForm
	form.Admin.Username, form.Admin.Password = m.Admin.Username, m.Admin.Password
	form.Tencent.SecretID, form.Tencent.SecretKey = m.TencentCloud.SecretID, m.TencentCloud.SecretKey
	form.Notification.ChannelType, form.Notification.WebhookURL = m.Notification.ChannelType, m.Notification.WebhookURL
	view := describe(m, false, "")
	for _, server := range view.Servers {
		host := m.HostCatalog[server.ID]
		form.Hosts = append(form.Hosts, FormHost{
			ID: server.ID, Uses: server.Uses, Address: host.Address, Region: server.Region,
			SSHUser: host.SSH.Username, SSHPort: host.SSH.Port, SSHPassword: host.SSH.Password,
		})
	}
	form.Retention.ViewBars = m.StorageRetention.ViewBars
	form.Retention.Frequencies = retentionFrequencies
	form.Retention.Defaults = copyMap(m.StorageRetention.Defaults)
	ids := make([]string, 0, len(m.StorageRetention.Spaces))
	for id := range m.StorageRetention.Spaces {
		ids = append(ids, id)
	}
	// 先按常见市场的顺序，其余按名称。
	order := map[string]int{"crypto": 0, "stockcn": 1, "stockhk": 2, "stockus": 3, "mooxsys": 4}
	sort.Slice(ids, func(i, j int) bool {
		oi, iok := order[ids[i]]
		oj, jok := order[ids[j]]
		if iok != jok {
			return iok
		}
		if iok {
			return oi < oj
		}
		return ids[i] < ids[j]
	})
	for _, id := range ids {
		form.Retention.Spaces = append(form.Retention.Spaces, FormSpace{ID: id, Name: firstNonEmpty(spaceNames[id], id), Values: copyMap(m.StorageRetention.Spaces[id])})
	}
	form.Logs.MaxSizeMB, form.Logs.BackupCount = m.LocalLogs.MaxSizeMB, m.LocalLogs.BackupCount
	return form
}

func copyMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// applyForm 把表单的值逐项写回配置文本，返回新的配置；结果由调用方再做严格校验。
// 只认当前配置里已有的主机与空间，表单里多出来的一律拒绝。
func applyForm(plain []byte, current setupconfig.Manifest, form ConfigForm) ([]byte, error) {
	for _, field := range []struct{ name, value string }{
		{"管理员用户名", form.Admin.Username}, {"管理员密码", form.Admin.Password},
		{"腾讯云 SecretId", form.Tencent.SecretID}, {"腾讯云 SecretKey", form.Tencent.SecretKey},
		{"告警渠道", form.Notification.ChannelType}, {"告警 Webhook 地址", form.Notification.WebhookURL},
	} {
		if err := checkText(field.name, field.value, false); err != nil {
			return nil, err
		}
	}
	lines := strings.Split(string(plain), "\n")
	set := func(section, key, value string) { lines = putKey(lines, section, key, value) }
	set("admin", "username", tomlString(strings.TrimSpace(form.Admin.Username)))
	set("admin", "password", tomlString(form.Admin.Password))
	set("tencent_cloud", "secret_id", tomlString(strings.TrimSpace(form.Tencent.SecretID)))
	set("tencent_cloud", "secret_key", tomlString(form.Tencent.SecretKey))
	set("notification", "channel_type", tomlString(strings.TrimSpace(form.Notification.ChannelType)))
	set("notification", "webhook_url", tomlString(strings.TrimSpace(form.Notification.WebhookURL)))

	for _, host := range form.Hosts {
		if _, ok := current.HostCatalog[host.ID]; !ok {
			return nil, fmt.Errorf("配置里没有主机 %q", host.ID)
		}
		user := strings.TrimSpace(host.SSHUser)
		for _, field := range []struct{ name, value string }{{"SSH 用户名", user}, {"SSH 密码", host.SSHPassword}} {
			if err := checkText(host.ID+" 的"+field.name, field.value, false); err != nil {
				return nil, err
			}
		}
		port, err := sshPort(host.ID, host.SSHPort)
		if err != nil {
			return nil, err
		}
		ssh := "{ port = " + strconv.Itoa(port) + ", username = " + tomlString(user)
		if host.SSHPassword != "" {
			ssh += ", password = " + tomlString(host.SSHPassword)
		}
		set("hosts."+host.ID, "ssh", ssh+" }")
	}

	set("storage_retention", "view_bars", strconv.FormatUint(form.Retention.ViewBars, 10))
	for freq := range form.Retention.Defaults {
		if err := retentionCell(freq, ""); err != nil {
			return nil, err
		}
	}
	for _, freq := range retentionFrequencies {
		value, ok := form.Retention.Defaults[freq]
		if !ok {
			continue
		}
		if err := retentionCell(freq, value); err != nil {
			return nil, err
		}
		set("storage_retention.defaults", freq, tomlString(strings.TrimSpace(value)))
	}
	for _, space := range form.Retention.Spaces {
		if _, ok := current.StorageRetention.Spaces[space.ID]; !ok {
			return nil, fmt.Errorf("配置里没有空间 %q 的保留期", space.ID)
		}
		section := "storage_retention.spaces." + space.ID
		for freq := range space.Values {
			if err := retentionCell(freq, ""); err != nil {
				return nil, err
			}
		}
		for _, freq := range retentionFrequencies {
			value, ok := space.Values[freq]
			if !ok {
				continue
			}
			if err := retentionCell(freq, value); err != nil {
				return nil, err
			}
			if strings.TrimSpace(value) == "" {
				// 留空表示这个空间跟随默认值。
				lines = dropKey(lines, section, freq)
				continue
			}
			set(section, freq, tomlString(strings.TrimSpace(value)))
		}
	}
	set("local_logs", "max_size_mb", strconv.Itoa(form.Logs.MaxSizeMB))
	set("local_logs", "backup_count", strconv.Itoa(form.Logs.BackupCount))
	return []byte(strings.Join(lines, "\n")), nil
}

func retentionCell(freq, value string) error {
	known := false
	for _, f := range retentionFrequencies {
		known = known || f == freq
	}
	if !known {
		return fmt.Errorf("不认识的频率 %q", freq)
	}
	return checkText("保留期", strings.TrimSpace(value), false)
}
