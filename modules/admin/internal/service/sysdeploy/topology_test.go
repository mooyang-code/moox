package sysdeploy

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func topologySpecs() []HostSpec {
	return []HostSpec{
		{HostID: "control", Address: "control.example.test", Components: []string{"admin", "console-proxy", "web-host", "collector"}},
		{HostID: "storage", Address: "storage.example.test", Components: []string{"storage-primary", "storage-node", "storage-view"}},
		{HostID: "compute-1", Address: "compute.example.test", Components: []string{"trade", "access"}},
	}
}

func topologyDAO(t *testing.T) *TopologyDAO {
	t.Helper()
	db := setupTopologyTestDB(t)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	dao, err := NewTopologyDAO(db, "control")
	require.NoError(t, err)
	require.NoError(t, dao.SyncHosts(context.Background(), topologySpecs()))
	return dao
}

func TestTopologySyncPreservesStateAndRebuildsDirectory(t *testing.T) {
	dao, ctx := topologyDAO(t), context.Background()
	require.NoError(t, dao.SetPlacementStatus(ctx, "control", "collector", servicecatalog.Disabled))
	require.NoError(t, dao.SetHostStatus(ctx, "compute-1", servicecatalog.Disabled))
	control := topologySpecs()[0]
	control.Description = "updated metadata"
	control.Components = append(control.Components, "monitor")
	require.NoError(t, dao.SyncHostPlacements(ctx, control))
	compute := topologySpecs()[2]
	compute.Components = []string{"access"}
	require.NoError(t, dao.SyncHostPlacements(ctx, compute))
	hosts, placements, err := dao.Read(ctx)
	require.NoError(t, err)
	for _, h := range hosts {
		if h.HostID == "compute-1" {
			require.Equal(t, servicecatalog.Disabled, h.Status)
		}
	}
	for _, p := range placements {
		if p.HostID == "control" && p.ComponentID == "collector" {
			require.Equal(t, servicecatalog.Disabled, p.Status)
		}
		require.False(t, p.HostID == "compute-1" && p.ComponentID == "trade", "components removed from the full list are deleted")
	}
	compiled, err := dao.Compile(ctx, "control")
	require.NoError(t, err)
	require.NoError(t, compiled.Directory.Validate())
	require.NotContains(t, compiled.Directory.Hosts, "compute-1")
	require.NotContains(t, compiled.Directory.Services, "trpc.moox.collector.CollectMgr")
	// The same offline DAO is used to recover a disabled placement.
	require.NoError(t, dao.SetPlacementStatus(ctx, "control", "collector", servicecatalog.Enabled))
	recovered, err := dao.Compile(ctx, "control")
	require.NoError(t, err)
	require.NotEqual(t, compiled.Directory.Version, recovered.Directory.Version)
	require.NoError(t, dao.SyncHosts(ctx, []HostSpec{control, compute}))
}

func TestTopologyRejectsInvalidSynchronizationWithoutChangingDatabase(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func([]HostSpec) []HostSpec
	}{
		{"unknown component", func(s []HostSpec) []HostSpec { s[0].Components = append(s[0].Components, "unknown"); return s }},
		{"protected removal", func(s []HostSpec) []HostSpec { s[0].Components = []string{"collector"}; return s }},
		{"control scope", func(s []HostSpec) []HostSpec { s[1].Components = append(s[1].Components, "console-proxy"); return s }},
		{"replica limit", func(s []HostSpec) []HostSpec { s[2].Components = append(s[2].Components, "collector"); return s }},
		{"duplicate component", func(s []HostSpec) []HostSpec { s[2].Components = []string{"access", "access"}; return s }},
		{"automatic component", func(s []HostSpec) []HostSpec { s[2].Components = []string{"host-gateway"}; return s }},
		{"duplicate address", func(s []HostSpec) []HostSpec { s[2].Address = s[0].Address; return s }},
		{"region too long", func(s []HostSpec) []HostSpec { s[2].Region = strings.Repeat("r", 129); return s }},
		{"duplicate host", func(s []HostSpec) []HostSpec { return append(s, s[0]) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dao, ctx := topologyDAO(t), context.Background()
			beforeHosts, beforePlacements, err := dao.Read(ctx)
			require.NoError(t, err)
			specs := topologySpecs()
			specs[0].Description = "must roll back"
			require.Error(t, dao.SyncHosts(ctx, tc.change(specs)))
			afterHosts, afterPlacements, err := dao.Read(ctx)
			require.NoError(t, err)
			require.Equal(t, beforeHosts, afterHosts)
			require.Equal(t, beforePlacements, afterPlacements)
		})
	}
}

func TestTopologyRollsBackHostWhenPlacementWriteFails(t *testing.T) {
	dao, ctx := topologyDAO(t), context.Background()
	beforeHosts, beforePlacements, err := dao.Read(ctx)
	require.NoError(t, err)
	require.NoError(t, dao.db.Exec(`CREATE TRIGGER fail_new_placement BEFORE INSERT ON t_placements
WHEN NEW.c_host_id = 'new-host' BEGIN SELECT RAISE(ABORT, 'simulated disk failure'); END;`).Error)
	specs := []HostSpec{topologySpecs()[0], {HostID: "new-host", Address: "new.example.test"}}
	specs[0].Description = "must also roll back"
	require.Error(t, dao.SyncHosts(ctx, specs))
	afterHosts, afterPlacements, err := dao.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, beforeHosts, afterHosts)
	require.Equal(t, beforePlacements, afterPlacements)
}

func TestTopologyProtectsControlAndDeletesOnlyEmptyHosts(t *testing.T) {
	dao, ctx := topologyDAO(t), context.Background()
	require.Error(t, dao.SetHostStatus(ctx, "control", servicecatalog.Disabled))
	require.Error(t, dao.SetPlacementStatus(ctx, "control", "admin", servicecatalog.Disabled))
	require.Error(t, dao.SetPlacementStatus(ctx, "storage", "host-gateway", servicecatalog.Disabled))
	require.Error(t, dao.DeleteHost(ctx, "control"))
	require.Error(t, dao.DeleteHost(ctx, "compute-1"))
	require.ErrorIs(t, dao.SetHostStatus(ctx, "unknown", servicecatalog.Enabled), gorm.ErrRecordNotFound)
	require.ErrorIs(t, dao.SetPlacementStatus(ctx, "storage", "collector", servicecatalog.Enabled), gorm.ErrRecordNotFound)
	compute := topologySpecs()[2]
	compute.Components = nil
	require.NoError(t, dao.SyncHostPlacements(ctx, compute))
	require.NoError(t, dao.db.Create(&HostGatewayStatus{HostID: "compute-1", InstanceID: "previous-process"}).Error)
	require.NoError(t, dao.DeleteHost(ctx, "compute-1"))
	hosts, placements, err := dao.Read(ctx)
	require.NoError(t, err)
	require.False(t, slices.ContainsFunc(hosts, func(h HostRecord) bool { return h.HostID == "compute-1" }))
	require.False(t, slices.ContainsFunc(placements, func(p PlacementRecord) bool { return p.HostID == "compute-1" }))
	var count int64
	require.NoError(t, dao.db.Model(&HostGatewayStatus{}).Where("c_host_id = ?", "compute-1").Count(&count).Error)
	require.Zero(t, count)
}

func TestConcurrentTopologyWritersCannotEnableTwoSingleReplicas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin.db")
	db, err := gorm.Open(sqlite.Open(path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	first, err := NewTopologyDAO(db, "control")
	require.NoError(t, err)
	second, err := NewTopologyDAO(db, "control")
	require.NoError(t, err)
	specs := topologySpecs()
	specs[0].Components = []string{"admin", "console-proxy", "web-host"}
	require.NoError(t, first.SyncHosts(context.Background(), specs))
	start, results := make(chan struct{}), make(chan error, 2)
	for _, item := range []struct {
		dao  *TopologyDAO
		spec HostSpec
	}{{first, specs[1]}, {second, specs[2]}} {
		go func() {
			<-start
			item.spec.Components = append(slices.Clone(item.spec.Components), "collector")
			results <- item.dao.SyncHostPlacements(context.Background(), item.spec)
		}()
	}
	close(start)
	successes := 0
	for i := 0; i < 2; i++ {
		if err := <-results; err == nil {
			successes++
		} else {
			require.ErrorContains(t, err, "single component")
		}
	}
	require.Equal(t, 1, successes)
	compiled, err := first.Compile(context.Background(), "control")
	require.NoError(t, err)
	require.NoError(t, compiled.Directory.Validate())
}
