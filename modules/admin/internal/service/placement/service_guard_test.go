package placement

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 主机组件（主机网关、主机采集器）随主机部署，不能单独启停；只能停用整台主机。
func TestSetPlacementStatusRejectsHostComponents(t *testing.T) {
	s, _ := newTestService(t)
	syncProduction(t, s)

	for _, component := range []string{"host-gateway", "host-agent"} {
		_, err := s.SetPlacementStatus(context.Background(), "storage", component, StatusDisabled)
		require.ErrorIs(t, err, ErrInvalid, component)
		assert.Equal(t, StatusEnabled, placementStatus(t, s, "storage", component), component)
	}
}

// 组件目录升级后库里残留的旧组件行，不能让其它主机的同步、启停和编译失败。
func TestUnknownComponentRowDoesNotBlockOtherHosts(t *testing.T) {
	s, db := newTestService(t)
	syncProduction(t, s)
	require.NoError(t, db.Create(&Placement{
		HostID: "storage", ComponentID: "gone-component", Status: StatusEnabled, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}).Error)

	ctx := context.Background()
	_, err := s.Compile(ctx)
	require.NoError(t, err)
	_, err = s.SyncHostPlacements(ctx, productionHosts["compute-1"], productionPlacements["compute-1"])
	require.NoError(t, err)
	_, err = s.SetHostStatus(ctx, "compute-1", StatusDisabled)
	require.NoError(t, err)

	// 对残留所在的主机重新同步时，残留行被清掉。
	_, err = s.SyncHostPlacements(ctx, productionHosts["storage"], productionPlacements["storage"])
	require.NoError(t, err)
	assert.Empty(t, mustList(t, s, "storage", "gone-component"))
}

func mustList(t *testing.T, s *Service, host, component string) []Placement {
	t.Helper()
	rows, err := s.ListPlacements(context.Background(), host, component)
	require.NoError(t, err)
	return rows
}
