package config

import "github.com/mooyang-code/moox/modules/cli/internal/setup/topology"

// DefaultHostLabel 按主机 ID 生成中文名，规则在新包里。
func DefaultHostLabel(hostID string) string { return topology.DefaultHostLabel(hostID) }
