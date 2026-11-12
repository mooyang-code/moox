package barclock

import "testing"

func TestMarketsSortedAndComplete(t *testing.T) {
	got := Markets()
	if len(got) != len(markets) {
		t.Fatalf("Markets() 返回 %d 个，登记了 %d 个", len(got), len(markets))
	}
	for i, market := range got {
		if registered, ok := markets[market.ID]; !ok || registered.Alignment != market.Alignment {
			t.Fatalf("Markets()[%d]=%+v 与登记不一致", i, market)
		}
		if i > 0 && got[i-1].ID >= market.ID {
			t.Fatalf("Markets() 没有按 ID 排序：%s 在 %s 之后", market.ID, got[i-1].ID)
		}
	}
}
