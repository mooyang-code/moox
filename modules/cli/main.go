/*
Copyright © 2025 MooX Team
*/
package main

import "github.com/mooyang-code/moox/modules/cli/cmd"

// 版本信息变量，由构建时通过ldflags设置
var (
	Version   = "dev"     // 版本号
	BuildTime = "unknown" // 构建时间
	GitCommit = "unknown" // Git提交哈希
)

func main() {
	// 将版本信息传递给cmd包
	cmd.Version = Version
	cmd.BuildTime = BuildTime
	cmd.GitCommit = GitCommit

	cmd.Execute()
}
