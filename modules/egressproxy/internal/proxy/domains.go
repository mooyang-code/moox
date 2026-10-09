package proxy

import (
	"fmt"
	"net"
	"strings"
)

// DomainList 是出口代理放行的域名白名单。条目是完整域名，或以 "*." 开头表示它的所有子域名（不含它自身）。
type DomainList struct {
	exact    map[string]struct{}
	suffixes []string
}

// NewDomainList 校验并规范化白名单条目。
func NewDomainList(entries []string) (DomainList, error) {
	list := DomainList{exact: map[string]struct{}{}}
	for _, raw := range entries {
		entry := strings.ToLower(strings.TrimSpace(raw))
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
	if len(list.exact) == 0 && len(list.suffixes) == 0 {
		return DomainList{}, fmt.Errorf("域名白名单不能为空")
	}
	return list, nil
}

// Allows 判断域名是否在白名单内；host 必须已规范化为小写、不带端口。
func (l DomainList) Allows(host string) bool {
	if _, ok := l.exact[host]; ok {
		return true
	}
	for _, suffix := range l.suffixes {
		if strings.HasSuffix(host, suffix) && len(host) > len(suffix) {
			return true
		}
	}
	return false
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
