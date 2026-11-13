package catalog

import (
	"strings"

	"github.com/rs/xid"
)

// defaultID 由名称推出 ID，名称为空时用前缀加随机串。
func defaultID(name, prefix string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return prefix + "_" + xid.New().String()
	}
	replacer := strings.NewReplacer(" ", "_", "/", "_", "\\", "_", ":", "_")
	return replacer.Replace(name)
}
