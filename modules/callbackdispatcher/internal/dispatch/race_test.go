package dispatch

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/action"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/delivery"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/stretchr/testify/require"
)

// mergingHandler 在 Step 执行期间让另一个事件合并进同一条记录（下架时间提前了），模拟处理期间到达的新事件。
type mergingHandler struct {
	merge   func()
	outcome action.Outcome
}

func (h *mergingHandler) Step(context.Context, store.Execution, time.Time) (action.Outcome, error) {
	h.merge()
	return h.outcome, nil
}
func (*mergingHandler) OnCancel(context.Context, store.Execution) error { return nil }

// 处理期间下架时间被改了：按旧时间算出的下一步安排不能覆盖新安排，记录必须立即重新处理。
func TestEventMergedDuringStepIsNotLost(t *testing.T) {
	for name, outcome := range map[string]action.Outcome{
		"按旧时间安排了下一步": {Status: store.StatusPending, NextRunAt: baseTime.Add(6 * 24 * time.Hour)},
		"按旧时间判定已完成":  {Status: store.StatusSucceeded},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s := openStore(t)
			c := &clock{now: baseTime}
			seeded := seedSystem(t, s, c)

			intake := newIntake(t, s)
			intake.Now = c.Now
			earlier := baseTime.Add(48 * time.Hour)
			handler := &mergingHandler{outcome: outcome}
			handler.merge = func() {
				delivery := delistDelivery(t, "binance", events.DelistMarketSpot, "FOOUSDT", earlier)
				require.Equal(t, Accepted, handle(ctx, intake, delivery))
			}
			registry := action.NewRegistry()
			registry.Register(store.SystemActionSellDelistedHoldings, handler)
			worker := newWorker(t, s, &fakeSender{results: []delivery.Result{{}}}, registry, c)

			require.Equal(t, 1, worker.RunOnce(ctx))
			got, err := s.GetExecution(ctx, seeded.ExecutionID)
			require.NoError(t, err)
			require.Equal(t, store.StatusPending, got.Status)
			require.Equal(t, baseTime.UnixMilli(), got.NextRunAt, "合并进来的新事件让记录立即到期")
			require.Contains(t, got.PayloadJSON, `"delist_at":"2026-11-06T08:00:00Z"`)
			require.Zero(t, got.FinishedAt)
		})
	}
}
