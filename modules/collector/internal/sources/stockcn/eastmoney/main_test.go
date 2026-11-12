package eastmoney

import (
	"os"
	"testing"

	"github.com/mooyang-code/moox/packages/cnstock"
)

// TestMain 给进程内的东方财富限流器换上宽松的参数：这里的测试用真实主机名加假网络翻列表，
// 不必按每秒 2 个真的等待，也不会在用例之间留下冷却状态。
func TestMain(m *testing.M) {
	cfg := cnstock.DefaultRequestLimitConfig()
	cfg.RequestsPerSecond, cfg.Burst, cfg.MaxConcurrent, cfg.CooldownFailures = 20, 50, 8, 50
	if err := cnstock.ConfigureRequestLimit(cfg, nil); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}
