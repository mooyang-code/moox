/*
Copyright © 2025 MooX Team
*/
package main

import (
	"fmt"
	"os"

	"github.com/mooyang-code/moox/modules/cli/internal/command"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/plugin"
)

// 版本信息变量，由构建时通过ldflags设置
var (
	Version   = "dev"     // 版本号
	BuildTime = "unknown" // 构建时间
	GitCommit = "unknown" // Git提交哈希
)

func main() {
	// Keep RPC diagnostics separate from machine-readable command output.
	log.RegisterWriter("stderr", stderrLogWriter{})
	log.SetLogger(log.NewZapLog(log.Config{{Writer: "stderr", Level: "warn"}}))
	defer log.GetDefaultLogger().Sync()

	// 将版本信息传递给命令包
	command.Version = Version
	command.BuildTime = BuildTime
	command.GitCommit = GitCommit

	command.Execute()
}

type stderrLogWriter struct{}

func (stderrLogWriter) Type() string { return "log" }

func (stderrLogWriter) Setup(_ string, decoder plugin.Decoder) error {
	output, ok := decoder.(*log.Decoder)
	if !ok {
		return fmt.Errorf("invalid CLI log decoder")
	}
	level := zap.NewAtomicLevelAt(zap.WarnLevel)
	output.Core = zapcore.NewCore(zapcore.NewConsoleEncoder(zap.NewDevelopmentEncoderConfig()), zapcore.Lock(os.Stderr), level)
	output.ZapLevel = level
	return nil
}
