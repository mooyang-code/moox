package rpc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tencentscf "github.com/mooyang-code/moox/modules/cloudnode/internal/providers/tencentscf"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/publishlease"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"trpc.group/trpc-go/trpc-go/log"
)

func TestNodeBatchRunnerRunsTakenBatchWithTRPCGoAndWait(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	createRunnerBatch(t, catalog, "runner-concurrent", 7)
	var active atomic.Int32
	var maxActive atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, 7)
	var batchesMu sync.Mutex
	var batchSizes []int
	svc := &Service{
		catalog: catalog,
		executeNodeBatchItem: func(context.Context, store.NodeBatchItem) (string, error) {
			current := active.Add(1)
			for {
				previous := maxActive.Load()
				if current <= previous || maxActive.CompareAndSwap(previous, current) {
					break
				}
			}
			started <- struct{}{}
			<-release
			active.Add(-1)
			return "done", nil
		},
		nodeBatchTakenHook: func(items []store.NodeBatchItem) {
			batchesMu.Lock()
			batchSizes = append(batchSizes, len(items))
			batchesMu.Unlock()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, svc.StartNodeBatchRunner(ctx, 3, 100*time.Millisecond))
	for range 3 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("first batch did not start")
		}
	}
	assert.Equal(t, int32(3), maxActive.Load())
	close(release)
	waitForNodeBatchTerminal(t, catalog, "runner-concurrent")
	assert.Equal(t, int32(3), maxActive.Load())
	batchesMu.Lock()
	assert.Equal(t, []int{3, 3, 1}, batchSizes)
	batchesMu.Unlock()
}

func TestNodeBatchRunnerDoesNotTakeNextBatchUntilCurrentBatchFinishes(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	createRunnerBatch(t, catalog, "runner-barrier", 4)
	block := make(chan struct{})
	firstStarted := make(chan struct{})
	var once sync.Once
	svc := &Service{
		catalog: catalog,
		executeNodeBatchItem: func(_ context.Context, item store.NodeBatchItem) (string, error) {
			if item.ItemIndex == 0 {
				once.Do(func() { close(firstStarted) })
				<-block
			}
			return "done", nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, svc.StartNodeBatchRunner(ctx, 3, 100*time.Millisecond))
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first batch did not start")
	}
	require.Eventually(t, func() bool {
		aggregate, err := catalog.GetNodeBatch(context.Background(), "crypto", "runner-barrier")
		return err == nil && aggregate.PendingCount == 1 && aggregate.RunningCount == 1 && aggregate.SuccessCount == 2
	}, time.Second, 10*time.Millisecond)
	close(block)
	waitForNodeBatchTerminal(t, catalog, "runner-barrier")
}

func TestNodeBatchRunnerCompletesOtherItemsAfterOneFailure(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	createRunnerBatch(t, catalog, "runner-partial", 3)
	svc := &Service{
		catalog: catalog,
		executeNodeBatchItem: func(_ context.Context, item store.NodeBatchItem) (string, error) {
			if item.ItemIndex == 1 {
				return "", errors.New("provider rejected item")
			}
			return fmt.Sprintf("done %s", item.NodeID), nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, svc.StartNodeBatchRunner(ctx, 3, 100*time.Millisecond))
	aggregate := waitForNodeBatchTerminal(t, catalog, "runner-partial")
	assert.Equal(t, store.NodeBatchPartial, aggregate.Job.Status)
	assert.Equal(t, 2, aggregate.SuccessCount)
	assert.Equal(t, 1, aggregate.FailedCount)
}

func TestNodeBatchRunnerRetriesTransientProviderFailure(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	createRunnerBatch(t, catalog, "runner-provider-retry", 1)
	var calls atomic.Int32
	svc := &Service{
		catalog: catalog,
		executeNodeBatchItem: func(context.Context, store.NodeBatchItem) (string, error) {
			if calls.Add(1) < nodeBatchProviderAttempts {
				return "", errors.New("ClientError.NetworkError: TLS handshake timeout")
			}
			return "done after retry", nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, svc.StartNodeBatchRunner(ctx, 1, 100*time.Millisecond))

	aggregate := waitForNodeBatchTerminal(t, catalog, "runner-provider-retry")
	assert.Equal(t, store.NodeBatchSuccess, aggregate.Job.Status)
	assert.Equal(t, int32(nodeBatchProviderAttempts), calls.Load())
	assert.Equal(t, "done after retry", aggregate.Items[0].ResultSummary)
}

func TestDeployBatchReconcilesAfterSuccessfulCodeMutationAndLaterFailure(t *testing.T) {
	for _, failureStage := range []string{"configuration", "timer trigger"} {
		t.Run(failureStage, func(t *testing.T) {
			catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
			seedGenericNodeItemPackage(t, catalog)
			node := store.CloudNode{
				SpaceID: "crypto", NodeID: "node-partial-deploy", CloudAccountID: "account-a",
				PackageID: "old-package", NodeType: "scf-event", Provider: "tencent-scf",
				Region: "ap-guangzhou", Namespace: "collector", FunctionName: "collector-partial-deploy",
				Metadata: `{"handler":"main"}`,
			}
			if failureStage == "timer trigger" {
				node.TriggerType = "timer"
			}
			require.NoError(t, catalog.UpsertNode(context.Background(), node))

			fake := &fakeSCFClient{
				getResults: []fakeSCFGetResult{{info: &tencentscf.FunctionInfo{
					Status: "Active", Environment: map[string]string{"MOOX_CODE_PACKAGE_ID": "old-package"},
				}}},
			}
			providerFailure := errors.New("provider rejected configuration")
			if failureStage == "configuration" {
				fake.configurationErr = providerFailure
			} else {
				fake.timerErr = providerFailure
			}
			svc := newNodeItemTestService(catalog, fake)

			submitted, err := svc.SubmitDeployNodes(nodeBatchContext("crypto"), &pb.BatchDeployNodesReq{
				Deployments: []*pb.NodeDeployItem{{NodeId: node.NodeID, PackageId: "moox-collector_dev"}},
			})
			require.NoError(t, err)
			require.Equal(t, pb.ErrorCode_SUCCESS, submitted.GetRetInfo().GetCode())

			runnerCtx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			require.NoError(t, svc.StartNodeBatchRunner(runnerCtx, 1, 100*time.Millisecond))
			var aggregate *store.NodeBatchAggregate
			require.Eventually(t, func() bool {
				var getErr error
				aggregate, getErr = catalog.GetNodeBatch(context.Background(), "crypto", submitted.GetJobId())
				return getErr == nil && aggregate != nil && len(aggregate.Items) == 1 &&
					aggregate.Items[0].Status == store.NodeBatchReconciliationRequired
			}, 3*time.Second, 10*time.Millisecond)

			assert.Equal(t, 0, aggregate.FailedCount)
			assert.Equal(t, 1, aggregate.RunningCount)
			assert.Contains(t, aggregate.Items[0].ErrorMessage, providerFailure.Error())

			fake.mu.Lock()
			assert.Len(t, fake.updated, 1, "the package code mutation succeeded before the later failure")
			assert.Len(t, fake.configured, 1)
			if failureStage == "timer trigger" {
				assert.Equal(t, 1, fake.timerEnsures)
			}
			fake.mu.Unlock()

			persistedNode, getErr := catalog.GetNode(context.Background(), "crypto", node.NodeID)
			require.NoError(t, getErr)
			require.NotNil(t, persistedNode)
			assert.Equal(t, "old-package", persistedNode.PackageID, "catalog state must not claim the partial deployment completed")
			assert.Error(t, catalog.ClaimNodeMutation(context.Background(), "crypto", node.NodeID, persistedNode.LifecycleID, "replacement-operation"),
				"the exact operation claim must remain held until reconciliation")
		})
	}
}

type testPublishLeaseValidator struct {
	calls    atomic.Int32
	ends     atomic.Int32
	rejectAt int32
}

func (v *testPublishLeaseValidator) BeginOperation(context.Context, string, string, int64, string) error {
	call := v.calls.Add(1)
	if v.rejectAt > 0 && call >= v.rejectAt {
		return errors.New("collector publish lease is no longer current")
	}
	return nil
}

func (*testPublishLeaseValidator) RenewOperation(context.Context, string, string, int64) error {
	return nil
}

func (v *testPublishLeaseValidator) EndOperation(context.Context, string, string, int64) error {
	v.ends.Add(1)
	return nil
}

func (*testPublishLeaseValidator) AcquireLease(_ context.Context, spaceID, holderID string, _ int64) (*publishlease.Lease, error) {
	return &publishlease.Lease{SpaceID: spaceID, LeaseID: holderID, FencingToken: 8}, nil
}

func (*testPublishLeaseValidator) RenewLease(context.Context, *publishlease.Lease) error { return nil }

func (*testPublishLeaseValidator) ReleaseLease(context.Context, *publishlease.Lease) error {
	return nil
}

func TestCollectorPublishOperationClaimIsRetainedAfterAmbiguousProviderFailure(t *testing.T) {
	validator := &testPublishLeaseValidator{}
	service := &Service{publishLeaseValidator: validator}
	_, err := service.withCollectorPublishOperation(context.Background(), "crypto", collectorPublishFence{leaseID: "lease-1", fencingToken: 7}, func(context.Context) (string, error) {
		return "", errors.New("ClientError.NetworkError: connection reset after request submission")
	})
	require.Error(t, err)
	assert.Zero(t, validator.ends.Load(), "an ambiguous provider result must retain its claim until TTL")

	_, err = service.withCollectorPublishOperation(context.Background(), "crypto", collectorPublishFence{leaseID: "lease-1", fencingToken: 7}, func(context.Context) (string, error) {
		return "", errors.New("validation failed before provider mutation")
	})
	require.Error(t, err)
	assert.EqualValues(t, 1, validator.ends.Load(), "a deterministic failure can release its claim immediately")
}

func TestNodeBatchRunnerRevalidatesFenceBeforeProviderRetry(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	request, err := protojson.Marshal(validCreateNodeItem(t, "node-fenced"))
	require.NoError(t, err)
	validator := &testPublishLeaseValidator{rejectAt: 2}
	var providerCalls atomic.Int32
	svc := &Service{
		catalog:               catalog,
		publishLeaseValidator: validator,
		executeNodeBatchItem: func(context.Context, store.NodeBatchItem) (string, error) {
			providerCalls.Add(1)
			return "", errors.New("ClientError.NetworkError: TLS handshake timeout")
		},
	}
	item := store.NodeBatchItem{SpaceID: "crypto", RequestJSON: string(request)}

	_, err = svc.dispatchNodeBatchItemWithRetry(context.Background(), &item, nodeBatchOperationCreate)

	require.ErrorContains(t, err, "lease is no longer current")
	assert.EqualValues(t, 2, validator.calls.Load())
	assert.EqualValues(t, 1, providerCalls.Load(), "a superseded token must stop retries before the next Provider call")
}

func TestNodeBatchRecoversStalePublishFenceOnSameDurableItem(t *testing.T) {
	db := newNodeSCFTestDB(t)
	catalog := store.NewCatalogRepository(db)
	seedSCFAccountAndPackage(t, catalog)
	node := store.CloudNode{
		SpaceID: "crypto", NodeID: "node-stale-fence", CloudAccountID: "account-a", PackageID: "moox-collector_dev",
		NodeType: "scf-event", Provider: "tencent-scf", Region: "ap-guangzhou", Namespace: "collector",
		FunctionName: "node-stale-fence", Metadata: `{"biz_type":"market_fetcher","collector_publish_fenced":true}`,
	}
	require.NoError(t, catalog.UpsertNode(context.Background(), node))
	validator := &takeoverPublishLeaseValidator{}
	claimed := make(chan *pb.NodeDeployItem, 1)
	continueMutation := make(chan struct{})
	service := &Service{
		catalog:               catalog,
		publishLeaseValidator: validator,
		executeNodeBatchItem: func(ctx context.Context, batchItem store.NodeBatchItem) (string, error) {
			request := &pb.NodeDeployItem{}
			if err := protojson.Unmarshal([]byte(batchItem.RequestJSON), request); err != nil {
				return "", err
			}
			if err := catalog.ClaimNodeMutation(ctx, batchItem.SpaceID, request.GetNodeId(), request.GetLifecycleId(), request.GetOperationId()); err != nil {
				return "", err
			}
			claimed <- request
			select {
			case <-continueMutation:
				return "reconciled", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
	}

	submitted, err := service.SubmitDeployNodes(nodeBatchContext("crypto"), &pb.BatchDeployNodesReq{
		Deployments: []*pb.NodeDeployItem{{
			NodeId: node.NodeID, PackageId: node.PackageID,
			CollectorPublishLeaseId: "cli-lease", CollectorPublishFencingToken: 7,
		}},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, submitted.GetRetInfo().GetCode())

	items, err := catalog.TakePendingNodeBatchItems(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.NoError(t, service.runTakenNodeBatch(context.Background(), items))
	aggregate, err := catalog.GetNodeBatch(context.Background(), "crypto", submitted.GetJobId())
	require.NoError(t, err)
	require.NotNil(t, aggregate)
	require.Equal(t, store.NodeBatchReconciliationRequired, aggregate.Items[0].Status)
	assert.False(t, aggregate.Items[0].PublishLeaseRecoveryOwned, "a held lease must not be mistaken for one acquired by CloudNode")
	oldRequest := &pb.NodeDeployItem{}
	require.NoError(t, protojson.Unmarshal([]byte(aggregate.Items[0].RequestJSON), oldRequest))
	assert.Equal(t, "cli-lease", oldRequest.GetCollectorPublishLeaseId())
	assert.EqualValues(t, 7, oldRequest.GetCollectorPublishFencingToken())
	select {
	case <-claimed:
		t.Fatal("Provider mutation must not start while an older operation claim is active")
	default:
	}

	validator.allowAcquire.Store(true)
	require.NoError(t, db.Model(&store.NodeBatchItem{}).
		Where("c_space_id = ? AND c_job_id = ? AND c_item_id = ?", "crypto", submitted.GetJobId(), aggregate.Items[0].ItemID).
		Update("c_mtime", time.Now().UTC().Add(-20*time.Second)).Error)
	items, err = catalog.TakePendingNodeBatchItems(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.False(t, items[0].ResumeClaim, "the first stale-lease attempt did not acquire a node lifecycle claim")

	finished := make(chan error, 1)
	go func() { finished <- service.runTakenNodeBatch(context.Background(), items) }()
	var recovered *pb.NodeDeployItem
	select {
	case recovered = <-claimed:
	case <-time.After(time.Second):
		t.Fatal("same durable item did not resume after the old operation claim expired")
	}
	require.Equal(t, "node-stale-fence", recovered.GetNodeId())
	require.Equal(t, oldRequest.GetLifecycleId(), recovered.GetLifecycleId())
	require.Equal(t, oldRequest.GetOperationId(), recovered.GetOperationId())
	require.Equal(t, "recovery-lease", recovered.GetCollectorPublishLeaseId())
	require.EqualValues(t, 8, recovered.GetCollectorPublishFencingToken())

	require.Error(t, catalog.ClaimNodeMutation(context.Background(), "crypto", node.NodeID, recovered.GetLifecycleId(), "competing-operation"),
		"the recovered item must retain its original lifecycle claim against concurrent work")
	close(continueMutation)
	select {
	case err := <-finished:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("reconciled item did not reach a terminal state")
	}

	aggregate, err = catalog.GetNodeBatch(context.Background(), "crypto", submitted.GetJobId())
	require.NoError(t, err)
	require.Equal(t, store.NodeBatchSuccess, aggregate.Job.Status)
	assert.False(t, aggregate.Items[0].PublishLeaseRecoveryOwned, "terminal transition must release and clear only the recovery-owned lease")
	finalRequest := &pb.NodeDeployItem{}
	require.NoError(t, protojson.Unmarshal([]byte(aggregate.Items[0].RequestJSON), finalRequest))
	assert.Equal(t, recovered.GetLifecycleId(), finalRequest.GetLifecycleId())
	assert.Equal(t, recovered.GetOperationId(), finalRequest.GetOperationId())
	assert.Equal(t, "moox-collector_dev", finalRequest.GetPackageId())
	assert.EqualValues(t, 2, validator.acquireCalls.Load(), "first acquire is held by the old operation claim; second succeeds after it expires")
	assert.EqualValues(t, 1, validator.releaseCalls.Load())
	assert.Equal(t, "recovery-lease", validator.releasedLeaseID)
	assert.EqualValues(t, 8, validator.releasedToken)
	assert.Empty(t, validator.releasedOriginalLease, "CloudNode must never release the original CLI lease")
}

type takeoverPublishLeaseValidator struct {
	allowAcquire          atomic.Bool
	acquireCalls          atomic.Int32
	renewLeaseCalls       atomic.Int32
	releaseCalls          atomic.Int32
	mu                    sync.Mutex
	releasedLeaseID       string
	releasedToken         int64
	releasedOriginalLease string
}

func (*takeoverPublishLeaseValidator) BeginOperation(_ context.Context, _ string, leaseID string, token int64, _ string) error {
	if leaseID == "cli-lease" || token < 8 {
		return fmt.Errorf("%w: previous CLI lease expired", publishlease.ErrLeaseStale)
	}
	return nil
}

func (*takeoverPublishLeaseValidator) RenewOperation(context.Context, string, string, int64) error {
	return nil
}

func (*takeoverPublishLeaseValidator) EndOperation(context.Context, string, string, int64) error {
	return nil
}

func (v *takeoverPublishLeaseValidator) AcquireLease(_ context.Context, spaceID, _ string, _ int64) (*publishlease.Lease, error) {
	v.acquireCalls.Add(1)
	if !v.allowAcquire.Load() {
		return nil, publishlease.ErrLeaseHeld
	}
	return &publishlease.Lease{SpaceID: spaceID, LeaseID: "recovery-lease", FencingToken: 8}, nil
}

func (v *takeoverPublishLeaseValidator) RenewLease(context.Context, *publishlease.Lease) error {
	v.renewLeaseCalls.Add(1)
	return nil
}

func (v *takeoverPublishLeaseValidator) ReleaseLease(_ context.Context, lease *publishlease.Lease) error {
	v.releaseCalls.Add(1)
	v.mu.Lock()
	defer v.mu.Unlock()
	if lease.LeaseID == "cli-lease" {
		v.releasedOriginalLease = lease.LeaseID
	}
	v.releasedLeaseID = lease.LeaseID
	v.releasedToken = lease.FencingToken
	return nil
}

func TestNodeBatchRecoveryRenewsLeaseAcrossShortReconciliationAttempts(t *testing.T) {
	db := newNodeSCFTestDB(t)
	catalog := store.NewCatalogRepository(db)
	seedSCFAccountAndPackage(t, catalog)
	node := store.CloudNode{
		SpaceID: "crypto", NodeID: "node-short-reconcile", CloudAccountID: "account-a", PackageID: "moox-collector_dev",
		NodeType: "scf-event", Provider: "tencent-scf", Region: "ap-guangzhou", Namespace: "collector",
		FunctionName: "node-short-reconcile", Metadata: `{"biz_type":"market_fetcher","collector_publish_fenced":true}`,
	}
	require.NoError(t, catalog.UpsertNode(context.Background(), node))
	current, err := catalog.GetNode(context.Background(), "crypto", node.NodeID)
	require.NoError(t, err)
	itemID := "item-short-reconcile"
	request, err := protojson.Marshal(&pb.NodeDeployItem{
		NodeId: node.NodeID, PackageId: "moox-collector_dev", LifecycleId: current.LifecycleID, OperationId: itemID,
		CollectorPublishLeaseId: "cli-lease", CollectorPublishFencingToken: 7,
	})
	require.NoError(t, err)
	require.NoError(t, catalog.ClaimNodeMutation(context.Background(), "crypto", node.NodeID, current.LifecycleID, itemID))
	require.NoError(t, catalog.CreateNodeBatch(context.Background(), store.NodeBatchCreate{
		SpaceID: "crypto", JobID: "job-short-reconcile", Operation: nodeBatchOperationDeploy,
		Items: []store.NodeBatchItemCreate{{ItemID: itemID, NodeID: node.NodeID, RequestJSON: string(request)}},
	}))
	validator := &takeoverPublishLeaseValidator{}
	validator.allowAcquire.Store(true)
	var executions atomic.Int32
	service := &Service{
		catalog: catalog, publishLeaseValidator: validator,
		executeNodeBatchItem: func(context.Context, store.NodeBatchItem) (string, error) {
			if executions.Add(1) == 1 {
				return "", fmt.Errorf("%w: provider state needs verification", errNodeMutationReconciliationRequired)
			}
			return "", errors.New("provider readback permission denied")
		},
	}

	for attempt := 0; attempt < 2; attempt++ {
		items, takeErr := catalog.TakePendingNodeBatchItems(context.Background(), 1)
		require.NoError(t, takeErr)
		require.Len(t, items, 1)
		require.NoError(t, service.runTakenNodeBatch(context.Background(), items))
		aggregate, getErr := catalog.GetNodeBatch(context.Background(), "crypto", "job-short-reconcile")
		require.NoError(t, getErr)
		require.Equal(t, store.NodeBatchReconciliationRequired, aggregate.Items[0].Status)
		if attempt == 0 {
			require.NoError(t, db.Model(&store.NodeBatchItem{}).
				Where("c_space_id = ? AND c_job_id = ? AND c_item_id = ?", "crypto", "job-short-reconcile", itemID).
				Update("c_mtime", time.Now().UTC().Add(-20*time.Second)).Error)
		}
	}
	assert.EqualValues(t, 2, validator.renewLeaseCalls.Load(), "every short recovered attempt renews the lease before beginning another operation")
	assert.Zero(t, validator.releaseCalls.Load(), "a nonterminal reconciliation must retain its recovery lease")
	assert.Error(t, catalog.ClaimNodeMutation(context.Background(), "crypto", node.NodeID, current.LifecycleID, "competing-operation"),
		"the node lifecycle claim must remain held during reconciliation")
}

type expiredRecoveryLeaseValidator struct {
	acquireCalls atomic.Int32
	beginTokens  []int64
	mu           sync.Mutex
}

func (v *expiredRecoveryLeaseValidator) BeginOperation(_ context.Context, _, _ string, token int64, _ string) error {
	v.mu.Lock()
	v.beginTokens = append(v.beginTokens, token)
	v.mu.Unlock()
	if token < 9 {
		return publishlease.ErrLeaseStale
	}
	return nil
}

func (*expiredRecoveryLeaseValidator) RenewOperation(context.Context, string, string, int64) error {
	return nil
}

func (*expiredRecoveryLeaseValidator) EndOperation(context.Context, string, string, int64) error {
	return nil
}

func (v *expiredRecoveryLeaseValidator) AcquireLease(_ context.Context, spaceID, holderID string, expectedToken int64) (*publishlease.Lease, error) {
	v.acquireCalls.Add(1)
	return &publishlease.Lease{SpaceID: spaceID, LeaseID: holderID, FencingToken: expectedToken + 1}, nil
}

func (*expiredRecoveryLeaseValidator) RenewLease(_ context.Context, lease *publishlease.Lease) error {
	if lease.FencingToken < 9 {
		return publishlease.ErrLeaseStale
	}
	return nil
}

func (*expiredRecoveryLeaseValidator) ReleaseLease(context.Context, *publishlease.Lease) error {
	return nil
}

func TestNodeBatchRecoveryReacquiresExpiredRecoveryLease(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	ctx := context.Background()
	node := store.CloudNode{
		SpaceID: "crypto", NodeID: "node-expired-recovery-lease", CloudAccountID: "account-a", PackageID: "moox-collector_dev",
		NodeType: "scf-event", Provider: "tencent-scf", Region: "ap-guangzhou", Namespace: "collector",
		FunctionName: "node-expired-recovery-lease", Metadata: `{"biz_type":"market_fetcher","collector_publish_fenced":true}`,
	}
	require.NoError(t, catalog.UpsertNode(ctx, node))
	current, err := catalog.GetNode(ctx, "crypto", node.NodeID)
	require.NoError(t, err)
	itemID := "item-expired-recovery-lease"
	request, err := protojson.Marshal(&pb.NodeDeployItem{
		NodeId: node.NodeID, PackageId: "moox-collector_dev", LifecycleId: current.LifecycleID, OperationId: itemID,
		CollectorPublishLeaseId: "cli-lease", CollectorPublishFencingToken: 7,
	})
	require.NoError(t, err)
	require.NoError(t, catalog.ClaimNodeMutation(ctx, "crypto", node.NodeID, current.LifecycleID, itemID))
	require.NoError(t, catalog.CreateNodeBatch(ctx, store.NodeBatchCreate{
		SpaceID: "crypto", JobID: "job-expired-recovery-lease", Operation: nodeBatchOperationDeploy,
		Items: []store.NodeBatchItemCreate{{ItemID: itemID, NodeID: node.NodeID, RequestJSON: string(request)}},
	}))
	items, err := catalog.TakePendingNodeBatchItems(ctx, 1)
	require.NoError(t, err)
	require.Len(t, items, 1)
	recovered, err := catalog.ReplaceNodeBatchItemPublishFence(ctx, "crypto", "job-expired-recovery-lease", itemID, "cli-lease", 7, "recovery-lease", 8)
	require.NoError(t, err)
	validator := &expiredRecoveryLeaseValidator{}
	service := &Service{
		catalog: catalog, publishLeaseValidator: validator,
		executeNodeBatchItem: func(context.Context, store.NodeBatchItem) (string, error) { return "reconciled", nil },
	}
	summary, err := service.dispatchNodeBatchItemWithRetry(ctx, recovered, nodeBatchOperationDeploy)
	require.NoError(t, err)
	assert.Equal(t, "reconciled", summary)
	assert.EqualValues(t, 1, validator.acquireCalls.Load(), "a stale recovery-owned lease must trigger a compare-and-swap takeover")
	validator.mu.Lock()
	assert.Equal(t, []int64{9}, validator.beginTokens)
	validator.mu.Unlock()
	requestAfterRecovery := &pb.NodeDeployItem{}
	require.NoError(t, protojson.Unmarshal([]byte(recovered.RequestJSON), requestAfterRecovery))
	assert.EqualValues(t, 9, requestAfterRecovery.GetCollectorPublishFencingToken())
}

func TestNodeBatchRunnerWaitsForExistingMutationClaimWithoutPoisoningResume(t *testing.T) {
	for _, operation := range []string{nodeBatchOperationDeploy, nodeBatchOperationDelete} {
		t.Run(operation, func(t *testing.T) {
			db := newNodeSCFTestDB(t)
			catalog := store.NewCatalogRepository(db)
			ctx := context.Background()
			nodeID := "node-claim-wait-" + operation
			require.NoError(t, catalog.UpsertNode(ctx, store.CloudNode{SpaceID: "crypto", NodeID: nodeID, PackageID: "pkg-old"}))
			node, err := catalog.GetNode(ctx, "crypto", nodeID)
			require.NoError(t, err)
			jobID := "job-claim-wait-" + operation
			itemID := jobID + "-000"
			var request []byte
			if operation == nodeBatchOperationDelete {
				request, err = protojson.Marshal(&pb.NodeDeleteItem{NodeId: nodeID, LifecycleId: node.LifecycleID, OperationId: itemID})
			} else {
				request, err = protojson.Marshal(&pb.NodeDeployItem{NodeId: nodeID, LifecycleId: node.LifecycleID, OperationId: itemID})
			}
			require.NoError(t, err)
			require.NoError(t, catalog.CreateNodeBatch(ctx, store.NodeBatchCreate{
				SpaceID: "crypto", JobID: jobID, Operation: operation,
				Items: []store.NodeBatchItemCreate{{ItemID: itemID, NodeID: nodeID, RequestJSON: string(request)}},
			}))
			require.NoError(t, catalog.ClaimNodeMutation(ctx, "crypto", nodeID, node.LifecycleID, "previous-durable-item"))
			var calls atomic.Int32
			service := &Service{
				catalog: catalog,
				executeNodeBatchItem: func(_ context.Context, item store.NodeBatchItem) (string, error) {
					var claimErr error
					if operation == nodeBatchOperationDelete {
						claimErr = catalog.ClaimNodeDelete(ctx, "crypto", nodeID, node.LifecycleID, item.ItemID)
					} else {
						claimErr = catalog.ClaimNodeMutation(ctx, "crypto", nodeID, node.LifecycleID, item.ItemID)
					}
					if claimErr != nil {
						return "", claimErr
					}
					calls.Add(1)
					return "", errors.New("permission denied before provider mutation")
				},
			}
			items, err := catalog.TakePendingNodeBatchItems(ctx, 1)
			require.NoError(t, err)
			require.NoError(t, service.runTakenNodeBatch(ctx, items))
			aggregate, err := catalog.GetNodeBatch(ctx, "crypto", jobID)
			require.NoError(t, err)
			require.Equal(t, store.NodeBatchReconciliationRequired, aggregate.Items[0].Status)
			require.NoError(t, catalog.ReleaseNodeMutationClaim(ctx, "crypto", nodeID, node.LifecycleID, "previous-durable-item"))
			require.NoError(t, db.Model(&store.NodeBatchItem{}).
				Where("c_space_id = ? AND c_job_id = ? AND c_item_id = ?", "crypto", jobID, itemID).
				Update("c_mtime", time.Now().UTC().Add(-11*time.Second)).Error)
			items, err = catalog.TakePendingNodeBatchItems(ctx, 1)
			require.NoError(t, err)
			require.Len(t, items, 1)
			assert.False(t, items[0].ResumeClaim, "a claim-wait retry must not pretend this item already owns the claim")
			require.NoError(t, service.runTakenNodeBatch(ctx, items))
			aggregate, err = catalog.GetNodeBatch(ctx, "crypto", jobID)
			require.NoError(t, err)
			assert.Equal(t, store.NodeBatchFailed, aggregate.Job.Status, "a deterministic error after acquiring the claim must terminate")
			assert.Contains(t, aggregate.Items[0].ErrorMessage, "permission denied")
			assert.EqualValues(t, 1, calls.Load(), "the provider mutation should start only after the previous owner releases its claim")
			require.NoError(t, catalog.ClaimNodeMutation(ctx, "crypto", nodeID, node.LifecycleID, "next-operation"), "terminal completion must release the acquired claim")
		})
	}
}

func TestNodeBatchDeleteRequiresFenceEvenWhenPackageRecordIsMissing(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{
		SpaceID: "crypto", NodeID: "moox-fetcher-crypto-invoke-0", FunctionName: "moox-fetcher-crypto-invoke-0",
		PackageID: "deleted-package", NodeType: "scf-event", Metadata: "{\"collector_publish_fenced\":true,\"biz_type\":\"market_fetcher\"}",
	}))
	request, err := protojson.Marshal(&pb.NodeDeleteItem{NodeId: "moox-fetcher-crypto-invoke-0"})
	require.NoError(t, err)
	service := &Service{catalog: catalog, executeNodeBatchItem: func(context.Context, store.NodeBatchItem) (string, error) {
		return "deleted", nil
	}}

	_, err = service.dispatchNodeBatchItem(context.Background(), &store.NodeBatchItem{
		SpaceID: "crypto", RequestJSON: string(request),
	}, nodeBatchOperationDelete)
	require.ErrorContains(t, err, "requires a publish lease and fencing token")
}

func TestNodeBatchDeleteUsesFencingClaim(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	require.NoError(t, catalog.UpsertNode(context.Background(), store.CloudNode{
		SpaceID: "crypto", NodeID: "moox-fetcher-crypto-invoke-0", FunctionName: "moox-fetcher-crypto-invoke-0",
		PackageID: "deleted-package", NodeType: "scf-event", Metadata: "{\"collector_publish_fenced\":true,\"biz_type\":\"market_fetcher\"}",
	}))
	request, err := protojson.Marshal(&pb.NodeDeleteItem{
		NodeId: "moox-fetcher-crypto-invoke-0", CollectorPublishLeaseId: "lease-test", CollectorPublishFencingToken: 7,
	})
	require.NoError(t, err)
	validator := &testPublishLeaseValidator{}
	service := &Service{
		catalog: catalog, publishLeaseValidator: validator,
		executeNodeBatchItem: func(context.Context, store.NodeBatchItem) (string, error) { return "deleted", nil },
	}

	_, err = service.dispatchNodeBatchItem(context.Background(), &store.NodeBatchItem{
		SpaceID: "crypto", RequestJSON: string(request),
	}, nodeBatchOperationDelete)
	require.NoError(t, err)
	assert.EqualValues(t, 1, validator.calls.Load())
}

func TestNodeBatchHoldsPublishOperationClaimUntilMutationReturns(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	seedSCFAccountAndPackage(t, catalog)
	request, err := protojson.Marshal(validCreateNodeItem(t, "node-operation-claim"))
	require.NoError(t, err)
	claimStarted, mutationStarted, release, ended := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	validator := &blockingPublishLeaseValidator{started: claimStarted, ended: ended}
	service := &Service{
		catalog: catalog, publishLeaseValidator: validator,
		executeNodeBatchItem: func(ctx context.Context, _ store.NodeBatchItem) (string, error) {
			close(mutationStarted)
			select {
			case <-release:
				return "done", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
	}
	finished := make(chan error, 1)
	go func() {
		_, dispatchErr := service.dispatchNodeBatchItem(context.Background(), &store.NodeBatchItem{
			SpaceID: "crypto", RequestJSON: string(request),
		}, nodeBatchOperationCreate)
		finished <- dispatchErr
	}()
	select {
	case <-mutationStarted:
	case <-time.After(time.Second):
		t.Fatal("Provider mutation did not start")
	}
	select {
	case <-ended:
		t.Fatal("operation claim ended while the Provider mutation was still active")
	default:
	}
	close(release)
	select {
	case err := <-finished:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Provider mutation did not finish")
	}
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("operation claim was not ended after the Provider mutation")
	}
}

func TestRecoveredPublishLeaseRenewalLossRetainsOperationClaim(t *testing.T) {
	validator := &recoveredLeaseRenewalLossValidator{}
	service := &Service{publishLeaseValidator: validator}
	lease := &publishlease.Lease{SpaceID: "crypto", LeaseID: "recovery-lease", FencingToken: 8}
	result := make(chan error, 1)
	go func() {
		_, err := service.withCollectorPublishOperationRenewalInterval(
			withRecoveredPublishLease(context.Background(), lease),
			"crypto",
			collectorPublishFence{leaseID: lease.LeaseID, fencingToken: lease.FencingToken},
			time.Millisecond,
			func(ctx context.Context) (string, error) {
				<-ctx.Done()
				return "", ctx.Err()
			},
		)
		result <- err
	}()

	select {
	case err := <-result:
		require.ErrorIs(t, err, errNodeMutationReconciliationRequired)
	case <-time.After(time.Second):
		t.Fatal("lost recovery lease renewal did not cancel the active operation")
	}
	assert.GreaterOrEqual(t, validator.renewLeaseCalls.Load(), int32(1))
	assert.Zero(t, validator.endCalls.Load(), "the operation claim must remain fenced until its durable reconciliation resumes")
}

type recoveredLeaseRenewalLossValidator struct {
	renewLeaseCalls atomic.Int32
	endCalls        atomic.Int32
}

func (*recoveredLeaseRenewalLossValidator) BeginOperation(context.Context, string, string, int64, string) error {
	return nil
}

func (*recoveredLeaseRenewalLossValidator) RenewOperation(context.Context, string, string, int64) error {
	return nil
}

func (v *recoveredLeaseRenewalLossValidator) EndOperation(context.Context, string, string, int64) error {
	v.endCalls.Add(1)
	return nil
}

func (*recoveredLeaseRenewalLossValidator) AcquireLease(_ context.Context, spaceID, holderID string, _ int64) (*publishlease.Lease, error) {
	return &publishlease.Lease{SpaceID: spaceID, LeaseID: holderID, FencingToken: 9}, nil
}

func (v *recoveredLeaseRenewalLossValidator) RenewLease(context.Context, *publishlease.Lease) error {
	v.renewLeaseCalls.Add(1)
	return errors.New("recovery lease renewal lost")
}

func (*recoveredLeaseRenewalLossValidator) ReleaseLease(context.Context, *publishlease.Lease) error {
	return nil
}

type blockingPublishLeaseValidator struct {
	started chan struct{}
	ended   chan struct{}
}

func (v *blockingPublishLeaseValidator) BeginOperation(context.Context, string, string, int64, string) error {
	close(v.started)
	return nil
}

func (*blockingPublishLeaseValidator) RenewOperation(context.Context, string, string, int64) error {
	return nil
}

func (v *blockingPublishLeaseValidator) EndOperation(context.Context, string, string, int64) error {
	close(v.ended)
	return nil
}

func (*blockingPublishLeaseValidator) AcquireLease(_ context.Context, spaceID, holderID string, _ int64) (*publishlease.Lease, error) {
	return &publishlease.Lease{SpaceID: spaceID, LeaseID: holderID, FencingToken: 8}, nil
}

func (*blockingPublishLeaseValidator) RenewLease(context.Context, *publishlease.Lease) error {
	return nil
}

func (*blockingPublishLeaseValidator) ReleaseLease(context.Context, *publishlease.Lease) error {
	return nil
}

func TestNodeBatchRunnerRetriesProviderTimeoutWithFreshAttemptContext(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	createRunnerBatch(t, catalog, "runner-timeout-retry", 1)
	var calls atomic.Int32
	svc := &Service{
		catalog: catalog,
		executeNodeBatchItem: func(ctx context.Context, _ store.NodeBatchItem) (string, error) {
			if calls.Add(1) < nodeBatchProviderAttempts {
				return "", context.DeadlineExceeded
			}
			if err := ctx.Err(); err != nil {
				return "", fmt.Errorf("retry attempt context already canceled: %w", err)
			}
			return "done after timeout retry", nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, svc.StartNodeBatchRunner(ctx, 1, 100*time.Millisecond))

	aggregate := waitForNodeBatchTerminal(t, catalog, "runner-timeout-retry")
	assert.Equal(t, store.NodeBatchSuccess, aggregate.Job.Status)
	assert.Equal(t, int32(nodeBatchProviderAttempts), calls.Load())
	assert.Equal(t, "done after timeout retry", aggregate.Items[0].ResultSummary)
}

func TestNodeBatchRunnerDoesNotRetryPermanentProviderFailure(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	createRunnerBatch(t, catalog, "runner-provider-permanent", 1)
	var calls atomic.Int32
	svc := &Service{
		catalog: catalog,
		executeNodeBatchItem: func(context.Context, store.NodeBatchItem) (string, error) {
			calls.Add(1)
			return "", errors.New("LimitExceeded.Function: function quota reached")
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, svc.StartNodeBatchRunner(ctx, 1, 100*time.Millisecond))

	aggregate := waitForNodeBatchTerminal(t, catalog, "runner-provider-permanent")
	assert.Equal(t, store.NodeBatchFailed, aggregate.Job.Status)
	assert.Equal(t, int32(1), calls.Load())
}

func TestNodeBatchRunnerRetainsAmbiguousMutationClaimUntilSameItemReconciles(t *testing.T) {
	db := newNodeSCFTestDB(t)
	catalog := store.NewCatalogRepository(db)
	ctx := context.Background()
	jobID := "runner-ambiguous-mutation"
	itemID := jobID + "-000"
	const nodeID = "runner-ambiguous-node"
	require.NoError(t, catalog.UpsertNode(ctx, store.CloudNode{SpaceID: "crypto", NodeID: nodeID, PackageID: "pkg-old"}))
	node, err := catalog.GetNode(ctx, "crypto", nodeID)
	require.NoError(t, err)
	require.NotNil(t, node)
	raw, err := protojson.Marshal(&pb.NodeDeployItem{NodeId: nodeID, LifecycleId: node.LifecycleID, OperationId: itemID})
	require.NoError(t, err)
	require.NoError(t, catalog.CreateNodeBatch(ctx, store.NodeBatchCreate{
		SpaceID: "crypto", JobID: jobID, Operation: nodeBatchOperationDeploy,
		Items: []store.NodeBatchItemCreate{{ItemID: itemID, ItemIndex: 0, NodeID: nodeID, RequestJSON: string(raw)}},
	}))

	var calls atomic.Int32
	svc := &Service{
		catalog: catalog,
		executeNodeBatchItem: func(_ context.Context, item store.NodeBatchItem) (string, error) {
			call := calls.Add(1)
			if err := catalog.ClaimNodeMutation(ctx, "crypto", nodeID, node.LifecycleID, item.ItemID); err != nil {
				return "", err
			}
			if call <= nodeBatchProviderAttempts {
				return "", errors.New("ClientError.NetworkError: connection reset after request submission")
			}
			return "reconciled", nil
		},
	}
	runnerCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, svc.StartNodeBatchRunner(runnerCtx, 1, 100*time.Millisecond))
	require.Eventually(t, func() bool {
		aggregate, getErr := catalog.GetNodeBatch(ctx, "crypto", jobID)
		return getErr == nil && aggregate != nil && aggregate.Items[0].Status == store.NodeBatchReconciliationRequired
	}, 4*time.Second, 10*time.Millisecond)
	require.ErrorIs(t, catalog.ClaimNodeMutation(ctx, "crypto", nodeID, node.LifecycleID, "new-operation"), store.ErrNodeMutationClaimed)

	require.NoError(t, db.Model(&store.NodeBatchItem{}).
		Where("c_space_id = ? AND c_job_id = ? AND c_item_id = ?", "crypto", jobID, itemID).
		Update("c_mtime", time.Now().UTC().Add(-11*time.Second)).Error)
	aggregate := waitForNodeBatchTerminal(t, catalog, jobID)
	assert.Equal(t, store.NodeBatchSuccess, aggregate.Job.Status)
	assert.Equal(t, "reconciled", aggregate.Items[0].ResultSummary)
	assert.True(t, calls.Load() > nodeBatchProviderAttempts)
	require.NoError(t, catalog.ClaimNodeMutation(ctx, "crypto", nodeID, node.LifecycleID, "new-operation"))
}

func TestNodeBatchRunnerDoesNotRetryValidationTimeout(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	createRunnerBatch(t, catalog, "runner-validation-timeout", 1)
	var calls atomic.Int32
	svc := &Service{
		catalog: catalog,
		executeNodeBatchItem: func(context.Context, store.NodeBatchItem) (string, error) {
			calls.Add(1)
			return "", errors.New("validation deadline exceeded for cron")
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, svc.StartNodeBatchRunner(ctx, 1, 100*time.Millisecond))

	aggregate := waitForNodeBatchTerminal(t, catalog, "runner-validation-timeout")
	assert.Equal(t, store.NodeBatchFailed, aggregate.Job.Status)
	assert.Equal(t, int32(1), calls.Load())
}

func TestNodeBatchRunnerStopsWithRuntimeContext(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	createRunnerBatch(t, catalog, "runner-stopped", 1)
	var calls atomic.Int32
	svc := &Service{
		catalog: catalog,
		executeNodeBatchItem: func(context.Context, store.NodeBatchItem) (string, error) {
			calls.Add(1)
			return "done", nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, svc.StartNodeBatchRunner(ctx, 1, 100*time.Millisecond))
	time.Sleep(30 * time.Millisecond)
	assert.Zero(t, calls.Load())
	aggregate, err := catalog.GetNodeBatch(context.Background(), "crypto", "runner-stopped")
	require.NoError(t, err)
	assert.Equal(t, 1, aggregate.PendingCount)
}

func TestNodeBatchRunnerLeavesRunningItemForStartupRecoveryWhenCanceledDuringExecution(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	createRunnerBatch(t, catalog, "runner-canceled-active", 1)
	started := make(chan struct{})
	returned := make(chan struct{})
	svc := &Service{
		catalog: catalog,
		executeNodeBatchItem: func(ctx context.Context, _ store.NodeBatchItem) (string, error) {
			close(started)
			<-ctx.Done()
			close(returned)
			return "", ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, svc.StartNodeBatchRunner(ctx, 1, 100*time.Millisecond))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("node batch item did not start")
	}

	cancel()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("node batch executor did not observe runtime cancellation")
	}

	require.Never(t, func() bool {
		aggregate, err := catalog.GetNodeBatch(context.Background(), "crypto", "runner-canceled-active")
		return err != nil || aggregate == nil ||
			aggregate.PendingCount != 0 || aggregate.RunningCount != 1 ||
			aggregate.SuccessCount != 0 || aggregate.FailedCount != 0
	}, 250*time.Millisecond, 10*time.Millisecond)
}

func TestNodeBatchRunnerRequeuesInterruptedItemsAtStartup(t *testing.T) {
	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	createRunnerBatch(t, catalog, "runner-requeue", 1)
	taken, err := catalog.TakePendingNodeBatchItems(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, taken, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc := &Service{catalog: catalog}

	require.NoError(t, svc.StartNodeBatchRunner(ctx, 1, time.Second))

	aggregate, err := catalog.GetNodeBatch(context.Background(), "crypto", "runner-requeue")
	require.NoError(t, err)
	assert.Equal(t, store.NodeBatchPending, aggregate.Job.Status)
	assert.Equal(t, 1, aggregate.PendingCount)
	assert.Nil(t, aggregate.Items[0].StartedAt)
}

func TestNodeBatchRunnerNeverLogsRequestPayload(t *testing.T) {
	var logs bytes.Buffer
	original := log.GetDefaultLogger()
	log.SetLogger(&bufferLogger{out: &logs})
	t.Cleanup(func() { log.SetLogger(original) })

	catalog := store.NewCatalogRepository(newNodeSCFTestDB(t))
	raw, err := protojson.Marshal(&pb.NodeDeployItem{
		NodeId: "node-a", PackageId: "pkg-a",
		Environment: map[string]string{"SECRET": "must-not-appear-in-logs"},
	})
	require.NoError(t, err)
	require.NoError(t, catalog.CreateNodeBatch(context.Background(), store.NodeBatchCreate{
		SpaceID: "crypto", JobID: "runner-log-redaction", Operation: nodeBatchOperationDeploy,
		Items: []store.NodeBatchItemCreate{{
			ItemID: "item-0", NodeID: "node-a", RequestJSON: string(raw),
		}},
	}))
	svc := &Service{
		catalog: catalog,
		executeNodeBatchItem: func(context.Context, store.NodeBatchItem) (string, error) {
			return "", errors.New("provider failed")
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	require.NoError(t, svc.StartNodeBatchRunner(ctx, 1, 100*time.Millisecond))
	waitForNodeBatchTerminal(t, catalog, "runner-log-redaction")
	assert.NotContains(t, logs.String(), "must-not-appear-in-logs")
	assert.NotContains(t, logs.String(), `"SECRET"`)
}

func createRunnerBatch(t *testing.T, catalog *store.CatalogRepository, jobID string, count int) {
	t.Helper()
	items := make([]store.NodeBatchItemCreate, 0, count)
	for index := range count {
		raw, err := protojson.Marshal(&pb.NodeCreateItem{Region: "local", PackageId: fmt.Sprintf("pkg-%d", index)})
		require.NoError(t, err)
		items = append(items, store.NodeBatchItemCreate{
			ItemID: fmt.Sprintf("%s-%03d", jobID, index), ItemIndex: index,
			NodeID: fmt.Sprintf("node-%03d", index), RequestJSON: string(raw),
		})
	}
	require.NoError(t, catalog.CreateNodeBatch(context.Background(), store.NodeBatchCreate{
		SpaceID: "crypto", JobID: jobID, Operation: nodeBatchOperationCreate, Items: items,
	}))
}

func waitForNodeBatchTerminal(t *testing.T, catalog *store.CatalogRepository, jobID string) *store.NodeBatchAggregate {
	t.Helper()
	var aggregate *store.NodeBatchAggregate
	require.Eventually(t, func() bool {
		var err error
		aggregate, err = catalog.GetNodeBatch(context.Background(), "crypto", jobID)
		return err == nil && aggregate != nil &&
			aggregate.PendingCount == 0 && aggregate.RunningCount == 0
	}, 3*time.Second, 10*time.Millisecond)
	return aggregate
}

type bufferLogger struct {
	out *bytes.Buffer
}

func (l *bufferLogger) write(args ...any) { _, _ = fmt.Fprintln(l.out, args...) }
func (l *bufferLogger) writef(format string, args ...any) {
	_, _ = fmt.Fprintf(l.out, format+"\n", args...)
}
func (l *bufferLogger) Trace(args ...any)                 { l.write(args...) }
func (l *bufferLogger) Tracef(format string, args ...any) { l.writef(format, args...) }
func (l *bufferLogger) Debug(args ...any)                 { l.write(args...) }
func (l *bufferLogger) Debugf(format string, args ...any) { l.writef(format, args...) }
func (l *bufferLogger) Info(args ...any)                  { l.write(args...) }
func (l *bufferLogger) Infof(format string, args ...any)  { l.writef(format, args...) }
func (l *bufferLogger) Warn(args ...any)                  { l.write(args...) }
func (l *bufferLogger) Warnf(format string, args ...any)  { l.writef(format, args...) }
func (l *bufferLogger) Error(args ...any)                 { l.write(args...) }
func (l *bufferLogger) Errorf(format string, args ...any) { l.writef(format, args...) }
func (l *bufferLogger) Fatal(args ...any)                 { l.write(args...) }
func (l *bufferLogger) Fatalf(format string, args ...any) { l.writef(format, args...) }
func (l *bufferLogger) Sync() error                       { return nil }
func (l *bufferLogger) SetLevel(string, log.Level)        {}
func (l *bufferLogger) GetLevel(string) log.Level         { return log.LevelDebug }
func (l *bufferLogger) With(...log.Field) log.Logger      { return l }
