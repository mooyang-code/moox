package configinit

import (
	"context"
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
	Tencent      Keys `json:"tencent"`
	Notification struct {
		ChannelType string `json:"channel_type"`
		WebhookURL  string `json:"webhook_url"`
	} `json:"notification"`
	// Servers 按用途收集服务器；海外服务器和因子机可选（nil 表示不配置）。
	Servers struct {
		Storage  *Server    `json:"storage"`
		Control  *Server    `json:"control"`
		Overseas *Server    `json:"overseas"`
		Factor   *SSHTarget `json:"factor"`
	} `json:"servers"`

	// 以下由 normalize 算出，供渲染使用。
	hosts  []Host
	region string
}

// Server 是一台腾讯云服务器的填写内容；地域等信息由公网 IP 识别得到，不让用户填。
type Server struct {
	// SharedWith 不为空时表示与该角色共用同一台服务器（只有管理台服务器可以），其余字段忽略。
	SharedWith string `json:"shared_with"`
	SSHTarget
}

// Host 是渲染进 moox.toml 的一台主机。
type Host struct {
	ID       string
	Address  string
	Region   string
	Provider string
	TLSMode  string
	SSHPort  int
	SSHUser  string
	// Outbound 为 true 表示只能出站（因子机），经 control 网关的反向连接中转。
	Outbound   bool
	Components []string
}

// Environment 是换算拓扑时要用的外部能力：识别腾讯云服务器、确认免密登录、检测因子机。
type Environment struct {
	Lookup func(ctx context.Context, keys Keys, address string) (Instance, error)
	Login  func(ctx context.Context, target SSHTarget) (LoginReport, error)
	Probe  func(ctx context.Context, target SSHTarget) (ProbeReport, error)
}

// requireLogin 确认这台服务器已经能用 MooX 密钥免密登录。
func (env Environment) requireLogin(ctx context.Context, title string, target SSHTarget) error {
	report, err := env.Login(ctx, target)
	if err != nil {
		return fmt.Errorf("%s：%w", title, err)
	}
	if !report.Ready {
		return fmt.Errorf("%s还不能免密登录，请回到「免密登录」一步完成授权", title)
	}
	return nil
}

const maxFieldBytes = 512

// normalize 检查填写内容，并借助 env 把服务器换算成主机与组件部署；格式与拓扑的语义校验交给 config.Parse。
// 秘密字段原样保留（密码里的空格是有意义的），只拒绝控制字符与超长。
func (r *Request) normalize(ctx context.Context, env Environment) error {
	r.Admin.Username = strings.TrimSpace(r.Admin.Username)
	r.Notification.ChannelType = strings.TrimSpace(r.Notification.ChannelType)
	r.Notification.WebhookURL = strings.TrimSpace(r.Notification.WebhookURL)
	for _, field := range []struct{ name, value string }{
		{"管理员用户名", r.Admin.Username}, {"管理员密码", r.Admin.Password},
		{"告警渠道", r.Notification.ChannelType}, {"告警机器人的 Webhook 地址", r.Notification.WebhookURL},
	} {
		if err := checkText(field.name, field.value, true); err != nil {
			return err
		}
	}
	keys, err := r.Tencent.normalize()
	if err != nil {
		return err
	}
	r.Tencent = keys
	return r.buildTopology(ctx, env)
}

// buildTopology 把各类服务器换算成部署主机（含组件部署）。
func (r *Request) buildTopology(ctx context.Context, env Environment) error {
	roles, err := loadRoles()
	if err != nil {
		return err
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return err
	}
	r.hosts, r.region = nil, ""
	byRole := map[string]int{}
	var titles []string // 与 hosts 一一对应，用于错误提示
	for _, item := range []struct {
		key    string
		server *Server
	}{{RoleStorage, r.Servers.Storage}, {RoleOverseas, r.Servers.Overseas}, {RoleControl, r.Servers.Control}} {
		role := roleByKey(roles, item.key)
		if item.server == nil {
			if role.Optional {
				continue
			}
			return fmt.Errorf("请填写%s", role.Title)
		}
		if shared := item.server.SharedWith; shared != "" {
			if !canShare(role, shared) {
				return fmt.Errorf("%s不能和 %q 共用", role.Title, shared)
			}
			index, ok := byRole[shared]
			if !ok {
				return fmt.Errorf("%s选择了和%s共用，但没有配置%s", role.Title, roleByKey(roles, shared).Title, roleByKey(roles, shared).Title)
			}
			target := &r.hosts[index]
			target.Components = append(target.Components, role.components...)
			// 管理台的 HTTPS 入口跟着搬到共用的主机上。
			if target.TLSMode == "" {
				target.TLSMode = role.tlsMode
			}
			continue
		}
		host, err := item.server.host(ctx, env, r.Tencent, role, catalog)
		if err != nil {
			return err
		}
		byRole[item.key] = len(r.hosts)
		r.hosts = append(r.hosts, host)
		titles = append(titles, role.Title)
	}
	// 管理台单独部署时排到最前面，生成的文件与示例模板的顺序一致。
	if i, ok := byRole[RoleControl]; ok && i > 0 {
		r.hosts = append(append([]Host{r.hosts[i]}, r.hosts[:i]...), r.hosts[i+1:]...)
		titles = append(append([]string{titles[i]}, titles[:i]...), titles[i+1:]...)
	}
	// 腾讯云的默认地域取管理台所在的地域。
	for _, host := range r.hosts {
		if slices.Contains(host.Components, "admin") {
			r.region = host.Region
		}
	}
	if r.Servers.Factor != nil {
		role := roleByKey(roles, RoleFactor)
		factor, err := r.factorHost(ctx, env, role, catalog)
		if err != nil {
			return err
		}
		r.hosts = append(r.hosts, factor)
		titles = append(titles, role.Title)
	}
	addresses := map[string]string{}
	for i, host := range r.hosts {
		if other, ok := addresses[host.Address]; ok {
			if other == roleByKey(roles, RoleControl).Title || titles[i] == roleByKey(roles, RoleControl).Title {
				return fmt.Errorf("%s和%s的 IP 相同；如果是同一台服务器，请在管理台服务器里选择共用", other, titles[i])
			}
			return fmt.Errorf("%s和%s的 IP 相同，这两类服务器需要分开", other, titles[i])
		}
		addresses[host.Address] = titles[i]
	}
	return nil
}

func canShare(role Role, with string) bool {
	for _, option := range role.Share {
		if option.Value != "" && option.Value == with {
			return true
		}
	}
	return false
}

// host 校验一台腾讯云服务器的填写内容，用公网 IP 识别它，并换算成带组件部署的主机。
func (s *Server) host(ctx context.Context, env Environment, keys Keys, role Role, catalog servicecatalog.Catalog) (Host, error) {
	target, err := s.SSHTarget.normalize(role.Title)
	if err != nil {
		return Host{}, err
	}
	s.SSHTarget = target
	for _, id := range role.components {
		if component, ok := catalog.Component(id); !ok || component.Scope == servicecatalog.ScopeHost {
			return Host{}, fmt.Errorf("%s的组件 %q 不在服务目录里，请同步模板", role.Title, id)
		}
	}
	instance, err := env.Lookup(ctx, keys, s.Address)
	if err != nil {
		return Host{}, fmt.Errorf("%s：%w", role.Title, err)
	}
	if err := env.requireLogin(ctx, role.Title, target); err != nil {
		return Host{}, err
	}
	return Host{
		ID: role.hostID, Address: s.Address, Region: instance.Region, Provider: "tencent", TLSMode: role.tlsMode,
		SSHPort: target.SSHPort, SSHUser: target.SSHUser, Components: append([]string(nil), role.components...),
	}, nil
}

// factorHost 检测因子机并换算成只能出站的部署主机，部署 factor-engine 与 archive-worker。
func (r *Request) factorHost(ctx context.Context, env Environment, role Role, catalog servicecatalog.Catalog) (Host, error) {
	target, err := r.Servers.Factor.normalize(role.Title)
	if err != nil {
		return Host{}, err
	}
	for _, id := range role.components {
		if component, ok := catalog.Component(id); !ok || component.Scope == servicecatalog.ScopeHost {
			return Host{}, fmt.Errorf("%s的组件 %q 不在服务目录里，请同步模板", role.Title, id)
		}
	}
	if err := env.requireLogin(ctx, role.Title, target); err != nil {
		return Host{}, err
	}
	report, err := env.Probe(ctx, target)
	if err != nil {
		return Host{}, err
	}
	if !report.OK {
		for _, item := range report.Items {
			if item.Status == CheckFail {
				return Host{}, fmt.Errorf("%s环境检测没有通过：%s %s", role.Title, item.Name, item.Detail)
			}
		}
		return Host{}, fmt.Errorf("%s环境检测没有通过", role.Title)
	}
	return Host{
		ID: role.hostID, Address: target.Address, SSHPort: target.SSHPort, SSHUser: target.SSHUser,
		Outbound: true, Components: append([]string(nil), role.components...),
	}, nil
}

func sshPort(title string, port int) (int, error) {
	if port == 0 {
		return 22, nil
	}
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("%s的 SSH 端口 %d 不合法", title, port)
	}
	return port, nil
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
