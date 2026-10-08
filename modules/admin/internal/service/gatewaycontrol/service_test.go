package gatewaycontrol

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/internal/service/keys"
	"github.com/mooyang-code/moox/modules/admin/internal/service/placement"
	secretdao "github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/mooyang-code/moox/packages/gatewayroute"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/codec"
)

type fixture struct {
	control    *Service
	placements *placement.Service
	keys       *keys.Service
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "test-encryption-key-0123456789abcdef")
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "admin.db")+"?_pragma=foreign_keys(1)"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	placements := placement.NewService(db, nil)
	keyService := keys.NewService(secretdao.NewSecretDAO(db))
	ctx := context.Background()
	hosts := []placement.HostSpec{
		{HostID: "control", Address: "106.53.107.122"},
		{HostID: "storage", Address: "146.56.196.204", PrivateAddress: "10.206.0.5", Region: "ap-nanjing"},
	}
	components := map[string][]string{
		"control": {"console-proxy", "web-host", "admin", "eventbus", "monitor", "collector", "cloudnode", "factor-mgr", "strategy"},
		"storage": {"storage-primary", "storage-node", "storage-view", "access"},
	}
	for _, host := range hosts {
		_, err := placements.SyncHostPlacements(ctx, host, components[host.HostID])
		require.NoError(t, err)
	}
	for _, caller := range []string{"console", "collector", "strategy", "host-gateway@control", "host-gateway@storage", "access", "storage-view"} {
		_, _, err := keyService.Ensure(ctx, keys.CategoryCaller, caller)
		require.NoError(t, err)
	}
	return fixture{control: NewService(placements, keyService), placements: placements, keys: keyService}
}

func asCaller(caller string) context.Context {
	ctx, msg := codec.WithNewMessage(context.Background())
	msg.WithServerMetaData(codec.MetaData{gatewayroute.MetadataVerifiedCaller: []byte(caller)})
	return ctx
}

func TestPullSnapshotOnlyForSameHostGateway(t *testing.T) {
	f := newFixture(t)
	for _, caller := range []string{"", "console", "host-gateway@control", "host-gateway"} {
		rsp, err := f.control.PullSnapshot(asCaller(caller), &pb.PullSnapshotReq{HostId: "storage"})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_NO_PERMISSION, rsp.GetRetInfo().GetCode(), caller)
	}
	rsp, err := f.control.ReportStatus(asCaller("host-gateway@control"), &pb.ReportStatusReq{HostId: "storage", InstanceId: "x"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NO_PERMISSION, rsp.GetRetInfo().GetCode())
}

func TestPullSnapshotReturnsChangesByHash(t *testing.T) {
	f := newFixture(t)
	ctx := asCaller("host-gateway@storage")
	first, err := f.control.PullSnapshot(ctx, &pb.PullSnapshotReq{HostId: "storage"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, first.GetRetInfo().GetCode(), first.GetRetInfo().GetMsg())
	require.True(t, first.GetChanged())
	snapshot := first.GetSnapshot()
	require.NotEmpty(t, snapshot.GetHash())
	require.NotEmpty(t, snapshot.GetRoutes())
	require.NotEmpty(t, snapshot.GetDirectory().GetVersion())
	callers := map[string]bool{}
	for _, key := range snapshot.GetKeys() {
		callers[key.GetCaller()] = true
		require.NotEmpty(t, key.GetSecret())
	}
	require.True(t, callers["console"] && callers["strategy"] && callers["access"])
	require.False(t, callers["host-gateway@control"], "storage 不提供网关控制，不需要主机网关的校验密钥")

	unchanged, err := f.control.PullSnapshot(ctx, &pb.PullSnapshotReq{HostId: "storage", CurrentHash: snapshot.GetHash()})
	require.NoError(t, err)
	require.False(t, unchanged.GetChanged())
	require.Nil(t, unchanged.GetSnapshot())

	statuses, err := f.placements.ListGatewayStatus(context.Background())
	require.NoError(t, err)
	require.Equal(t, snapshot.GetHash(), statuses["storage"].ExpectedHash)

	// 新增一个调用方密钥：下一次快照即可下发，不需要重启网关。
	_, err = f.keys.Rotate(context.Background(), keys.CategoryCaller, "strategy")
	require.NoError(t, err)
	rotated, err := f.control.PullSnapshot(ctx, &pb.PullSnapshotReq{HostId: "storage", CurrentHash: snapshot.GetHash()})
	require.NoError(t, err)
	require.True(t, rotated.GetChanged())
	strategyKeys := 0
	for _, key := range rotated.GetSnapshot().GetKeys() {
		if key.GetCaller() == "strategy" {
			strategyKeys++
		}
	}
	require.Equal(t, 2, strategyKeys, "轮换期间新旧密钥同时下发")

	// 停用一条部署：快照变化，路由消失。
	_, err = f.placements.SetPlacementStatus(context.Background(), "storage", "storage-view", placement.StatusDisabled)
	require.NoError(t, err)
	disabled, err := f.control.PullSnapshot(ctx, &pb.PullSnapshotReq{HostId: "storage", CurrentHash: rotated.GetSnapshot().GetHash()})
	require.NoError(t, err)
	require.True(t, disabled.GetChanged())
	for _, route := range disabled.GetSnapshot().GetRoutes() {
		require.NotEqual(t, "trpc.moox.storage.DataView", route.GetServicePath())
	}
}

func TestControlSnapshotCarriesHostGatewayKeys(t *testing.T) {
	f := newFixture(t)
	rsp, err := f.control.PullSnapshot(asCaller("host-gateway@control"), &pb.PullSnapshotReq{HostId: "control"})
	require.NoError(t, err)
	require.True(t, rsp.GetChanged(), rsp.GetRetInfo().GetMsg())
	callers := map[string]bool{}
	for _, key := range rsp.GetSnapshot().GetKeys() {
		callers[key.GetCaller()] = true
	}
	require.True(t, callers["host-gateway@control"] && callers["host-gateway@storage"], "control 要校验所有主机网关的拉取")
	found := false
	for _, route := range rsp.GetSnapshot().GetRoutes() {
		if route.GetServicePath() == "trpc.moox.admin.GatewayControl" {
			found = true
			require.Equal(t, []string{"host-gateway@control", "host-gateway@storage"}, route.GetCallers())
		}
	}
	require.True(t, found)
}

func TestReportStatus(t *testing.T) {
	f := newFixture(t)
	rsp, err := f.control.ReportStatus(asCaller("host-gateway@storage"), &pb.ReportStatusReq{
		HostId: "storage", InstanceId: "inst-1", Version: "v1", AppliedHash: "h", RouteCount: 3,
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	statuses, err := f.placements.ListGatewayStatus(context.Background())
	require.NoError(t, err)
	require.Equal(t, "inst-1", statuses["storage"].InstanceID)
	require.Equal(t, int32(3), statuses["storage"].RouteCount)

	rsp, err = f.control.ReportStatus(asCaller("host-gateway@storage"), &pb.ReportStatusReq{HostId: "storage"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}
