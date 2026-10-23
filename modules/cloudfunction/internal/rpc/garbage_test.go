package rpc

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/cloudfunction/internal/cloudcredential"
	"github.com/mooyang-code/moox/modules/cloudfunction/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudfunction/proto/cloudfunctiongen"
	cloudfunctionschema "github.com/mooyang-code/moox/modules/cloudfunction/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type fakePackageObjects struct {
	objects map[string]packageObject
	deleted []string
}

func (f *fakePackageObjects) List(_ context.Context, prefix string) ([]packageObject, error) {
	var listed []packageObject
	for _, object := range f.objects {
		if len(object.Key) >= len(prefix) && object.Key[:len(prefix)] == prefix {
			listed = append(listed, object)
		}
	}
	return listed, nil
}

func (f *fakePackageObjects) Delete(_ context.Context, key string) error {
	delete(f.objects, key)
	f.deleted = append(f.deleted, key)
	return nil
}

type garbageFixture struct {
	db      *gorm.DB
	svc     *Service
	objects *fakePackageObjects
	now     time.Time
}

func newGarbageFixture(t *testing.T) *garbageFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(cloudfunctionschema.AllSQL()).Error)
	objects := &fakePackageObjects{objects: map[string]packageObject{}}
	f := &garbageFixture{db: db, objects: objects, now: time.Now().UTC()}
	f.svc = &Service{
		catalog:               store.NewCatalogRepository(db),
		credentialResolver:    fakeCredentialResolver{credential: cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}},
		packageObjectsFactory: func(store.CloudAccount, cloudcredential.TencentCredential) packageObjectStore { return objects },
	}
	require.NoError(t, db.Create(&store.CloudAccount{AccountID: "acc", Provider: "tencent", COSRegion: "ap-guangzhou", COSBucket: "bkt", CreateTime: f.now, ModifyTime: f.now}).Error)
	return f
}

// pkg seeds a package record and its COS object, both created age ago.
func (f *garbageFixture) pkg(t *testing.T, id string, age time.Duration, deleted bool) string {
	t.Helper()
	key := "moox/cloud-packages/2026-10-08/collector/pkg/v1/" + id + "/pkg.zip"
	created := f.now.Add(-age)
	require.NoError(t, f.db.Create(&store.FunctionPackage{
		SpaceID: "crypto", PackageID: id, PackageName: "pkg", Version: "v1", CloudAccountID: "acc",
		COSRegion: "ap-guangzhou", COSBucket: "bkt", COSPath: key, FileSize: 100, Status: "available",
		IsDeleted: deleted, CreateTime: created, ModifyTime: created,
	}).Error)
	f.objects.objects[key] = packageObject{Key: key, Size: 100, LastModified: created}
	return key
}

func (f *garbageFixture) instance(t *testing.T, id, packageID string, deleted bool, age time.Duration) {
	t.Helper()
	changed := f.now.Add(-age)
	require.NoError(t, f.db.Create(&store.FunctionInstance{
		SpaceID: "crypto", InstanceID: id, Provider: "tencent", CloudAccountID: "acc", PackageID: packageID,
		IsDeleted: deleted, CreateTime: changed, ModifyTime: changed,
	}).Error)
}

func (f *garbageFixture) batch(t *testing.T, jobID, status string, age time.Duration, leaseOwned bool) {
	t.Helper()
	completed := f.now.Add(-age)
	batch := store.FunctionJob{SpaceID: "crypto", JobID: jobID, Operation: "deploy_instances", Status: status, TotalCount: 1, CreateTime: completed, ModifyTime: completed}
	if status != store.FunctionJobRunning {
		batch.CompletedAt = &completed
	}
	require.NoError(t, f.db.Create(&batch).Error)
	require.NoError(t, f.db.Create(&store.FunctionJobItem{
		SpaceID: "crypto", JobID: jobID, ItemID: jobID + "-0", InstanceID: "n", Status: status,
		PublishLeaseRecoveryOwned: leaseOwned, CreateTime: completed, ModifyTime: completed,
	}).Error)
}

func (f *garbageFixture) packageIDs(t *testing.T) []string {
	t.Helper()
	var ids []string
	require.NoError(t, f.db.Model(&store.FunctionPackage{}).Order("c_package_id").Pluck("c_package_id", &ids).Error)
	return ids
}

func (f *garbageFixture) seedEverything(t *testing.T) (deletedKeys []string) {
	t.Helper()
	f.pkg(t, "in-use", 48*time.Hour, false)
	f.instance(t, "live", "in-use", false, 48*time.Hour)
	deletedKeys = append(deletedKeys, f.pkg(t, "soft-deleted", time.Hour, true))
	deletedKeys = append(deletedKeys, f.pkg(t, "superseded", 10*time.Hour, false))
	f.pkg(t, "fresh-upload", time.Hour, false)
	orphan := "moox/cloud-packages/2026-10-01/collector/pkg/v0/gone/pkg.zip"
	f.objects.objects[orphan] = packageObject{Key: orphan, Size: 50, LastModified: f.now.Add(-48 * time.Hour)}
	deletedKeys = append(deletedKeys, orphan)
	uploading := "moox/cloud-packages/2026-10-08/collector/pkg/v2/uploading/pkg.zip"
	f.objects.objects[uploading] = packageObject{Key: uploading, Size: 50, LastModified: f.now.Add(-time.Minute)}
	f.instance(t, "deleted-long-ago", "soft-deleted", true, 48*time.Hour)
	f.instance(t, "deleted-just-now", "soft-deleted", true, time.Hour)
	f.batch(t, "old-batch", store.FunctionJobSuccess, 8*24*time.Hour, false)
	f.batch(t, "old-batch-owing-lease-release", store.FunctionJobSuccess, 8*24*time.Hour, true)
	f.batch(t, "recent-batch", store.FunctionJobPartial, 24*time.Hour, false)
	sort.Strings(deletedKeys)
	return deletedKeys
}

func TestCollectGarbageRemovesWhatNothingUses(t *testing.T) {
	f := newGarbageFixture(t)
	deletedKeys := f.seedEverything(t)

	rsp, err := f.svc.CollectGarbage(context.Background(), &pb.CollectGarbageReq{})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	assert.EqualValues(t, 2, rsp.GetPackages())
	assert.EqualValues(t, 3, rsp.GetCosObjects())
	assert.EqualValues(t, 250, rsp.GetCosBytes())
	assert.EqualValues(t, 1, rsp.GetDeletedInstances())
	assert.EqualValues(t, 1, rsp.GetFunctionJobs())
	assert.Empty(t, rsp.GetSkipped())

	assert.Equal(t, []string{"fresh-upload", "in-use"}, f.packageIDs(t), "the package a instance runs and an upload within the grace period stay")
	sort.Strings(f.objects.deleted)
	assert.Equal(t, deletedKeys, f.objects.deleted, "an object being uploaded within the grace period stays")
	var instances []string
	require.NoError(t, f.db.Model(&store.FunctionInstance{}).Order("c_instance_id").Pluck("c_instance_id", &instances).Error)
	assert.Equal(t, []string{"deleted-just-now", "live"}, instances)
	var batches, items []string
	require.NoError(t, f.db.Model(&store.FunctionJob{}).Order("c_job_id").Pluck("c_job_id", &batches).Error)
	require.NoError(t, f.db.Model(&store.FunctionJobItem{}).Order("c_job_id").Pluck("c_job_id", &items).Error)
	assert.Equal(t, []string{"old-batch-owing-lease-release", "recent-batch"}, batches)
	assert.Equal(t, batches, items, "batch items are removed with their batch")
}

func TestCollectGarbageDryRunOnlyCounts(t *testing.T) {
	f := newGarbageFixture(t)
	f.seedEverything(t)

	rsp, err := f.svc.CollectGarbage(context.Background(), &pb.CollectGarbageReq{DryRun: true})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	assert.EqualValues(t, 2, rsp.GetPackages())
	assert.EqualValues(t, 3, rsp.GetCosObjects())
	assert.EqualValues(t, 1, rsp.GetDeletedInstances())
	assert.EqualValues(t, 1, rsp.GetFunctionJobs())
	assert.Empty(t, f.objects.deleted)
	assert.Len(t, f.packageIDs(t), 4)
	var instances, batches int64
	require.NoError(t, f.db.Model(&store.FunctionInstance{}).Count(&instances).Error)
	require.NoError(t, f.db.Model(&store.FunctionJob{}).Count(&batches).Error)
	assert.EqualValues(t, 3, instances)
	assert.EqualValues(t, 3, batches)
}

func TestCollectGarbageKeepsPackagesWhileABatchRuns(t *testing.T) {
	f := newGarbageFixture(t)
	superseded := f.pkg(t, "superseded", 10*time.Hour, false)
	f.batch(t, "running", store.FunctionJobRunning, time.Minute, false)

	rsp, err := f.svc.CollectGarbage(context.Background(), &pb.CollectGarbageReq{})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Zero(t, rsp.GetPackages())
	assert.Contains(t, rsp.GetSkipped(), "packages: a instance batch is still running")
	assert.Equal(t, []string{"superseded"}, f.packageIDs(t))
	assert.Contains(t, f.objects.objects, superseded, "an object a package record references is never an orphan")
}

func TestCollectGarbageKeepsPackagesWhoseBucketIsUnreachable(t *testing.T) {
	f := newGarbageFixture(t)
	f.svc.credentialResolver = fakeCredentialResolver{err: errors.New("secret unavailable")}
	f.pkg(t, "superseded", 10*time.Hour, false)

	rsp, err := f.svc.CollectGarbage(context.Background(), &pb.CollectGarbageReq{})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Zero(t, rsp.GetPackages())
	assert.Equal(t, []string{"superseded"}, f.packageIDs(t), "the record that points at the object stays until COS is reachable")
	require.Len(t, rsp.GetSkipped(), 1, "one unreachable account is reported once")
	assert.Contains(t, rsp.GetSkipped()[0], "account acc: resolve cloud credential: secret unavailable")
}
