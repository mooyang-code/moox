package bootstrap

import (
	"context"
	"strings"
	"testing"
	"time"
)

const stockResolvedJSON = `{"view_id":"view_a","dataset_id":"ds","bar":"1d","calendar":"cn_stock","spot":true,"columns":{},"view_columns":["close"]}`

// 有启用实例使用 A 股日历时，内嵌日历将到期给出提醒，已过期报未就绪；没有实例用它时不打扰。
func TestStockCalendarWarning(t *testing.T) {
	repo := openStore(t)
	ctx := context.Background()
	expired := time.Date(2027, 1, 4, 0, 0, 0, 0, time.UTC)
	if warning, down := stockCalendarWarning(ctx, repo, expired); warning != "" || down {
		t.Fatalf("没有实例使用 A 股日历时不应告警：%q %v", warning, down)
	}
	seedEnabled(t, repo, "crypto", nil, resolvedJSON)
	if warning, down := stockCalendarWarning(ctx, repo, expired); warning != "" || down {
		t.Fatalf("只有加密货币实例时不应告警：%q %v", warning, down)
	}
	seedEnabled(t, repo, "stock", nil, stockResolvedJSON)
	if warning, down := stockCalendarWarning(ctx, repo, time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)); warning != "" || down {
		t.Fatalf("日历有效期内不应告警：%q %v", warning, down)
	}
	// 内嵌日历止于 2026-12-31：12-30、12-31 的周期推不出有效期（往后 2 根），实际可用到 12-29。
	if warning, down := stockCalendarWarning(ctx, repo, time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC)); !strings.Contains(warning, "2026-12-29 之后的周期将无法处理") || down {
		t.Fatalf("可用截止日前 30 天内应提醒但仍就绪：%q %v", warning, down)
	}
	if warning, down := stockCalendarWarning(ctx, repo, time.Date(2026, 12, 29, 6, 0, 0, 0, time.UTC)); down || warning == "" {
		t.Fatalf("可用截止日当天仍应就绪：%q %v", warning, down)
	}
	if warning, down := stockCalendarWarning(ctx, repo, time.Date(2026, 12, 30, 1, 0, 0, 0, time.UTC)); !strings.Contains(warning, "已无法推算有效期") || !down {
		t.Fatalf("过了可用截止日应报未就绪（日历本身还没到期）：%q %v", warning, down)
	}
	if warning, down := stockCalendarWarning(ctx, repo, expired); !strings.Contains(warning, "无法求值") || !down {
		t.Fatalf("日历过期后应报未就绪：%q %v", warning, down)
	}
}

// 回放个数上限不合法时，提示里说清是 replays_max。
func TestLoadRejectsInvalidReplaysMax(t *testing.T) {
	_, err := Load(writeConfig(t, "database: ./strategy.sqlite\nretention:\n  replays_max: -1\n"))
	if err == nil || !strings.Contains(err.Error(), "replays_max") {
		t.Fatalf("replays_max 不合法应被拒绝并点名：%v", err)
	}
	cfg, err := Load(writeConfig(t, "database: ./strategy.sqlite\n"))
	if err != nil || cfg.Retention.ReplaysMax != 50 || cfg.EventBus.PublishTimeout != 10*time.Second {
		t.Fatalf("默认值不符：%+v %+v err=%v", cfg.Retention, cfg.EventBus, err)
	}
}
