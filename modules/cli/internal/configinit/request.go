package configinit

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// Request 是录入页提交的内容。其中的密码和密钥只用来渲染 moox.toml，不写日志、不回显、不出现在错误信息里。
type Request struct {
	Admin struct {
		Username string `json:"username"`
		Password string `json:"password"`
	} `json:"admin"`
	Tencent struct {
		SecretID  string `json:"secret_id"`
		SecretKey string `json:"secret_key"`
		Region    string `json:"region"`
	} `json:"tencent"`
	Notification struct {
		ChannelType string `json:"channel_type"`
		WebhookURL  string `json:"webhook_url"`
	} `json:"notification"`
	Hosts []Host `json:"hosts"`
	// Compile 是可选的 Linux 编译机（仅 CGO 制品需要）；不填则为 nil。
	Compile *Host `json:"compile"`
}

// Host 是一台主机的定义。Components 只对部署主机有意义，编译机不放置组件。
type Host struct {
	ID             string   `json:"id"`
	Address        string   `json:"address"`
	PrivateAddress string   `json:"private_address"`
	Region         string   `json:"region"`
	Provider       string   `json:"provider"`
	TLSMode        string   `json:"tls_mode"`
	SSHPort        int      `json:"ssh_port"`
	SSHUser        string   `json:"ssh_user"`
	SSHPassword    string   `json:"ssh_password"`
	Components     []string `json:"components"`
}

const (
	maxFieldBytes = 512
	maxHosts      = 32
)

// normalize 去掉非秘密字段的首尾空白并做输入层面的检查；格式与拓扑的语义校验交给 config.Parse。
// 秘密字段原样保留（密码里的空格是有意义的），只拒绝控制字符与超长。
func (r *Request) normalize() error {
	trim := func(values ...*string) {
		for _, v := range values {
			*v = strings.TrimSpace(*v)
		}
	}
	trim(&r.Admin.Username, &r.Tencent.SecretID, &r.Tencent.Region, &r.Notification.ChannelType, &r.Notification.WebhookURL)
	if err := checkText("管理员用户名", r.Admin.Username, true); err != nil {
		return err
	}
	for _, secret := range []struct{ name, value string }{
		{"管理员密码", r.Admin.Password}, {"腾讯云 SecretKey", r.Tencent.SecretKey},
	} {
		if err := checkText(secret.name, secret.value, true); err != nil {
			return err
		}
	}
	for _, field := range []struct {
		name, value string
		required    bool
	}{
		{"腾讯云 SecretId", r.Tencent.SecretID, true}, {"腾讯云地域", r.Tencent.Region, true},
		{"通知渠道", r.Notification.ChannelType, true}, {"通知 webhook 地址", r.Notification.WebhookURL, true},
	} {
		if err := checkText(field.name, field.value, field.required); err != nil {
			return err
		}
	}
	if len(r.Hosts) == 0 {
		return fmt.Errorf("至少需要一台部署主机")
	}
	if len(r.Hosts) > maxHosts {
		return fmt.Errorf("部署主机最多 %d 台", maxHosts)
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for i := range r.Hosts {
		if err := r.Hosts[i].normalize(false, catalog); err != nil {
			return fmt.Errorf("主机 %d：%w", i+1, err)
		}
		if seen[r.Hosts[i].ID] {
			return fmt.Errorf("主机 ID %q 重复", r.Hosts[i].ID)
		}
		seen[r.Hosts[i].ID] = true
	}
	if r.Compile != nil {
		if err := r.Compile.normalize(true, catalog); err != nil {
			return fmt.Errorf("编译机：%w", err)
		}
		if seen[r.Compile.ID] {
			return fmt.Errorf("编译机 ID %q 与部署主机重复", r.Compile.ID)
		}
	}
	return nil
}

func (h *Host) normalize(compile bool, catalog servicecatalog.Catalog) error {
	for _, v := range []*string{&h.ID, &h.Address, &h.PrivateAddress, &h.Region, &h.Provider, &h.TLSMode, &h.SSHUser} {
		*v = strings.TrimSpace(*v)
	}
	if !servicecatalog.ValidHostID(h.ID) {
		return fmt.Errorf("主机 ID %q 不合法，请用小写字母、数字、连字符", h.ID)
	}
	for _, field := range []struct {
		name, value string
		required    bool
	}{
		{"公网地址", h.Address, true}, {"私网地址", h.PrivateAddress, false}, {"地域", h.Region, false},
		{"provider", h.Provider, false}, {"SSH 用户名", h.SSHUser, true}, {"SSH 密码", h.SSHPassword, true},
	} {
		if err := checkText(h.ID+" 的"+field.name, field.value, field.required); err != nil {
			return err
		}
	}
	if h.SSHPort == 0 {
		h.SSHPort = 22
	}
	if compile {
		h.Components = nil
		return nil
	}
	if len(h.Components) == 0 {
		return fmt.Errorf("%s 至少要放置一个组件", h.ID)
	}
	seen := map[string]bool{}
	for _, id := range h.Components {
		component, ok := catalog.Component(id)
		if !ok || component.Scope == servicecatalog.ScopeHost {
			return fmt.Errorf("%s 的组件 %q 不存在或由系统自动放置（host-gateway、host-agent 不用选）", h.ID, id)
		}
		if seen[id] {
			return fmt.Errorf("%s 的组件 %q 重复", h.ID, id)
		}
		seen[id] = true
	}
	return nil
}

// checkText 拒绝控制字符（包括换行）、非法 UTF-8 和超长，保证渲染进 TOML 的值不会破坏文件结构。
func checkText(name, value string, required bool) error {
	if value == "" {
		if required {
			return fmt.Errorf("%s不能为空", name)
		}
		return nil
	}
	if len(value) > maxFieldBytes {
		return fmt.Errorf("%s超过 %d 字节", name, maxFieldBytes)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s不是合法的 UTF-8 文本", name)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s含有控制字符或换行", name)
		}
	}
	return nil
}
