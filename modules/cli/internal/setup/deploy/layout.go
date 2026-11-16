// Package deploy 实现新的部署路径：一台主机一个部署根，组件由 systemd 用户服务托管。
//
// 一次部署（Deploy）做的事：把这台主机的登记同步到 Admin，把要用的二进制放到目标机，向控制机要身份材料，
// 渲染配置、环境文件与单元文件，打成一个包传到目标机，由内嵌的 install.sh 持文件锁装好并按启动顺序重启有变化的组件，
// 写 version.json，最后等本次装的组件就绪。重跑同一条命令就是再做一遍，没有过程记录。
package deploy

import (
	"path"
	"strings"

	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// VersionFile 是部署根下的部署记录：有它就表示这台主机装过。
const VersionFile = "version.json"

// StagingDirectory 是目标机下载组件包的目录（部署根下）。
const StagingDirectory = ".staging"

// Layout 是一台主机的部署根布局。路径都是目标机（Linux）上的绝对路径。
type Layout struct{ Root string }

func (l Layout) Bin(name string) string         { return path.Join(l.Root, "bin", name) }
func (l Layout) Config(component string) string { return path.Join(l.Root, "config", component) }
func (l Layout) Data(component string) string   { return path.Join(l.Root, "data", component) }
func (l Layout) Logs(component string) string   { return path.Join(l.Root, "logs", component) }
func (l Layout) Secrets() string                { return path.Join(l.Root, "secrets") }
func (l Layout) Secret(name string) string      { return path.Join(l.Root, "secrets", name) }
func (l Layout) Env(component string) string {
	return path.Join(l.Root, "secrets", "env", component+".env")
}
func (l Layout) Version() string { return path.Join(l.Root, VersionFile) }
func (l Layout) Staging() string { return path.Join(l.Root, StagingDirectory) }

// HostPolicy 是主机策略缓存：HostAgent 从 Monitor 同步后写在自己的数据目录里，Admin 的证书巡检读它取告警渠道。
func (l Layout) HostPolicy() string { return path.Join(l.Data("host-agent"), "host-policy.json") }

// Expand 把服务目录部署字段里的占位换成这个组件的绝对路径。
func (l Layout) Expand(component, value string) string {
	return strings.NewReplacer(
		"{root}", l.Root, "{config}", l.Config(component), "{data}", l.Data(component),
		"{logs}", l.Logs(component), "{secrets}", l.Secrets(),
	).Replace(value)
}

// UnitName 是组件的 systemd 用户服务名。
func UnitName(component string) string { return servicecatalog.UnitName(component) }
