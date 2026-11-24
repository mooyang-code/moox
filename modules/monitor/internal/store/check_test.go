package store

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/stretchr/testify/require"
)

// 当前告警按空间过滤（本空间加系统空间），在 SQL 里先过滤再限条数；SaveRound 更新时保留首次出现的时间。
func TestListFiringAndSaveRound(t *testing.T) {
	repos := openNotificationTestDB(t).Repositories()
	ctx := t.Context()
	first := time.Date(2026, 11, 24, 3, 0, 0, 0, time.UTC)
	row := func(space, id, alert string) domain.Check {
		return domain.Check{SpaceID: space, CheckID: id, Name: id, Severity: domain.SeverityCritical, Status: domain.CheckStatusDown,
			CheckedAt: first, AlertStatus: alert, TriggeredAt: &first, CreatedAt: first}
	}
	require.NoError(t, repos.Checks.SaveRound(ctx, []domain.Check{
		row("crypto", "a", domain.AlertStatusFiring), row("mooxsys", "b", domain.AlertStatusFiring),
		row("stockcn", "c", domain.AlertStatusFiring), row("crypto", "d", domain.AlertStatusOK),
	}, nil, nil))
	firing, err := repos.Checks.ListFiring(ctx, "crypto", 10)
	require.NoError(t, err)
	require.Len(t, firing, 2)
	all, err := repos.Checks.ListFiring(ctx, "", 10)
	require.NoError(t, err)
	require.Len(t, all, 3)

	updated := row("crypto", "a", domain.AlertStatusOK)
	updated.CreatedAt = first.Add(time.Hour)
	require.NoError(t, repos.Checks.SaveRound(ctx, []domain.Check{updated}, []CheckKey{{SpaceID: "crypto", CheckID: "d"}}, nil))
	got, err := repos.Checks.Get(ctx, "crypto", "a")
	require.NoError(t, err)
	require.False(t, got.Firing())
	require.True(t, got.CreatedAt.Equal(first), "首次出现的时间不被覆盖")
	count, err := repos.Checks.Count(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 3, count)
}
