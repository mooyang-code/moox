package publishlease

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	spaceSvc "github.com/mooyang-code/moox/modules/admin/internal/service/space"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go"
)

func newLeaseTestDAO(t *testing.T) *DAO {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "admin.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return NewDAO(db)
}

func TestLeaseDAOFencesOldPublisherAfterRelease(t *testing.T) {
	dao := newLeaseTestDAO(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

	first, err := dao.Acquire(ctx, "stockcn", "publisher-a", now)
	require.NoError(t, err)
	require.EqualValues(t, 1, first.FencingToken)
	_, valid, err := dao.Validate(ctx, "stockcn", first.LeaseID, first.FencingToken, now.Add(time.Second))
	require.NoError(t, err)
	require.True(t, valid)

	_, err = dao.Acquire(ctx, "stockcn", "publisher-b", now.Add(time.Second))
	require.ErrorIs(t, err, ErrLeaseHeld)

	released, err := dao.Release(ctx, "stockcn", first.LeaseID, first.FencingToken, now.Add(2*time.Second))
	require.NoError(t, err)
	require.True(t, released)
	second, err := dao.Acquire(ctx, "stockcn", "publisher-b", now.Add(3*time.Second))
	require.NoError(t, err)
	require.EqualValues(t, first.FencingToken+1, second.FencingToken)

	_, valid, err = dao.Validate(ctx, "stockcn", first.LeaseID, first.FencingToken, now.Add(4*time.Second))
	require.NoError(t, err)
	require.False(t, valid)
	_, err = dao.Renew(ctx, "stockcn", first.LeaseID, first.FencingToken, now.Add(4*time.Second))
	require.ErrorIs(t, err, ErrLeaseStale)
}

func TestLeaseDAOReacquiresAfterExpiryWithHigherFence(t *testing.T) {
	dao := newLeaseTestDAO(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	first, err := dao.Acquire(ctx, "crypto", "publisher-a", now)
	require.NoError(t, err)

	second, err := dao.Acquire(ctx, "crypto", "publisher-b", now.Add(leaseTTL+time.Millisecond))
	require.NoError(t, err)
	require.EqualValues(t, first.FencingToken+1, second.FencingToken)
	_, valid, err := dao.Validate(ctx, "crypto", first.LeaseID, first.FencingToken, now.Add(leaseTTL+2*time.Millisecond))
	require.NoError(t, err)
	require.False(t, valid)
}

func TestLeaseDAORecoveryCannotRetakeFenceAfterNewPublisher(t *testing.T) {
	dao := newLeaseTestDAO(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	original, err := dao.Acquire(ctx, "crypto", "cli-publisher", now)
	require.NoError(t, err)

	recoveryAt := now.Add(operationClaimTTL + time.Second)
	recovered, err := dao.AcquireRecovery(ctx, "crypto", "cloudnode-recovery/job-1/item-1", original.FencingToken, recoveryAt)
	require.NoError(t, err)
	require.EqualValues(t, original.FencingToken+1, recovered.FencingToken)

	// A lost response can retry the same recovery request without incrementing
	// the token again or losing ownership of the durable item.
	retried, err := dao.AcquireRecovery(ctx, "crypto", "cloudnode-recovery/job-1/item-1", original.FencingToken, recoveryAt.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, recovered.LeaseID, retried.LeaseID)
	require.EqualValues(t, recovered.FencingToken, retried.FencingToken)

	released, err := dao.Release(ctx, "crypto", recovered.LeaseID, recovered.FencingToken, recoveryAt.Add(2*time.Second))
	require.NoError(t, err)
	require.True(t, released)

	newPublisher, err := dao.Acquire(ctx, "crypto", "new-publisher", recoveryAt.Add(3*time.Second))
	require.NoError(t, err)
	require.EqualValues(t, recovered.FencingToken+1, newPublisher.FencingToken)
	_, err = dao.AcquireRecovery(ctx, "crypto", "cloudnode-recovery/job-1/item-1", original.FencingToken, recoveryAt.Add(4*time.Second))
	require.ErrorIs(t, err, ErrLeaseSuperseded, "old durable work must not reacquire a higher fence and replay after newer work")
}

func TestLeaseDAOOperationClaimFencesNewPublisherUntilOperationEnds(t *testing.T) {
	dao := newLeaseTestDAO(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	first, err := dao.Acquire(ctx, "stockcn", "publisher-a", now)
	require.NoError(t, err)
	claim, err := dao.BeginOperation(ctx, "stockcn", first.LeaseID, "provider-attempt-1", first.FencingToken, now.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, "provider-attempt-1", claim.OperationID)

	released, err := dao.Release(ctx, "stockcn", first.LeaseID, first.FencingToken, now.Add(2*time.Second))
	require.NoError(t, err)
	require.True(t, released)
	_, err = dao.Acquire(ctx, "stockcn", "publisher-b", now.Add(3*time.Second))
	require.ErrorIs(t, err, ErrLeaseHeld, "an in-flight CloudNode operation must outlive its CLI lease release")

	_, err = dao.RenewOperation(ctx, "stockcn", claim.OperationID, first.FencingToken, now.Add(4*time.Second))
	require.NoError(t, err, "the in-flight operation may renew its claim after CLI lease release")
	ended, err := dao.EndOperation(ctx, "stockcn", claim.OperationID, first.FencingToken)
	require.NoError(t, err)
	require.True(t, ended)
	second, err := dao.Acquire(ctx, "stockcn", "publisher-b", now.Add(5*time.Second))
	require.NoError(t, err)
	require.EqualValues(t, first.FencingToken+1, second.FencingToken)
}

func TestLeaseDAOOperationClaimExpiresAfterCloudNodeCrash(t *testing.T) {
	dao := newLeaseTestDAO(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	first, err := dao.Acquire(ctx, "crypto", "publisher-a", now)
	require.NoError(t, err)
	_, err = dao.BeginOperation(ctx, "crypto", first.LeaseID, "provider-attempt-1", first.FencingToken, now.Add(time.Second))
	require.NoError(t, err)

	second, err := dao.Acquire(ctx, "crypto", "publisher-b", now.Add(operationClaimTTL+2*time.Second))
	require.NoError(t, err)
	require.EqualValues(t, first.FencingToken+1, second.FencingToken)
}

func TestLeaseDAOWaitsForExpiredLeaseOperationClaimBeforeHigherFence(t *testing.T) {
	dao := newLeaseTestDAO(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	first, err := dao.Acquire(ctx, "stockcn", "cli-publisher", now)
	require.NoError(t, err)
	_, err = dao.BeginOperation(ctx, "stockcn", first.LeaseID, "cloudnode-item-1", first.FencingToken, now.Add(time.Second))
	require.NoError(t, err)

	leaseExpiredAt := now.Add(leaseTTL + time.Millisecond)
	_, err = dao.Renew(ctx, "stockcn", first.LeaseID, first.FencingToken, leaseExpiredAt)
	require.ErrorIs(t, err, ErrLeaseStale, "the original CLI lease can no longer be renewed")
	_, err = dao.Acquire(ctx, "stockcn", "cloudnode-recovery", leaseExpiredAt)
	require.ErrorIs(t, err, ErrLeaseHeld, "the old operation claim still fences a takeover before its TTL")

	claimExpiredAt := now.Add(time.Second + operationClaimTTL + time.Millisecond)
	second, err := dao.Acquire(ctx, "stockcn", "cloudnode-recovery", claimExpiredAt)
	require.NoError(t, err)
	require.EqualValues(t, first.FencingToken+1, second.FencingToken)
}

type rejectingLeaseAuthorizer struct {
	spaceSvc.Service
	err error
}

func (a rejectingLeaseAuthorizer) AuthorizeTradeRequest(context.Context, string, string, string, int32) error {
	return a.err
}

func TestLeaseServiceRequiresSpaceAuthorizationForRenewAndRelease(t *testing.T) {
	dao := newLeaseTestDAO(t)
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	lease, err := dao.Acquire(context.Background(), "crypto", "publisher", now)
	require.NoError(t, err)

	service := &Service{dao: dao, authorizer: rejectingLeaseAuthorizer{err: errors.New("member cannot publish")}}
	ctx := trpc.BackgroundContext()
	trpc.SetMetaData(ctx, model.CtxUserID, []byte("member"))
	trpc.SetMetaData(ctx, model.CtxUserRole, []byte("1"))

	renewed, err := service.RenewCollectorPublishLease(ctx, &pb.RenewCollectorPublishLeaseReq{
		SpaceId: lease.SpaceID, LeaseId: lease.LeaseID, FencingToken: lease.FencingToken,
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NO_PERMISSION, renewed.GetRetInfo().GetCode())

	released, err := service.ReleaseCollectorPublishLease(ctx, &pb.ReleaseCollectorPublishLeaseReq{
		SpaceId: lease.SpaceID, LeaseId: lease.LeaseID, FencingToken: lease.FencingToken,
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NO_PERMISSION, released.GetRetInfo().GetCode())
	require.False(t, released.GetReleased())
	validated, err := service.ValidateCollectorPublishLease(ctx, &pb.ValidateCollectorPublishLeaseReq{
		SpaceId: lease.SpaceID, LeaseId: lease.LeaseID, FencingToken: lease.FencingToken,
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NO_PERMISSION, validated.GetRetInfo().GetCode())

	_, valid, err := dao.Validate(context.Background(), lease.SpaceID, lease.LeaseID, lease.FencingToken, now.Add(time.Second))
	require.NoError(t, err)
	require.True(t, valid, "unauthorized lease management must not change the persisted lease")
}
