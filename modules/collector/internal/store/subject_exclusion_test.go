package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/require"
)

func TestSubjectExclusionsSetUpdateExpireAndClear(t *testing.T) {
	db, err := Open(&Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	repo := db.SubjectExclusions()
	ctx := context.Background()
	now := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	require.NoError(t, repo.Set(ctx, []SubjectExclusion{
		{SpaceID: "Crypto", SubjectID: "btc-usdt", MarketType: "SWAP", Reason: "公告下架", Caller: "moox-cli", CreatedAt: now},
		{SpaceID: "crypto", SubjectID: "ETH-USDT", Reason: "数据异常", Caller: "console", CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
	}))
	active, err := repo.Active(ctx, "crypto", now)
	require.NoError(t, err)
	require.Len(t, active, 2)
	require.Equal(t, SubjectExclusion{SpaceID: "crypto", SubjectID: "BTC-USDT", MarketType: "swap", Reason: "公告下架", Caller: "moox-cli", CreatedAt: now}, active[0])
	require.True(t, active[0].Excludes("swap"))
	require.False(t, active[0].Excludes("spot"))
	require.True(t, active[1].Excludes("spot"), "不填市场表示该空间所有市场")

	// 重复设置更新原因与到期时间。
	require.NoError(t, repo.Set(ctx, []SubjectExclusion{{SpaceID: "crypto", SubjectID: "ETH-USDT", Reason: "延长", Caller: "collector-operator", CreatedAt: now, ExpiresAt: now.Add(2 * time.Hour)}}))
	active, _ = repo.Active(ctx, "crypto", now.Add(90*time.Minute))
	require.Len(t, active, 2)
	require.Equal(t, "延长", active[1].Reason)
	require.Equal(t, "collector-operator", active[1].Caller)
	// 到期自动失效。
	active, _ = repo.Active(ctx, "crypto", now.Add(2*time.Hour))
	require.Len(t, active, 1)

	removed, err := repo.Clear(ctx, "crypto", []string{"btc-usdt"}, "spot")
	require.NoError(t, err)
	require.Zero(t, removed)
	removed, err = repo.Clear(ctx, "crypto", []string{"btc-usdt", "eth-usdt"}, "")
	require.NoError(t, err)
	require.EqualValues(t, 2, removed)
	active, _ = repo.Active(ctx, "crypto", now)
	require.Empty(t, active)
	require.Error(t, repo.Set(ctx, []SubjectExclusion{{SpaceID: "crypto"}}))
}
