package input

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A 股索引里的行全部晚于当前已闭合的最后一根时（未来的交易日，或盘中新建、只有当天一根的 View），说明是“晚于已闭合”，
// 而不是“都不在交易日上”；行全部落在周末时才说不在交易日上。
func TestStockRowsLaterThanClosedBarAreReportedAsFuture(t *testing.T) {
	now := time.Date(2026, 10, 12, 10, 0, 0, 0, shanghai(t))
	resolved := Resolved{ViewID: "view_stock", Bar: "1d", Calendar: "cn_stock"}
	for name, view := range map[string]ViewInfo{
		"未来的交易日":    {IndexedFrom: stockDay(t, 2026, 10, 20), IndexedTo: stockDay(t, 2026, 10, 23), SeriesBars: 500},
		"只有当天盘中的一行": {IndexedFrom: stockDay(t, 2026, 10, 12), IndexedTo: stockDay(t, 2026, 10, 12), SeriesBars: 500},
	} {
		_, err := CoverageBounds(view, resolved, now)
		if err == nil || !strings.Contains(err.Error(), "晚于当前已闭合的最后一根") || strings.Contains(err.Error(), "不在 A 股交易日上") {
			t.Fatalf("%s：应说明晚于当前已闭合的最后一根：%v", name, err)
		}
		// 设置了 min_age_bars 的启用与回放提交都带着同样的说明。
		aged := resolved
		aged.MinAgeBars = 5
		if err := checkAgeCoverage(view, aged, 5, now); err == nil || !strings.Contains(err.Error(), "晚于当前已闭合的最后一根") {
			t.Fatalf("%s：启用校验应带同样的说明：%v", name, err)
		}
		if _, err := ReplayWindow(resolved, nil, view, stockDay(t, 2026, 9, 1), stockDay(t, 2026, 10, 1), now); err == nil || !strings.Contains(err.Error(), "晚于当前已闭合的最后一根") {
			t.Fatalf("%s：回放提交应带同样的说明：%v", name, err)
		}
	}
	weekend := ViewInfo{IndexedFrom: stockDay(t, 2026, 10, 10), IndexedTo: stockDay(t, 2026, 10, 11), SeriesBars: 500}
	if _, err := CoverageBounds(weekend, resolved, now); err == nil || !strings.Contains(err.Error(), "都不在 A 股交易日上") || strings.Contains(err.Error(), "晚于") {
		t.Fatalf("行全部落在周末应说明不在交易日上：%v", err)
	}
}

// 没有 min_age_bars 的 A 股实例启用时只校验行键：覆盖范围不影响实时求值，新建的 View 只有未来行或当天盘中一行也能启用。
func TestStockEnableWithoutMinAgeOnlyChecksRowKey(t *testing.T) {
	now := time.Date(2026, 10, 12, 10, 0, 0, 0, shanghai(t))
	resolved := Resolved{ViewID: "view_stock", Bar: "1d", Calendar: "cn_stock"}
	client := newFakeClient("spot")
	for name, view := range map[string]ViewInfo{
		"未来的交易日":    {ViewID: "view_stock", IndexedFrom: stockDay(t, 2026, 10, 20), IndexedTo: stockDay(t, 2026, 10, 23), SeriesBars: 500},
		"只有当天盘中的一行": {ViewID: "view_stock", IndexedFrom: stockDay(t, 2026, 10, 12), IndexedTo: stockDay(t, 2026, 10, 12), SeriesBars: 500},
		"周末的行":      {ViewID: "view_stock", IndexedFrom: stockDay(t, 2026, 10, 10), IndexedTo: stockDay(t, 2026, 10, 11), SeriesBars: 500},
	} {
		client.views["view_stock"] = view
		if err := CheckCoverage(context.Background(), client, "space", resolved, now); err != nil {
			t.Fatalf("%s：没有 min_age_bars 时只校验行键，应允许启用：%v", name, err)
		}
	}
}
