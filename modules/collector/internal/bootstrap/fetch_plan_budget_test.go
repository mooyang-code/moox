package bootstrap

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 加密货币 1m 数据集的实际迟到窗口是 10 秒（Storage 周期期望里的 latenessLimitSeconds）。函数在收盘后依次经历：
// 错峰延迟、行情请求、写入存储；三段预算之和必须放得进窗口，否则最坏情况下写入到达时周期已封口。
// 写入预算也不能太小：它是拨号、TLS 与提交共用的截止时间，太小就没有时间重试建连失败。
func TestDefaultFetchBudgetsFitTheLatenessWindow(t *testing.T) {
	fetch := defaultSCFPlan().Fetch
	const lateness = 10 * time.Second
	total := time.Duration(fetch.StaggerMS+fetch.RequestTimeoutMS+fetch.StorageTimeoutMS) * time.Millisecond
	require.Less(t, total, lateness, "错峰 + 行情请求 + 写入的预算之和必须小于迟到窗口")
	require.GreaterOrEqual(t, time.Duration(fetch.StorageTimeoutMS)*time.Millisecond, 4*time.Second, "写入预算要容得下一次建连失败后的重试")
}
