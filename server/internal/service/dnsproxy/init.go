package dnsproxy

import (
	"github.com/mooyang-code/moox/server/internal/service/dnsproxy/logic"
)

// DnsproxySchedule 定时器入口函数 - 定时解析配置的域名并缓存结果
// 这个函数需要被定时器调用，所以保持在包级别
var DnsproxySchedule = logic.DnsproxySchedule

// 如果需要其他初始化逻辑，可以在这里添加
func init() {
	// DNS代理服务初始化逻辑
}