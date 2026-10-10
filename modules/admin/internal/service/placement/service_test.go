package placement

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newTestService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "admin.db")+"?_pragma=foreign_keys(1)"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return NewService(db, nil), db
}

var productionHosts = map[string]HostSpec{
	"control":   {HostID: "control", Address: "106.53.107.122"},
	"storage":   {HostID: "storage", Address: "146.56.196.204", PrivateAddress: "10.206.0.5", Region: "ap-nanjing"},
	"compute-1": {HostID: "compute-1", Address: "43.132.204.177", PrivateAddress: "172.19.32.13", Region: "ap-hongkong"},
}

var productionPlacements = map[string][]string{
	"control":   {"console-proxy", "web-host", "admin", "eventbus", "monitor", "collector", "cloudnode", "factor-mgr", "strategy"},
	"storage":   {"storage-primary", "storage-node", "storage-view", "access"},
	"compute-1": {"trade", "access", "egress-proxy"},
}

func syncProduction(t *testing.T, s *Service) {
	t.Helper()
	for _, host := range []string{"control", "storage", "compute-1"} {
		_, err := s.SyncHostPlacements(context.Background(), productionHosts[host], productionPlacements[host])
		require.NoError(t, err, host)
	}
}

func placementStatus(t *testing.T, s *Service, host, component string) string {
	t.Helper()
	rows, err := s.ListPlacements(context.Background(), host, component)
	require.NoError(t, err)
	if len(rows) == 0 {
		return ""
	}
	return rows[0].Status
}

func snapshotState(t *testing.T, db *gorm.DB) string {
	t.Helper()
	var hosts []Host
	var placements []Placement
	require.NoError(t, db.Order("c_host_id").Find(&hosts).Error)
	require.NoError(t, db.Order("c_host_id, c_component_id").Find(&placements).Error)
	var out []string
	for _, host := range hosts {
		out = append(out, "host:"+host.HostID+":"+host.Address+":"+host.Status)
	}
	for _, placement := range placements {
		out = append(out, placement.HostID+"/"+placement.ComponentID+":"+placement.Status)
	}
	return strings.Join(out, "\n")
}

func TestSyncHostPlacementsAddsRemovesAndKeepsStatus(t *testing.T) {
	s, _ := newTestService(t)
	ctx := context.Background()
	result, err := s.SyncHostPlacements(ctx, productionHosts["control"], productionPlacements["control"])
	require.NoError(t, err)
	require.True(t, result.HostCreated)
	require.Contains(t, result.Added, "admin")
	require.Contains(t, result.Added, "host-gateway", "主机组件应当自动补上")
	require.Contains(t, result.Added, "host-agent")

	_, err = s.SetPlacementStatus(ctx, "control", "strategy", StatusDisabled)
	require.NoError(t, err)

	// 从部署表中去掉 cloudnode，保留其余组件。
	without := []string{}
	for _, id := range productionPlacements["control"] {
		if id != "cloudnode" {
			without = append(without, id)
		}
	}
	result, err = s.SyncHostPlacements(ctx, productionHosts["control"], without)
	require.NoError(t, err)
	require.False(t, result.HostCreated)
	require.Equal(t, []string{"cloudnode"}, result.Removed)
	require.Empty(t, result.Added)
	require.Equal(t, "", placementStatus(t, s, "control", "cloudnode"))
	require.Equal(t, StatusDisabled, placementStatus(t, s, "control", "strategy"), "已有部署的启用状态保持不变")
	require.Equal(t, StatusEnabled, placementStatus(t, s, "control", "host-gateway"))
}

func TestSyncHostPlacementsRejectsWholeChange(t *testing.T) {
	s, db := newTestService(t)
	syncProduction(t, s)
	before := snapshotState(t, db)
	ctx := context.Background()
	cases := []struct {
		name       string
		host       HostSpec
		components []string
		want       string
	}{
		{"副本数超限", productionHosts["storage"], append(append([]string{}, productionPlacements["storage"]...), "collector"), "只允许一份"},
		{"移除受保护组件", productionHosts["control"], []string{"console-proxy", "web-host", "eventbus", "monitor"}, "受保护"},
		{"control 组件放到其他主机", productionHosts["storage"], append(append([]string{}, productionPlacements["storage"]...), "monitor"), "只能部署在 control"},
		{"未知组件", productionHosts["storage"], []string{"unknown-component"}, "不在组件目录中"},
		{"写入主机组件", productionHosts["storage"], []string{"host-gateway"}, "自动部署"},
		{"主机地址重复", HostSpec{HostID: "compute-2", Address: "146.56.196.204"}, []string{}, "地址相同"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.SyncHostPlacements(ctx, tc.host, tc.components)
			require.Error(t, err)
			require.True(t, errors.Is(err, ErrInvalid), err)
			require.Contains(t, err.Error(), tc.want)
			require.Equal(t, before, snapshotState(t, db), "整体拒绝时数据库不变")
		})
	}
}

func TestProtectedObjectsCannotBeDisabled(t *testing.T) {
	s, _ := newTestService(t)
	syncProduction(t, s)
	ctx := context.Background()
	for _, component := range []string{"admin", "console-proxy", "web-host"} {
		_, err := s.SetPlacementStatus(ctx, "control", component, StatusDisabled)
		require.ErrorIs(t, err, ErrInvalid, component)
	}
	_, err := s.SetPlacementStatus(ctx, "storage", "host-gateway", StatusDisabled)
	require.ErrorIs(t, err, ErrInvalid)
	_, err = s.SetHostStatus(ctx, "control", StatusDisabled)
	require.ErrorIs(t, err, ErrInvalid)

	host, err := s.SetHostStatus(ctx, "compute-1", StatusDisabled)
	require.NoError(t, err)
	require.Equal(t, StatusDisabled, host.Status)
	placement, err := s.SetPlacementStatus(ctx, "storage", "storage-view", StatusDisabled)
	require.NoError(t, err)
	require.Equal(t, StatusDisabled, placement.Status)
	// 恢复
	_, err = s.SetPlacementStatus(ctx, "storage", "storage-view", StatusEnabled)
	require.NoError(t, err)
	_, err = s.SetHostStatus(ctx, "compute-1", StatusEnabled)
	require.NoError(t, err)
	compiled, err := s.Compile(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"storage"}, compiled.Directory.ServiceHostIDs("trpc.moox.storage.DataView"))
}

func TestSetPlacementStatusUnknownPlacement(t *testing.T) {
	s, _ := newTestService(t)
	syncProduction(t, s)
	_, err := s.SetPlacementStatus(context.Background(), "storage", "collector", StatusDisabled)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = s.SetPlacementStatus(context.Background(), "nowhere", "collector", StatusDisabled)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = s.SetPlacementStatus(context.Background(), "storage", "storage-view", "paused")
	require.ErrorIs(t, err, ErrInvalid)
}

func TestDeleteHost(t *testing.T) {
	s, _ := newTestService(t)
	syncProduction(t, s)
	ctx := context.Background()
	require.ErrorIs(t, s.DeleteHost(ctx, "control"), ErrInvalid)
	err := s.DeleteHost(ctx, "compute-1")
	require.ErrorIs(t, err, ErrInvalid)
	require.Contains(t, err.Error(), "trade")

	_, err = s.SyncHostPlacements(ctx, productionHosts["compute-1"], nil)
	require.NoError(t, err)
	_, err = s.RecordGatewayReport(ctx, GatewayReport{HostID: "compute-1", InstanceID: "a"})
	require.NoError(t, err)
	require.NoError(t, s.DeleteHost(ctx, "compute-1"))
	_, err = s.GetHost(ctx, "compute-1")
	require.ErrorIs(t, err, ErrNotFound)
	statuses, err := s.ListGatewayStatus(ctx)
	require.NoError(t, err)
	require.NotContains(t, statuses, "compute-1")
}

func TestGatewayReplacementAndConflict(t *testing.T) {
	s, _ := newTestService(t)
	syncProduction(t, s)
	ctx := context.Background()
	now := time.Date(2026, 10, 8, 8, 0, 0, 0, time.UTC)
	s.WithClock(func() time.Time { return now })
	report := func(instance string) GatewayStatus {
		row, err := s.RecordGatewayReport(ctx, GatewayReport{HostID: "storage", InstanceID: instance, AppliedHash: "h1"})
		require.NoError(t, err)
		return row
	}

	row := report("A")
	require.Equal(t, GatewayOnline, row.State(now))
	now = now.Add(15 * time.Second)
	row = report("B")
	require.Equal(t, "A", row.PreviousInstanceID, "重启（替换）")
	require.Equal(t, GatewayOnline, row.State(now), "一次替换不算冲突")
	require.Nil(t, row.ConflictSeenAt)

	now = now.Add(15 * time.Second)
	row = report("A")
	require.Equal(t, GatewayConflict, row.State(now), "A→B→A 交替记为冲突")
	require.Equal(t, "B", row.ConflictInstanceID)

	now = now.Add(15 * time.Second)
	row = report("B")
	require.Equal(t, GatewayConflict, row.State(now))

	// 之后只剩 B 在上报：5 分钟内不再交替就清除。
	for i := 0; i < 21; i++ {
		now = now.Add(15 * time.Second)
		row = report("B")
	}
	require.Equal(t, GatewayOnline, row.State(now))
	require.Nil(t, row.ConflictSeenAt)

	// 替换 6 分钟后旧实例才出现：视为又一次替换，不是冲突。
	now = now.Add(6 * time.Minute)
	row = report("C")
	now = now.Add(6 * time.Minute)
	row = report("B")
	require.Equal(t, GatewayOnline, row.State(now))

	now = now.Add(3 * time.Minute)
	require.Equal(t, GatewayOffline, row.State(now), "超过 2 分钟没有心跳视为失联")
}

func TestGatewayMismatchSince(t *testing.T) {
	s, _ := newTestService(t)
	syncProduction(t, s)
	ctx := context.Background()
	now := time.Date(2026, 10, 8, 8, 0, 0, 0, time.UTC)
	s.WithClock(func() time.Time { return now })
	require.NoError(t, s.SetExpectedHash(ctx, "storage", "h2"))
	row, err := s.RecordGatewayReport(ctx, GatewayReport{HostID: "storage", InstanceID: "A", AppliedHash: "h1"})
	require.NoError(t, err)
	require.NotNil(t, row.MismatchSince)
	since := *row.MismatchSince
	now = now.Add(time.Minute)
	row, err = s.RecordGatewayReport(ctx, GatewayReport{HostID: "storage", InstanceID: "A", AppliedHash: "h1"})
	require.NoError(t, err)
	require.True(t, row.MismatchSince.Equal(since), "不一致的起始时间保持不变")
	row, err = s.RecordGatewayReport(ctx, GatewayReport{HostID: "storage", InstanceID: "A", AppliedHash: "h2"})
	require.NoError(t, err)
	require.Nil(t, row.MismatchSince)
	require.True(t, row.Synced())

	_, err = s.RecordGatewayReport(ctx, GatewayReport{HostID: "unknown", InstanceID: "A"})
	require.ErrorIs(t, err, ErrNotFound)
}
