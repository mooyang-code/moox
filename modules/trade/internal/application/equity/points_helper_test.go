package equity

import (
	"context"
	"math"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// allLogicalEquityPoints 取 logical-1 的全部权益点：桶宽 1 分钟，等于不降采样。
func allLogicalEquityPoints(tradeStore *store.Store) ([]store.EquityPointRecord, error) {
	return tradeStore.QueryLogicalAccountEquityCurve(context.Background(), "space-1", "logical-1", 0, math.MaxInt64, time.Minute.Milliseconds())
}
