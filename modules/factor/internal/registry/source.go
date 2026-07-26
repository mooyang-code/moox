package registry

import (
	"crypto/sha1"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var (
	extraDataDictPattern = regexp.MustCompile(`(?s)extra_data_dict\s*=\s*\{(.*?)\}`)
	listValuePattern     = regexp.MustCompile(`:\s*\[([^\]]*)\]`)
	quotedStringPattern  = regexp.MustCompile(`['"]([^'"]+)['"]`)
)

// DefaultLookback returns the default rolling-window warmup size.
func DefaultLookback(params []int) int {
	maxParam := 0
	for _, param := range params {
		if param > maxParam {
			maxParam = param
		}
	}
	lookback := maxParam * 3
	if lookback < 200 {
		return 200
	}
	return lookback
}

// ResultDataset returns the default factor result dataset for one source dataset.
func ResultDataset(sourceDataset string) string {
	sourceDataset = strings.TrimSpace(strings.ToLower(sourceDataset))
	base := strings.TrimSuffix(sourceDataset, "_kline")
	candidate := base + "_factor"
	if len(candidate) <= 20 {
		return candidate
	}
	sum := sha1.Sum([]byte(sourceDataset))
	suffix := fmt.Sprintf("_f%x", sum[:2])
	prefixLen := 20 - len(suffix)
	prefix := strings.TrimRight(base, "_")
	if len(prefix) > prefixLen {
		prefix = strings.TrimRight(prefix[:prefixLen], "_")
	}
	if prefix == "" {
		prefix = "dataset"
	}
	return prefix + suffix
}

// DependsFromSource extracts extra input columns referenced by xbx extra_data_dict.
func DependsFromSource(source string) []string {
	matches := extraDataDictPattern.FindStringSubmatch(source)
	if len(matches) < 2 {
		return nil
	}
	set := map[string]struct{}{}
	for _, list := range listValuePattern.FindAllStringSubmatch(matches[1], -1) {
		if len(list) < 2 {
			continue
		}
		for _, quoted := range quotedStringPattern.FindAllStringSubmatch(list[1], -1) {
			if len(quoted) < 2 {
				continue
			}
			name := strings.TrimSpace(quoted[1])
			if name != "" {
				set[name] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
