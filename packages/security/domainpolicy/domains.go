// Package domainpolicy matches explicit DNS names and subdomain suffixes.
package domainpolicy

import (
	"errors"
	"net"
	"strings"
)

type Matcher struct {
	exact    map[string]struct{}
	suffixes []string
}

// Host accepts an ASCII DNS hostname without a scheme, port, address or userinfo.
func Host(raw string) (string, bool) {
	if raw != strings.TrimSpace(raw) {
		return "", false
	}
	host := strings.ToLower(strings.TrimSuffix(raw, "."))
	if host == "" || len(host) > 253 || net.ParseIP(host) != nil {
		return "", false
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return "", false
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", false
			}
		}
	}
	return host, true
}

// New treats *.example.com as subdomains only; the apex needs its own entry.
func New(patterns []string) (Matcher, error) {
	if len(patterns) > 64 {
		return Matcher{}, errors.New("domain policy supports at most 64 entries")
	}
	matcher := Matcher{exact: map[string]struct{}{}}
	seen := map[string]bool{}
	for _, pattern := range patterns {
		suffix := strings.HasPrefix(pattern, "*.")
		name, ok := Host(strings.TrimPrefix(pattern, "*."))
		if !ok {
			return Matcher{}, errors.New("domain policy requires exact DNS names or *. suffixes")
		}
		canonical := name
		if suffix {
			canonical = "*." + name
		}
		if seen[canonical] {
			return Matcher{}, errors.New("duplicate domain policy entry")
		}
		seen[canonical] = true
		if suffix {
			matcher.suffixes = append(matcher.suffixes, "."+name)
		} else {
			matcher.exact[name] = struct{}{}
		}
	}
	return matcher, nil
}

func (m Matcher) Allows(raw string) bool {
	host, ok := Host(raw)
	if !ok {
		return false
	}
	if _, found := m.exact[host]; found {
		return true
	}
	for _, suffix := range m.suffixes {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}
