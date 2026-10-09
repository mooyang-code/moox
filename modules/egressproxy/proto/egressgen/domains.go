package egresspb

import (
	"fmt"
	"net"
	"strings"
)

// 域名白名单的写法和匹配规则属于出口代理协议的一部分：出口代理据此放行 Do 请求，Collector 据此决定哪些
// HTTP 请求走出口代理，CLI 据此校验 moox.toml。三方共用这一份实现，保证理解一致。

// DomainList 是域名白名单。条目是完整域名，或以 "*." 开头表示它的所有子域名（不含它自身）。零值是空白名单。
type DomainList struct {
	exact    map[string]struct{}
	suffixes []string
}

// ParseDomainList 校验并规范化白名单条目，不区分大小写；条目无效或重复时报错，允许为空。
func ParseDomainList(entries []string) (DomainList, error) {
	list := DomainList{exact: make(map[string]struct{}, len(entries))}
	seen := make(map[string]struct{}, len(entries))
	for _, raw := range entries {
		entry := strings.ToLower(strings.TrimSpace(raw))
		if _, duplicate := seen[entry]; duplicate {
			return DomainList{}, fmt.Errorf("白名单条目 %q 重复", raw)
		}
		seen[entry] = struct{}{}
		if suffix, ok := strings.CutPrefix(entry, "*."); ok {
			if !ValidDomain(suffix) {
				return DomainList{}, fmt.Errorf("白名单条目 %q 无效", raw)
			}
			list.suffixes = append(list.suffixes, "."+suffix)
			continue
		}
		if !ValidDomain(entry) {
			return DomainList{}, fmt.Errorf("白名单条目 %q 无效", raw)
		}
		list.exact[entry] = struct{}{}
	}
	return list, nil
}

// Empty 判断白名单是否为空。
func (l DomainList) Empty() bool {
	return len(l.exact) == 0 && len(l.suffixes) == 0
}

// Allows 判断主机名是否在白名单内。不区分大小写，忽略末尾的点；带端口或不是合法域名时一律不匹配。
func (l DomainList) Allows(host string) bool {
	host = NormalizeHost(host)
	if !ValidDomain(host) {
		return false
	}
	if _, ok := l.exact[host]; ok {
		return true
	}
	for _, suffix := range l.suffixes {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// NormalizeHost 把主机名转成小写，并去掉首尾空白和末尾的点。
func NormalizeHost(host string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
}

// ValidDomain 判断是否是合法的域名：至少两段，只含小写字母、数字和连字符，不能是 IP 地址。
func ValidDomain(domain string) bool {
	if domain == "" || len(domain) > 253 || net.ParseIP(domain) != nil {
		return false
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return false
			}
		}
	}
	return true
}
