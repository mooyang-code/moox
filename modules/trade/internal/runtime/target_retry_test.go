package runtime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 同一个目标反复收敛时，定时唤醒按目标去重，不会每次唤醒都多挂一个定时器。
func TestScheduleRetryKeepsOneTimerPerTarget(t *testing.T) {
	worker := &TargetWorker{}
	for range 100 {
		worker.scheduleRetry("space-1", "logical-1", time.Hour)
	}
	worker.scheduleRetry("space-1", "logical-2", time.Hour)

	worker.retryMu.Lock()
	defer worker.retryMu.Unlock()
	require.Len(t, worker.retryTimers, 2)
	for _, timer := range worker.retryTimers {
		timer.Stop()
	}
}
