package bootstrap

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/checks"
	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// 行情探针关闭时，启动时把探针域的旧检查全部删掉：在告警的推一条恢复；别的域不受影响。
func TestRetireMarketCanariesRemovesCanaryDomain(t *testing.T) {
	manager, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close() })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	repositories := manager.Repositories()

	seed := func(spaceID, checkID string, firing bool) {
		now := time.Now().UTC()
		row := domain.Check{
			SpaceID: spaceID, CheckID: checkID, Name: checkID, Severity: domain.SeverityCritical,
			Status: domain.CheckStatusDown, Reason: "旧失败", CheckedAt: now, FailureCount: 3,
			AlertStatus: domain.AlertStatusOK, CreatedAt: now,
		}
		if firing {
			row.AlertStatus, row.TriggeredAt, row.NotifiedAt = domain.AlertStatusFiring, &now, &now
		}
		require.NoError(t, repositories.Checks.SaveRound(t.Context(), []domain.Check{row}, nil, nil))
	}
	getRow := func(spaceID, checkID string) *domain.Check {
		row, err := repositories.Checks.Get(t.Context(), spaceID, checkID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		require.NoError(t, err)
		return row
	}

	canary := "market_canary:crypto/ds/BTC-USDT/1m/"
	tag := checks.SubjectTagCheckID("crypto", "binance_spot")
	seed("crypto", canary, true)
	seed("crypto", tag, false)
	seed("crypto", "dataset:storage:ds:1m", true)
	applier := &checks.Applier{Checks: repositories.Checks}
	require.NoError(t, retireMarketCanaries(t.Context(), &config.Config{MarketCanary: config.MarketCanaryConfig{Enabled: true}}, applier))
	require.NotNil(t, getRow("crypto", canary), "探针开启时不动")

	require.NoError(t, retireMarketCanaries(t.Context(), &config.Config{}, applier))
	require.Nil(t, getRow("crypto", canary), "不再产出的检查当轮删除")
	events, err := repositories.Alerts.ListRecentEvents(t.Context(), 100)
	require.NoError(t, err)
	var canaryEvents []domain.AlertEvent
	for _, event := range events {
		if event.CheckID == canary {
			canaryEvents = append(canaryEvents, event)
		}
	}
	require.NotEmpty(t, canaryEvents)
	require.Equal(t, domain.AlertEventResolved, canaryEvents[0].EventType)
	require.Contains(t, canaryEvents[0].Payload, checks.ReasonRemoved)
	require.Nil(t, getRow("crypto", tag))
	require.NotNil(t, getRow("crypto", "dataset:storage:ds:1m"), "不删业务域的检查")
}
