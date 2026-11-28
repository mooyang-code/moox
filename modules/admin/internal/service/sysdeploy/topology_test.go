package sysdeploy

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func topologySpecs() []*pb.SyncTopologyHost {
	return []*pb.SyncTopologyHost{
		{HostId: "control", Enabled: true, Address: "control.example.test", ComponentIds: []string{"admin", "collector"}},
		{HostId: "storage", Enabled: true, Address: "storage.example.test", ComponentIds: []string{"storage-primary", "storage-view"}},
		{HostId: "overseas", Enabled: true, Address: "compute.example.test", ComponentIds: []string{"trade", "egress-proxy"}},
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
	_, err = dao.ReplaceTopology(context.Background(), &pb.SyncTopologyReq{Hosts: topologySpecs()})
	require.NoError(t, err)
	return dao
}

func replace(t *testing.T, dao *TopologyDAO, specs []*pb.SyncTopologyHost) []string {
	t.Helper()
	touched, err := dao.ReplaceTopology(context.Background(), &pb.SyncTopologyReq{Hosts: specs})
	require.NoError(t, err)
	return touched
}

// 状态取自声明：停用的主机与部署照写，声明里去掉的组件删除，恢复后换快照哈希。
func TestReplaceTopologyTakesStatusFromDeclaration(t *testing.T) {
	dao, ctx := topologyDAO(t), context.Background()
	specs := topologySpecs()
	specs[0].Description = "updated metadata"
	specs[0].ComponentIds = append(specs[0].ComponentIds, "monitor")
	specs[0].DisabledComponentIds = []string{"collector"}
	specs[2].Enabled = false
	specs[2].ComponentIds = []string{"egress-proxy"}
	replace(t, dao, specs)
	hosts, deployments, err := dao.Read(ctx)
	require.NoError(t, err)
	for _, h := range hosts {
		want := servicecatalog.Enabled
		if h.HostID == "overseas" {
			want = servicecatalog.Disabled
		}
		require.Equal(t, want, h.Status, h.HostID)
	}
	for _, p := range deployments {
		want := servicecatalog.Enabled
		if p.HostID == "control" && p.ComponentID == "collector" {
			want = servicecatalog.Disabled
		}
		require.Equal(t, want, p.Status, p.HostID+"/"+p.ComponentID)
		require.False(t, p.HostID == "overseas" && p.ComponentID == "trade", "components removed from the full list are deleted")
	}
	compiled, err := dao.Compile(ctx, "control")
	require.NoError(t, err)
	require.NoError(t, compiled.Directory.Validate())
	require.NotContains(t, compiled.Directory.Hosts, "overseas")
	require.NotContains(t, compiled.Directory.Services, "trpc.moox.collector.CollectMgr")
	before, err := dao.Snapshot(ctx)
	require.NoError(t, err)
	specs[0].DisabledComponentIds = nil
	replace(t, dao, specs)
	recovered, err := dao.Compile(ctx, "control")
	require.NoError(t, err)
	require.Equal(t, []string{"control"}, recovered.Directory.Services["trpc.moox.collector.CollectMgr"])
	after, err := dao.Snapshot(ctx)
	require.NoError(t, err)
	require.NotEqual(t, before.Hash, after.Hash, "恢复部署要换快照哈希，网关与客户端据此重新取")
}

// 签名公钥与 SSH 指纹不是声明的内容：覆盖后保留；只有连接信息变了或被删掉的主机算“动过”。
func TestReplaceTopologyKeepsKeysAndFingerprints(t *testing.T) {
	dao, ctx := topologyDAO(t), context.Background()
	require.NoError(t, dao.db.Model(&HostRecord{}).Where("c_host_id = ?", "storage").Updates(map[string]any{"c_sign_public_key": "storage-key", "c_ssh_fingerprint": "SHA256:fixture"}).Error)
	specs := topologySpecs()
	require.Empty(t, replace(t, dao, specs), "内容不变时没有主机要关会话")
	specs[1].Description = "only metadata"
	require.Empty(t, replace(t, dao, specs))
	specs[1].Address = "storage-2.example.test"
	require.Equal(t, []string{"storage"}, replace(t, dao, specs))
	hosts, _, err := dao.Read(ctx)
	require.NoError(t, err)
	i := slices.IndexFunc(hosts, func(h HostRecord) bool { return h.HostID == "storage" })
	require.Equal(t, "storage-key", hosts[i].SignPublicKey)
	require.Equal(t, "SHA256:fixture", hosts[i].SSHFingerprint)
	require.Equal(t, "storage-2.example.test", hosts[i].Address)
}

func TestTopologyRejectsInvalidSynchronizationWithoutChangingDatabase(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func([]*pb.SyncTopologyHost) []*pb.SyncTopologyHost
	}{
		{"unknown component", func(s []*pb.SyncTopologyHost) []*pb.SyncTopologyHost {
			s[0].ComponentIds = append(s[0].ComponentIds, "unknown")
			return s
		}},
		{"protected removal", func(s []*pb.SyncTopologyHost) []*pb.SyncTopologyHost {
			s[0].ComponentIds = []string{"collector"}
			return s
		}},
		{"control scope", func(s []*pb.SyncTopologyHost) []*pb.SyncTopologyHost {
			s[1].ComponentIds = append(s[1].ComponentIds, "admin")
			return s
		}},
		{"replica limit", func(s []*pb.SyncTopologyHost) []*pb.SyncTopologyHost {
			s[2].ComponentIds = append(s[2].ComponentIds, "collector")
			return s
		}},
		{"duplicate component", func(s []*pb.SyncTopologyHost) []*pb.SyncTopologyHost {
			s[2].ComponentIds = []string{"egress-proxy", "egress-proxy"}
			return s
		}},
		{"automatic component", func(s []*pb.SyncTopologyHost) []*pb.SyncTopologyHost {
			s[2].ComponentIds = []string{"host-gateway"}
			return s
		}},
		{"duplicate address", func(s []*pb.SyncTopologyHost) []*pb.SyncTopologyHost { s[2].Address = s[0].Address; return s }},
		{"region too long", func(s []*pb.SyncTopologyHost) []*pb.SyncTopologyHost {
			s[2].Region = strings.Repeat("r", 129)
			return s
		}},
		{"duplicate host", func(s []*pb.SyncTopologyHost) []*pb.SyncTopologyHost { return append(s, s[0]) }},
		{"disabled control", func(s []*pb.SyncTopologyHost) []*pb.SyncTopologyHost { s[0].Enabled = false; return s }},
		{"disabled protected", func(s []*pb.SyncTopologyHost) []*pb.SyncTopologyHost {
			s[0].DisabledComponentIds = []string{"admin"}
			return s
		}},
		{"disabled not deployed", func(s []*pb.SyncTopologyHost) []*pb.SyncTopologyHost {
			s[1].DisabledComponentIds = []string{"collector"}
			return s
		}},
		{"control removed", func(s []*pb.SyncTopologyHost) []*pb.SyncTopologyHost { return s[1:] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dao, ctx := topologyDAO(t), context.Background()
			beforeHosts, beforePlacements, err := dao.Read(ctx)
			require.NoError(t, err)
			specs := topologySpecs()
			specs[0].Description = "must roll back"
			_, err = dao.ReplaceTopology(ctx, &pb.SyncTopologyReq{Hosts: tc.change(specs)})
			require.Error(t, err)
			afterHosts, afterDeployments, err := dao.Read(ctx)
			require.NoError(t, err)
			require.Equal(t, beforeHosts, afterHosts)
			require.Equal(t, beforePlacements, afterDeployments)
		})
	}
}

func TestTopologyRollsBackHostWhenDeploymentWriteFails(t *testing.T) {
	dao, ctx := topologyDAO(t), context.Background()
	beforeHosts, beforePlacements, err := dao.Read(ctx)
	require.NoError(t, err)
	require.NoError(t, dao.db.Exec(`CREATE TRIGGER fail_new_deployment BEFORE INSERT ON t_deployments
WHEN NEW.c_host_id = 'new-host' BEGIN SELECT RAISE(ABORT, 'simulated disk failure'); END;`).Error)
	specs := append(topologySpecs(), &pb.SyncTopologyHost{HostId: "new-host", Enabled: true, Address: "new.example.test"})
	specs[0].Description = "must also roll back"
	_, err = dao.ReplaceTopology(ctx, &pb.SyncTopologyReq{Hosts: specs})
	require.Error(t, err)
	afterHosts, afterDeployments, err := dao.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, beforeHosts, afterHosts)
	require.Equal(t, beforePlacements, afterDeployments)
}

// 声明里没有的主机连同它的部署与控制台 SSH 凭据一起删掉；即使内存里还有它的旧网关状态，GetHostRoutes 也不会暴露。
func TestReplaceTopologyDeletesUndeclaredHosts(t *testing.T) {
	dao, ctx := topologyDAO(t), context.Background()
	statuses := NewGatewayStatusStore()
	statuses.Record("overseas", "previous", time.Now(), nil)
	svc := &ServiceImpl{topology: dao, statuses: statuses, adminNodeID: "control"}
	require.Equal(t, []string{"overseas"}, replace(t, dao, topologySpecs()[:2]))
	hosts, deployments, err := dao.Read(ctx)
	require.NoError(t, err)
	require.False(t, slices.ContainsFunc(hosts, func(h HostRecord) bool { return h.HostID == "overseas" }))
	require.False(t, slices.ContainsFunc(deployments, func(p DeploymentRecord) bool { return p.HostID == "overseas" }))
	routes, err := svc.GetHostRoutes(ctx, &pb.GetHostRoutesReq{HostId: "overseas"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NOT_FOUND, routes.GetRetInfo().GetCode())
}

func TestConcurrentTopologyWritersSerialize(t *testing.T) {
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
	initial := topologySpecs()
	initial[0].ComponentIds = []string{"admin"}
	_, err = first.ReplaceTopology(context.Background(), &pb.SyncTopologyReq{Hosts: initial})
	require.NoError(t, err)
	start, results := make(chan struct{}), make(chan error, 2)
	for _, item := range []struct {
		dao  *TopologyDAO
		host int
	}{{first, 1}, {second, 2}} {
		go func() {
			<-start
			declared := topologySpecs()
			declared[0].ComponentIds = []string{"admin"}
			declared[item.host].ComponentIds = append(declared[item.host].ComponentIds, "collector")
			_, err := item.dao.ReplaceTopology(context.Background(), &pb.SyncTopologyReq{Hosts: declared})
			results <- err
		}()
	}
	close(start)
	// 写锁让两次整份覆盖串行：两次都成功，后写的那份胜出，单副本组件仍然只有一处。
	for i := 0; i < 2; i++ {
		require.NoError(t, <-results)
	}
	_, deployments, err := first.Read(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, len(slices.DeleteFunc(deployments, func(p DeploymentRecord) bool { return p.ComponentID != "collector" })))
	compiled, err := first.Compile(context.Background(), "control")
	require.NoError(t, err)
	require.NoError(t, compiled.Directory.Validate())
}

// 出站主机登记后进入拓扑与目录；未知取值与出站 control 都拒绝。
func TestTopologyOutboundHostReachability(t *testing.T) {
	dao, ctx := topologyDAO(t), context.Background()
	home := &pb.SyncTopologyHost{HostId: "home", Enabled: true, Address: "192.168.0.103", Reachability: "outbound"}
	replace(t, dao, append(topologySpecs(), home))
	hosts, _, err := dao.Read(ctx)
	require.NoError(t, err)
	for _, h := range hosts {
		want := "direct"
		if h.HostID == "home" {
			want = "outbound"
		}
		require.Equal(t, want, h.Reachability, h.HostID)
	}
	compiled, err := dao.Compile(ctx, "control")
	require.NoError(t, err)
	require.Equal(t, "control", compiled.Directory.Hosts["home"].Relay)
	require.Len(t, compiled.Relays, 1)

	home.Reachability = "inbound"
	_, err = dao.ReplaceTopology(ctx, &pb.SyncTopologyReq{Hosts: append(topologySpecs(), home)})
	require.ErrorIs(t, err, ErrInvalidTopology)
	specs := topologySpecs()
	specs[0].Reachability = "outbound"
	_, err = dao.ReplaceTopology(ctx, &pb.SyncTopologyReq{Hosts: specs})
	require.ErrorIs(t, err, ErrInvalidTopology)
}

// syncHost 只改一台主机的声明（按启用处理），其余主机沿用 topologySpecs()，整份覆盖。
func syncHost(dao *TopologyDAO, spec *pb.SyncTopologyHost) error {
	cloned := proto.Clone(spec).(*pb.SyncTopologyHost)
	cloned.Enabled = true
	specs := topologySpecs()
	if i := slices.IndexFunc(specs, func(s *pb.SyncTopologyHost) bool { return s.GetHostId() == cloned.GetHostId() }); i >= 0 {
		specs[i] = cloned
	} else {
		specs = append(specs, cloned)
	}
	_, err := dao.ReplaceTopology(context.Background(), &pb.SyncTopologyReq{Hosts: specs})
	return err
}
