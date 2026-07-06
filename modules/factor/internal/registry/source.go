package registry

import (
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
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

// DependsInfo stores V1 source-level dependency hints.
type DependsInfo struct {
	ExtraColumns []string `json:"extra_columns,omitempty"`
}

// DependsJSONFromSource returns dependency hints detected from trusted factor source.
func DependsJSONFromSource(source string) string {
	extra := ExtraColumnsFromSource(source)
	if len(extra) == 0 {
		return domain.DefaultFactorDependsJSON
	}
	raw, err := json.Marshal(DependsInfo{ExtraColumns: extra})
	if err != nil {
		return domain.DefaultFactorDependsJSON
	}
	return string(raw)
}

// ExtraColumnsFromSource extracts columns referenced by xbx extra_data_dict.
func ExtraColumnsFromSource(source string) []string {
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

// ExtraColumnsFromFactors returns the union of imported extra columns.
func ExtraColumnsFromFactors(factors []domain.FactorDef) []string {
	set := map[string]struct{}{}
	for _, factor := range factors {
		for _, column := range extraColumnsFromDepends(factor.DependsJSON) {
			set[column] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for column := range set {
		out = append(out, column)
	}
	sort.Strings(out)
	return out
}

func extraColumnsFromDepends(raw string) []string {
	var info DependsInfo
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &info); err != nil {
		return nil
	}
	return info.ExtraColumns
}
