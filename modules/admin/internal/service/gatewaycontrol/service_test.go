package gatewaycontrol

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/glebarez/sqlite"
	authdao "github.com/mooyang-code/moox/modules/admin/internal/service/auth/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/keys"
	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const fixtureMaster = "fixture-only-gateway-control-master-0123456789"

type controlFixture struct {
	service         *Service
	db              *gorm.DB
	path, cachePath string
	badger          *badger.DB
	keys            *keys.Store
	topology        *sysdeploy.TopologyDAO
	clock           atomic.Int64
}

func openControlDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"), &gorm.Config{})
	require.NoError(t, err)
	connection, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	return db
}

func newControlFixture(t *testing.T) *controlFixture {
	t.Helper()
	dir := t.TempDir()
	f := &controlFixture{path: filepath.Join(dir, "admin.db"), cachePath: filepath.Join(dir, "badger")}
	f.db = openControlDB(t, f.path)
	require.NoError(t, f.db.Exec(schema.AdminSQL()).Error)
	var err error
	f.topology, err = sysdeploy.NewTopologyDAO(f.db, "control")
	require.NoError(t, err)
	require.NoError(t, f.topology.SyncHosts(context.Background(), []sysdeploy.HostSpec{
		{HostID: "control", Address: "control.example.test", Components: []string{"admin", "console-proxy", "web-host", "collector"}},
		{HostID: "storage", Address: "storage.example.test", Components: []string{"storage-primary", "storage-node", "storage-view"}},
		{HostID: "compute-1", Address: "compute.example.test", Components: []string{"access", "trade"}},
	}))
	f.keys, err = keys.NewStore(f.db, fixtureMaster)
	require.NoError(t, err)
	_, err = f.keys.EnsureAll(context.Background())
	require.NoError(t, err)
	f.badger, err = badger.Open(badger.DefaultOptions(f.cachePath).WithLogger(nil))
	require.NoError(t, err)
	t.Cleanup(func() {
		if !f.badger.IsClosed() {
			require.NoError(t, f.badger.Close())
		}
	})
	f.service = f.newService(t, f.db)
	f.clock.Store(time.Now().UnixNano())
	f.service.now = func() time.Time { return time.Unix(0, f.clock.Load()) }
	return f
}

func (f *controlFixture) newService(t *testing.T, db *gorm.DB) *Service {
	t.Helper()
	cache, err := authdao.NewCacheDBFromBadger(f.badger)
	require.NoError(t, err)
	service, err := NewService(db, "control", fixtureMaster, authdao.NewUserDAO(db, cache))
	require.NoError(t, err)
	return service
}

func hostContext(host string) context.Context {
	return context.WithValue(context.Background(), verifiedCaller{}, "host-gateway@"+host)
}

func (f *controlFixture) snapshot(t *testing.T, host string) *pb.HostGatewaySnapshot {
	t.Helper()
	rsp, err := f.service.PullSnapshot(hostContext(host), &pb.PullSnapshotReq{HostId: host})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.True(t, rsp.Changed)
	require.NotNil(t, rsp.Snapshot)
	return rsp.Snapshot
}

func TestSnapshotsCoverActualKeysAndOnlyTargetHostPermissions(t *testing.T) {
	f := newControlFixture(t)
	ctx := context.Background()
	storage := f.snapshot(t, "storage")
	require.NotEmpty(t, storage.Hash)
	for _, key := range storage.VerificationKeys {
		require.NotContains(t, []string{"scf-collector", "factor-engine", "moox-skill", "admin", "host-gateway@compute-1"}, key.Caller)
	}
	unchanged, err := f.service.PullSnapshot(hostContext("storage"), &pb.PullSnapshotReq{HostId: "storage", CurrentHash: storage.Hash})
	require.NoError(t, err)
	require.False(t, unchanged.Changed)
	require.Nil(t, unchanged.Snapshot, "unchanged responses must not retransmit secrets")
	old, err := f.keys.Current(ctx, "console")
	require.NoError(t, err)
	current, err := f.keys.Rotate(ctx, "console")
	require.NoError(t, err)
	rotated := f.snapshot(t, "storage")
	require.NotEqual(t, storage.Hash, rotated.Hash)
	for _, id := range []string{old.KeyID, current.KeyID} {
		require.True(t, slices.ContainsFunc(rotated.VerificationKeys, func(key *pb.GatewayVerificationKey) bool {
			return key.Caller == "console" && key.KeyId == id && key.ExpiresAtUnix == 0
		}))
	}
	_, err = f.keys.Rotate(ctx, "factor-engine")
	require.NoError(t, err)
	require.Equal(t, rotated.Hash, f.snapshot(t, "storage").Hash, "external rotations never enter host snapshots")
	require.NoError(t, f.keys.Retire(ctx, "console", old.KeyID))
	retired := f.snapshot(t, "storage")
	require.NotEqual(t, rotated.Hash, retired.Hash)
	require.False(t, slices.ContainsFunc(retired.VerificationKeys, func(key *pb.GatewayVerificationKey) bool { return key.KeyId == old.KeyID }))
	retired.VerificationKeys[0].Secret[0] ^= 1
	require.NotEqual(t, retired.VerificationKeys[0].Secret, f.snapshot(t, "storage").VerificationKeys[0].Secret)
	require.NoError(t, f.topology.SetHostStatus(ctx, "storage", servicecatalog.Disabled))
	disabled := f.snapshot(t, "storage")
	require.True(t, disabled.Disabled)
	require.Empty(t, disabled.Routes)
	require.Empty(t, disabled.VerificationKeys)
	require.NotContains(t, disabled.Directory.Hosts, "storage")
	_, err = f.service.PullSnapshot(hostContext("control"), &pb.PullSnapshotReq{HostId: "storage"})
	require.Error(t, err)
	_, err = f.service.PullSnapshot(ctx, &pb.PullSnapshotReq{HostId: "control"})
	require.Error(t, err)
}

func TestSnapshotReadsTopologyAndKeysFromOneDatabaseState(t *testing.T) {
	f := newControlFixture(t)
	before := f.snapshot(t, "storage")
	writerDB := openControlDB(t, f.path)
	var changed atomic.Bool
	require.NoError(t, f.db.Callback().Query().After("gorm:query").Register("fixture:rotate-after-topology", func(tx *gorm.DB) {
		if tx.Statement.Table != "t_placements" || !changed.CompareAndSwap(false, true) {
			return
		}
		require.NoError(t, writerDB.Transaction(func(writer *gorm.DB) error {
			if err := writer.Exec("UPDATE t_hosts SET c_address='new.storage.example.test' WHERE c_host_id='storage'").Error; err != nil {
				return err
			}
			store, err := keys.NewStore(writer, fixtureMaster)
			if err != nil {
				return err
			}
			_, err = store.Rotate(context.Background(), "console")
			return err
		}))
	}))
	t.Cleanup(func() { _ = f.db.Callback().Query().Remove("fixture:rotate-after-topology") })
	during := f.snapshot(t, "storage")
	require.True(t, changed.Load())
	require.Equal(t, before.Hash, during.Hash, "mid-read commits must not mix old routes with new directory/keys")
	after := f.snapshot(t, "storage")
	require.NotEqual(t, before.Hash, after.Hash)
	require.Equal(t, "new.storage.example.test", after.Directory.Hosts["storage"].Address)
	require.Greater(t, len(after.VerificationKeys), len(before.VerificationKeys))
}

func (f *controlFixture) report(t *testing.T, instance string) {
	t.Helper()
	rsp, err := f.service.ReportStatus(hostContext("storage"), &pb.ReportStatusReq{HostId: "storage", InstanceId: instance, Version: "v-test-" + instance, RouteCount: 7, AppliedHash: strings.Repeat("a", 64)})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
}

func (f *controlFixture) status(t *testing.T) sysdeploy.HostGatewayStatus {
	t.Helper()
	var status sysdeploy.HostGatewayStatus
	require.NoError(t, f.db.Where("c_host_id='storage'").First(&status).Error)
	return status
}

func TestHeartbeatsDistinguishReplacementConflictAndExpiry(t *testing.T) {
	f := newControlFixture(t)
	f.report(t, "A")
	initial := f.status(t)
	require.Equal(t, f.snapshot(t, "storage").Hash, initial.ExpectedHash)
	require.Empty(t, initial.PreviousInstanceID)
	f.clock.Add(int64(time.Minute))
	f.report(t, "B")
	replaced := f.status(t)
	require.Equal(t, "A", replaced.PreviousInstanceID)
	require.NotNil(t, replaced.ReplacedAt)
	require.Empty(t, replaced.ConflictInstanceID)
	f.clock.Add(int64(time.Minute))
	f.report(t, "A")
	conflict := f.status(t)
	require.Equal(t, "B", conflict.ConflictInstanceID)
	require.NotNil(t, conflict.ConflictSeenAt)
	f.clock.Add(int64(time.Minute))
	f.report(t, "B")
	renewed := f.status(t)
	require.Equal(t, "A", renewed.ConflictInstanceID)
	require.True(t, renewed.ConflictSeenAt.After(*conflict.ConflictSeenAt))
	f.clock.Add(int64(5*time.Minute - time.Nanosecond))
	f.report(t, "B")
	require.NotEmpty(t, f.status(t).ConflictInstanceID)
	f.clock.Add(1)
	f.report(t, "B")
	require.Empty(t, f.status(t).ConflictInstanceID)
	require.Nil(t, f.status(t).ConflictSeenAt)
	f.report(t, "C")
	require.Empty(t, f.status(t).ConflictInstanceID)
	// Reads expire old conflicts even when the gateway stops heartbeating.
	past := time.Now().Add(-6 * time.Minute)
	require.NoError(t, f.db.Model(&sysdeploy.HostGatewayStatus{}).Where("c_host_id='storage'").Updates(map[string]any{"c_conflict_instance_id": "B", "c_conflict_seen_at": &past}).Error)
	read, err := f.topology.GetGatewayStatus(context.Background(), "storage")
	require.NoError(t, err)
	require.Empty(t, read.ConflictInstanceID)
	require.Nil(t, read.ConflictSeenAt)
}

func TestInvalidHeartbeatsDoNotOverwriteLastGoodStatus(t *testing.T) {
	f := newControlFixture(t)
	f.report(t, "A")
	before := f.status(t)
	for _, mutate := range []func(*pb.ReportStatusReq){
		func(r *pb.ReportStatusReq) { r.InstanceId = "" }, func(r *pb.ReportStatusReq) { r.InstanceId = "../bad" },
		func(r *pb.ReportStatusReq) { r.Version = "" }, func(r *pb.ReportStatusReq) { r.AppliedHash = "bad" },
		func(r *pb.ReportStatusReq) { r.RouteCount = -1 }, func(r *pb.ReportStatusReq) { r.RouteCount = 10001 },
		func(r *pb.ReportStatusReq) { r.Error = strings.Repeat("x", 4097) },
	} {
		req := &pb.ReportStatusReq{HostId: "storage", InstanceId: "B", Version: "v2"}
		mutate(req)
		rsp, err := f.service.ReportStatus(hostContext("storage"), req)
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
		require.Equal(t, before, f.status(t))
	}
	_, err := f.service.ReportStatus(hostContext("control"), &pb.ReportStatusReq{HostId: "storage", InstanceId: "B", Version: "v2"})
	require.Error(t, err)
	require.Equal(t, before, f.status(t))
}

func TestIndependentStatusWritersSerializeInstanceHistory(t *testing.T) {
	f := newControlFixture(t)
	f.report(t, "A")
	other := f.newService(t, openControlDB(t, f.path))
	var wg sync.WaitGroup
	responses := make([]*pb.ReportStatusRsp, 2)
	errorsSeen := make([]error, 2)
	start := make(chan struct{})
	for i, service := range []*Service{f.service, other} {
		wg.Add(1)
		go func(i int, service *Service) {
			defer wg.Done()
			<-start
			instance := []string{"B", "C"}[i]
			responses[i], errorsSeen[i] = service.ReportStatus(hostContext("storage"), &pb.ReportStatusReq{HostId: "storage", InstanceId: instance, Version: "v-" + instance})
		}(i, service)
	}
	close(start)
	wg.Wait()
	for i, response := range responses {
		require.NoError(t, errorsSeen[i])
		require.Equal(t, pb.ErrorCode_SUCCESS, response.GetRetInfo().GetCode())
	}
	status := f.status(t)
	require.Contains(t, []string{"B", "C"}, status.InstanceID)
	require.Contains(t, []string{"B", "C"}, status.PreviousInstanceID)
	require.NotEqual(t, status.InstanceID, status.PreviousInstanceID)
	require.Equal(t, "v-"+status.InstanceID, status.Version)
	require.Empty(t, status.ConflictInstanceID)
}

func TestMissingMasterCopiesFailClosedUntilExplicitProvisioning(t *testing.T) {
	f := newControlFixture(t)
	ctx := context.Background()
	before := f.snapshot(t, "control")
	require.NoError(t, f.topology.SyncHostPlacements(ctx, sysdeploy.HostSpec{HostID: "new-host", Address: "new.example.test"}))
	response, err := f.service.PullSnapshot(hostContext("control"), &pb.PullSnapshotReq{HostId: "control"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, response.GetRetInfo().GetCode())
	require.Nil(t, response.Snapshot)
	_, err = f.keys.Current(ctx, "host-gateway@new-host")
	require.ErrorIs(t, err, keys.ErrKeyNotFound, "snapshot reads must not generate deployment secrets")
	_, err = f.keys.EnsureAll(ctx)
	require.NoError(t, err)
	after := f.snapshot(t, "control")
	require.NotEqual(t, before.Hash, after.Hash)
	require.True(t, slices.ContainsFunc(after.VerificationKeys, func(key *pb.GatewayVerificationKey) bool { return key.Caller == "host-gateway@new-host" }))
	wrong, err := NewService(f.db, "control", "wrong-master-fixture", f.service.nonces)
	require.NoError(t, err)
	response, err = wrong.PullSnapshot(hostContext("control"), &pb.PullSnapshotReq{HostId: "control"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, response.GetRetInfo().GetCode())
	require.Nil(t, response.Snapshot)
	_, err = NewService(f.db, "control", fixtureMaster, nil)
	require.Error(t, err)
	_, err = NewService(f.db, "old_node", fixtureMaster, f.service.nonces)
	require.Error(t, err)
}

func TestFailedStatusWritePreservesInstanceHistory(t *testing.T) {
	f := newControlFixture(t)
	f.report(t, "A")
	before := f.status(t)
	require.NoError(t, f.db.Exec("CREATE TRIGGER reject_status BEFORE INSERT ON t_host_gateway_status BEGIN SELECT RAISE(ABORT, 'fixture rejection'); END").Error)
	response, err := f.service.ReportStatus(hostContext("storage"), &pb.ReportStatusReq{HostId: "storage", InstanceId: "B", Version: "v-new"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, response.GetRetInfo().GetCode())
	require.Equal(t, before, f.status(t))
}
