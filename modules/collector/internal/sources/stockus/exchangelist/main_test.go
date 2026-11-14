package exchangelist

import (
	"os"
	"testing"

	"github.com/mooyang-code/moox/packages/cnstock"
)

// looseRequestLimit 给进程内的东方财富限流器换上宽松的参数：这里的测试用真实主机名加假网络翻完整的美股列表（约 140 页），
// 不能按每秒 2 个真的等待，也不要在测试之间留下冷却状态。要测限流与冷却行为的用例自己配置，结束时再调用它恢复。
func looseRequestLimit() {
	cfg := cnstock.DefaultRequestLimitConfig()
	cfg.RequestsPerSecond, cfg.Burst, cfg.MaxConcurrent, cfg.CooldownFailures = 20, 50, 8, 50
	if err := cnstock.ConfigureRequestLimit(cfg, nil); err != nil {
		panic(err)
	}
}

func TestMain(m *testing.M) {
	looseRequestLimit()
	os.Exit(m.Run())
}
