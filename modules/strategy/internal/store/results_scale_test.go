package store

import (
	"context"
	"testing"
)

// 解释明细的仓位系数读写：写了系数的读回原值，没写的为空。
func TestResultItemsStoreScale(t *testing.T) {
	repo := openTestStore(t)
	ctx := context.Background()
	seedEnabledInstance(t, repo, "i1", "session-1", nil)
	items := []ResultItem{
		{RuleID: "r", InstrumentID: "BTC-USDT", Stage: "weighted", Weight: ptr("0.25"), Scale: ptr(0.5)},
		{RuleID: "r", InstrumentID: "ETH-USDT", Stage: "weighted", Weight: ptr("0.5")},
	}
	mustCommit(t, repo, okResult("r1", "i1", "session-1", 1, PublishNone), items, testNow)
	listed, err := repo.ListResultItems(ctx, "r1")
	if err != nil || len(listed) != 2 || listed[0].Scale == nil || *listed[0].Scale != 0.5 || listed[1].Scale != nil {
		t.Fatalf("系数读写不符：%+v err=%v", listed, err)
	}
}
