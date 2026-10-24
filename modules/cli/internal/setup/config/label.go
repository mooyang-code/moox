package config

import (
	"regexp"
	"strings"
)

// hostRoleLabels 是主机 ID 里的角色名对应的中文名。
var hostRoleLabels = map[string]string{
	"control":  "控制机",
	"storage":  "存储机",
	"overseas": "海外机",
	"factor":   "因子机",
}

var hostRolePattern = regexp.MustCompile(`^([a-z]+)(?:-([0-9]+))?$`)

// DefaultHostLabel 按主机 ID 生成中文名：角色名直接对应（overseas → 海外机），带数字后缀时保留序号
// （overseas-2 → 海外机 2）；不是已知角色时返回空，界面回退显示主机 ID。
func DefaultHostLabel(hostID string) string {
	match := hostRolePattern.FindStringSubmatch(strings.ToLower(hostID))
	if match == nil {
		return ""
	}
	label, ok := hostRoleLabels[match[1]]
	if !ok {
		return ""
	}
	if match[2] != "" {
		return label + " " + match[2]
	}
	return label
}
