package configinit

import (
	"fmt"
	"slices"
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
	// Servers 按角色收集服务器；因子执行服务器可选（nil 表示不配置）。
	Servers struct {
		Storage  *Server `json:"storage"`
		Overseas *Server `json:"overseas"`
		Control  *Server `json:"control"`
		Factor   *Server `json:"factor"`
	} `json:"servers"`

	// normalize 根据角色算出的部署主机与编译机，供渲染使用。
	hosts   []Host
	compile *Host
}

// Server 是页面上一类服务器的填写内容。
type Server struct {
	// SharedWith 不为空时表示与该角色共用同一台服务器（只有管理台主控可以），其余字段忽略。
	SharedWith     string `json:"shared_with"`
	ID             string `json:"id"`
	Address        string `json:"address"`
	PrivateAddress string `json:"private_address"`
	Region         string `json:"region"`
	Provider       string `json:"provider"`
	TLSMode        string `json:"tls_mode"`
	SSHPort        int    `json:"ssh_port"`
	SSHUser        string `json:"ssh_user"`
	SSHPassword    string `json:"ssh_password"`
}

// Host 是渲染进 moox.toml 的一台主机。Components 只对部署主机有意义，编译机不放置组件。
type Host struct {
	ID             string
	Address        string
	PrivateAddress string
	Region         string
	Provider       string
	TLSMode        string
	SSHPort        int
	SSHUser        string
	SSHPassword    string
	Components     []string
}

const maxFieldBytes = 512

// normalize 去掉非秘密字段的首尾空白并做输入层面的检查，再把角色换算成主机与组件放置；
// 格式与拓扑的语义校验交给 config.Parse。秘密字段原样保留（密码里的空格是有意义的），只拒绝控制字符与超长。
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
	return r.buildTopology()
}

// buildTopology 把四类服务器换算成部署主机（含组件放置）和编译机。
func (r *Request) buildTopology() error {
	roles, err := loadRoles()
	if err != nil {
		return err
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return err
	}
	r.hosts, r.compile = nil, nil
	byRole := map[string]int{}
	var titles []string // 与 hosts（以及排在最后的编译机）一一对应的角色名称，用于错误提示
	for _, item := range []struct {
		key    string
		server *Server
	}{{RoleStorage, r.Servers.Storage}, {RoleOverseas, r.Servers.Overseas}, {RoleControl, r.Servers.Control}} {
		role, _ := roleByKey(roles, item.key)
		if item.server == nil {
			return fmt.Errorf("缺少%s", role.Title)
		}
		if item.server.SharedWith != "" {
			if !slices.Contains(role.CanShareWith, item.server.SharedWith) {
				return fmt.Errorf("%s不能与 %q 共用服务器", role.Title, item.server.SharedWith)
			}
			target := &r.hosts[byRole[item.server.SharedWith]]
			target.Components = append(target.Components, role.components...)
			// 管理台的 HTTPS 入口在共用的主机上，证书模式取管理台的选择。
			if tls := firstNonEmpty(strings.TrimSpace(item.server.TLSMode), target.TLSMode, role.Defaults.TLSMode); tls != "" {
				target.TLSMode = tls
			}
			continue
		}
		host, err := item.server.host(role, catalog, false)
		if err != nil {
			return err
		}
		byRole[item.key] = len(r.hosts)
		r.hosts = append(r.hosts, host)
		titles = append(titles, role.Title)
	}
	// 管理台独立部署时排到最前面，生成的文件与示例模板的顺序一致，读起来更直观。
	if i, ok := byRole[RoleControl]; ok && i > 0 {
		r.hosts = append(append([]Host{r.hosts[i]}, r.hosts[:i]...), r.hosts[i+1:]...)
		titles = append(append([]string{titles[i]}, titles[:i]...), titles[i+1:]...)
	}
	if r.Servers.Factor != nil {
		role, _ := roleByKey(roles, RoleFactor)
		if r.Servers.Factor.SharedWith != "" {
			return fmt.Errorf("%s不能与其他服务器共用", role.Title)
		}
		host, err := r.Servers.Factor.host(role, catalog, true)
		if err != nil {
			return err
		}
		r.compile = &host
		titles = append(titles, role.Title)
	}

	all := append([]Host(nil), r.hosts...)
	if r.compile != nil {
		all = append(all, *r.compile)
	}
	ids, addresses := map[string]string{}, map[string]string{}
	for i, host := range all {
		title := titles[i]
		if other, ok := ids[host.ID]; ok {
			return fmt.Errorf("%s和%s的主机 ID 都是 %q，请在高级设置里改成不同的 ID", other, title, host.ID)
		}
		ids[host.ID] = title
		if other, ok := addresses[host.Address]; ok {
			control, _ := roleByKey(roles, RoleControl)
			switch control.Title {
			case title:
				return fmt.Errorf("%s和%s的地址相同；如果是同一台服务器，请在%s里选择“与%s共用”", other, title, title, other)
			case other:
				return fmt.Errorf("%s和%s的地址相同；如果是同一台服务器，请在%s里选择“与%s共用”", other, title, other, title)
			}
			return fmt.Errorf("%s和%s的地址相同，这两类服务器需要分开部署", other, title)
		}
		addresses[host.Address] = title
	}
	return nil
}

// host 校验一类服务器的填写内容并换算成主机；compile 为 true 时是编译机，不放置组件。
func (s *Server) host(role Role, catalog servicecatalog.Catalog, compile bool) (Host, error) {
	for _, v := range []*string{&s.ID, &s.Address, &s.PrivateAddress, &s.Region, &s.Provider, &s.TLSMode, &s.SSHUser} {
		*v = strings.TrimSpace(*v)
	}
	host := Host{
		ID: firstNonEmpty(s.ID, role.Defaults.ID), Address: s.Address, PrivateAddress: s.PrivateAddress,
		Region: s.Region, Provider: s.Provider, TLSMode: s.TLSMode, SSHPort: s.SSHPort, SSHUser: s.SSHUser, SSHPassword: s.SSHPassword,
	}
	if !servicecatalog.ValidHostID(host.ID) {
		return Host{}, fmt.Errorf("%s的主机 ID %q 不合法，请用小写字母、数字、连字符", role.Title, host.ID)
	}
	for _, field := range []struct {
		name, value string
		required    bool
	}{
		{"地址", host.Address, true}, {"私网地址", host.PrivateAddress, false}, {"地域", host.Region, false},
		{"云厂商", host.Provider, false}, {"SSH 用户名", host.SSHUser, true}, {"SSH 密码", host.SSHPassword, true},
	} {
		if err := checkText(role.Title+"的"+field.name, field.value, field.required); err != nil {
			return Host{}, err
		}
	}
	if host.SSHPort == 0 {
		host.SSHPort = 22
	}
	if host.SSHPort < 1 || host.SSHPort > 65535 {
		return Host{}, fmt.Errorf("%s的 SSH 端口 %d 不合法", role.Title, host.SSHPort)
	}
	if compile {
		return host, nil
	}
	for _, id := range role.components {
		if component, ok := catalog.Component(id); !ok || component.Scope == servicecatalog.ScopeHost {
			return Host{}, fmt.Errorf("%s的组件 %q 不在服务目录里，请同步模板", role.Title, id)
		}
	}
	host.Components = append([]string(nil), role.components...)
	return host, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
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
