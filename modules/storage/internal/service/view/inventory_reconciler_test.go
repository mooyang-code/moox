package view

import (
	"context"
	"errors"
	"slices"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/client"
)

type inventoryMetadataFake struct {
	views []*pb.View
	err   error
}

func (f *inventoryMetadataFake) ListViews(context.Context, *pb.ListViewsReq, ...client.Option) (*pb.ListViewsRsp, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &pb.ListViewsRsp{RetInfo: successRetInfo(), Views: f.views}, nil
}

type syncPointAppenderFake struct {
	calls []*pb.AppendDatasetSyncPointReq
	err   error
}

func (f *syncPointAppenderFake) AppendDatasetSyncPoint(_ context.Context, req *pb.AppendDatasetSyncPointReq, _ ...client.Option) (*pb.AppendDatasetSyncPointRsp, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return nil, f.err
	}
	return &pb.AppendDatasetSyncPointRsp{RetInfo: successRetInfo(), EventId: "sync-event"}, nil
}

func TestDynamicDatasetConsumerIdentityIsStable(t *testing.T) {
	partitionID, durable := dynamicDatasetConsumerIdentity("storage_view_misc", datasetRef{spaceID: "crypto", datasetID: "spot_kline_derived_4h"})
	if partitionID != "misc_b5051da607b133e5" || durable != "storage_view_misc_b5051da607b1" {
		t.Fatalf("identity = %q/%q", partitionID, durable)
	}
	otherPartitionID, _ := dynamicDatasetConsumerIdentity("storage_view_misc", datasetRef{spaceID: "crypto", datasetID: "spot_kline_derived_6h"})
	if otherPartitionID == partitionID {
		t.Fatal("different Datasets received the same consumer identity")
	}
}

func TestInventoryReconcilerBindsDynamicDatasetAndPublishesRouteReadyOnce(t *testing.T) {
	metadata := &inventoryMetadataFake{views: []*pb.View{{
		SpaceId: "crypto", ViewId: "spot_kline_derived_4h_view", Status: "active",
		PrimaryDatasetId: "spot_kline_derived_4h", DatasetIds: []string{"spot_kline_derived_4h"},
		Attributes: map[string]string{routeReadyRequestIDAttribute: "kline-resample-route:rule-1:7"},
	}}}
	primary := &syncPointAppenderFake{}
	var specs []dynamicDatasetConsumerSpec
	reconciler := newInventoryReconcilerForTest(metadata, primary, []DatasetRoute{{SpaceID: "crypto", DatasetID: "binance_spot_kline_1m"}}, func(_ context.Context, spec dynamicDatasetConsumerSpec) (*dynamicDatasetConsumerBinding, error) {
		specs = append(specs, spec)
		return &dynamicDatasetConsumerBinding{partitionID: spec.partitionID, durable: spec.durable}, nil
	})

	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 {
		t.Fatalf("bind calls = %d, want 1", len(specs))
	}
	if got := specs[0].ref; got != (datasetRef{spaceID: "crypto", datasetID: "spot_kline_derived_4h"}) {
		t.Fatalf("bound Dataset = %#v", got)
	}
	if len(specs[0].filters) != 4 {
		t.Fatalf("filters = %#v, want four event families", specs[0].filters)
	}
	if specs[0].config.DeliverPolicy != "all" {
		t.Fatalf("dynamic deliver policy = %q, want all for migration replay", specs[0].config.DeliverPolicy)
	}
	if len(primary.calls) != 1 {
		t.Fatalf("route-ready calls = %d, want 1", len(primary.calls))
	}
	marker := primary.calls[0].GetSyncPoint()
	if primary.calls[0].GetSpaceId() != "crypto" || marker.GetDatasetId() != "spot_kline_derived_4h" || marker.GetRequestId() != "kline-resample-route:rule-1:7" || marker.GetSource() != "catchup" {
		t.Fatalf("route-ready request = %#v", primary.calls[0])
	}
}

func TestInventoryReconcilerRetriesFailuresWithoutStoppingHealthyBindings(t *testing.T) {
	metadata := &inventoryMetadataFake{views: []*pb.View{
		{SpaceId: "crypto", ViewId: "existing_view", Status: "active", PrimaryDatasetId: "existing", DatasetIds: []string{"existing"}},
	}}
	primary := &syncPointAppenderFake{}
	stopped := 0
	bindAttempts := map[string]int{}
	reconciler := newInventoryReconcilerForTest(metadata, primary, nil, func(_ context.Context, spec dynamicDatasetConsumerSpec) (*dynamicDatasetConsumerBinding, error) {
		bindAttempts[spec.ref.datasetID]++
		if spec.ref.datasetID == "failing" {
			return nil, errors.New("bind failed")
		}
		return &dynamicDatasetConsumerBinding{partitionID: spec.partitionID, durable: spec.durable, stop: func() { stopped++ }}, nil
	})
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	metadata.views = append(metadata.views, &pb.View{SpaceId: "crypto", ViewId: "failing_view", Status: "active", PrimaryDatasetId: "failing", DatasetIds: []string{"failing"}})
	if err := reconciler.Reconcile(context.Background()); err == nil {
		t.Fatal("bind failure was not reported")
	}
	if stopped != 0 || bindAttempts["existing"] != 1 {
		t.Fatalf("healthy binding changed: stopped=%d attempts=%v", stopped, bindAttempts)
	}
	if err := reconciler.Reconcile(context.Background()); err == nil || bindAttempts["failing"] != 2 {
		t.Fatalf("failed binding was not retried: attempts=%v err=%v", bindAttempts, err)
	}
	metadata.err = errors.New("metadata unavailable")
	if err := reconciler.Reconcile(context.Background()); err == nil {
		t.Fatal("metadata failure was not reported")
	}
	if stopped != 0 || bindAttempts["existing"] != 1 {
		t.Fatalf("metadata failure changed healthy binding: stopped=%d attempts=%v", stopped, bindAttempts)
	}
}

func TestInventoryReconcilerRetriesRouteReadyAndStopsRemovedDataset(t *testing.T) {
	metadata := &inventoryMetadataFake{views: []*pb.View{{
		SpaceId: "crypto", ViewId: "target_view", Status: "active", PrimaryDatasetId: "target", DatasetIds: []string{"target"},
		Attributes: map[string]string{routeReadyRequestIDAttribute: "route-1"},
	}}}
	primary := &syncPointAppenderFake{err: errors.New("primary unavailable")}
	stopped := 0
	binds := 0
	reconciler := newInventoryReconcilerForTest(metadata, primary, nil, func(_ context.Context, spec dynamicDatasetConsumerSpec) (*dynamicDatasetConsumerBinding, error) {
		binds++
		return &dynamicDatasetConsumerBinding{partitionID: spec.partitionID, durable: spec.durable, stop: func() { stopped++ }}, nil
	})

	if err := reconciler.Reconcile(context.Background()); err == nil {
		t.Fatal("route-ready failure was not reported")
	}
	primary.err = nil
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if binds != 1 || len(primary.calls) != 2 {
		t.Fatalf("binds=%d route-ready calls=%d, want 1/2", binds, len(primary.calls))
	}

	metadata.views = nil
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stopped != 1 || len(reconciler.bindingRefs()) != 0 {
		t.Fatalf("removed binding stopped=%d refs=%v", stopped, reconciler.bindingRefs())
	}
}

func TestInventoryReconcilerLeavesExactRoutesOnSharedConsumers(t *testing.T) {
	metadata := &inventoryMetadataFake{views: []*pb.View{{SpaceId: "crypto", ViewId: "kline_view", Status: "active", PrimaryDatasetId: "binance_spot_kline_1m", DatasetIds: []string{"binance_spot_kline_1m"}}}}
	binds := 0
	reconciler := newInventoryReconcilerForTest(metadata, &syncPointAppenderFake{}, []DatasetRoute{{SpaceID: "crypto", DatasetID: "binance_spot_kline_1m"}}, func(context.Context, dynamicDatasetConsumerSpec) (*dynamicDatasetConsumerBinding, error) {
		binds++
		return nil, nil
	})
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if binds != 0 || !slices.Equal(reconciler.bindingRefs(), nil) {
		t.Fatalf("exact route was dynamically bound: binds=%d refs=%v", binds, reconciler.bindingRefs())
	}
}

func TestInventoryReconcilerHonorsWildcardSpaceAllowList(t *testing.T) {
	metadata := &inventoryMetadataFake{views: []*pb.View{
		{SpaceId: "crypto", ViewId: "allowed_view", Status: "active", PrimaryDatasetId: "allowed", DatasetIds: []string{"allowed"}},
		{SpaceId: "private_space", ViewId: "blocked_view", Status: "active", PrimaryDatasetId: "blocked", DatasetIds: []string{"blocked"}},
	}}
	var bound []datasetRef
	reconciler := newInventoryReconcilerForTest(metadata, &syncPointAppenderFake{}, nil, func(_ context.Context, spec dynamicDatasetConsumerSpec) (*dynamicDatasetConsumerBinding, error) {
		bound = append(bound, spec.ref)
		return &dynamicDatasetConsumerBinding{}, nil
	})
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(bound) != 1 || bound[0] != (datasetRef{spaceID: "crypto", datasetID: "allowed"}) {
		t.Fatalf("dynamic bindings = %#v, want only crypto/allowed", bound)
	}
}

func TestInventoryReconcilerKeepsExactRouteOutsideWildcardSpace(t *testing.T) {
	metadata := &inventoryMetadataFake{views: []*pb.View{{
		SpaceId: "moox_system", ViewId: "metrics_view", Status: "active", PrimaryDatasetId: "moox_service_metrics", DatasetIds: []string{"moox_service_metrics"},
		Attributes: map[string]string{routeReadyRequestIDAttribute: "route-exact"},
	}}}
	primary := &syncPointAppenderFake{}
	binds := 0
	reconciler := newInventoryReconcilerForTest(metadata, primary, []DatasetRoute{{SpaceID: "moox_system", DatasetID: "moox_service_metrics"}}, func(context.Context, dynamicDatasetConsumerSpec) (*dynamicDatasetConsumerBinding, error) {
		binds++
		return nil, nil
	})
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if binds != 0 || len(primary.calls) != 1 {
		t.Fatalf("exact route outside wildcard space: binds=%d route-ready calls=%d", binds, len(primary.calls))
	}
}

func TestDynamicConsumerTemplateTreatsMiscExactRoutesAsDynamic(t *testing.T) {
	opts := EventConsumerOptions{PartitionConfigs: []EventConsumerOptions{
		{PartitionID: "kline", Consumer: "storage_view_kline", DatasetRoutes: []DatasetRoute{{SpaceID: "crypto", DatasetID: "binance_spot_kline_1m"}}},
		{PartitionID: "misc", Consumer: "storage_view_misc", DatasetRoutes: []DatasetRoute{{SpaceID: "stock_cn", DatasetID: "stock_cn_kline"}, {SpaceID: "crypto", DatasetID: "*"}}},
	}}
	_, exact, dynamicExact, allowed, err := dynamicConsumerTemplate(opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := exact[datasetRef{spaceID: "crypto", datasetID: "binance_spot_kline_1m"}]; !ok {
		t.Fatalf("static kline route missing from exact set: %#v", exact)
	}
	if _, ok := exact[datasetRef{spaceID: "stock_cn", datasetID: "stock_cn_kline"}]; ok {
		t.Fatalf("misc exact route incorrectly excluded from dynamic set: %#v", exact)
	}
	if _, ok := allowed["crypto"]; !ok {
		t.Fatalf("wildcard space missing from allow list: %#v", allowed)
	}
	if _, ok := dynamicExact[datasetRef{spaceID: "stock_cn", datasetID: "stock_cn_kline"}]; !ok {
		t.Fatalf("misc exact route missing from dynamic exact set: %#v", dynamicExact)
	}
}

func TestInventoryReconcilerBindsMiscExactRouteOutsideWildcardSpace(t *testing.T) {
	metadata := &inventoryMetadataFake{views: []*pb.View{{
		SpaceId: "stock_cn", ViewId: "stock_view", Status: "active", PrimaryDatasetId: "stock_cn_kline", DatasetIds: []string{"stock_cn_kline"},
	}}}
	template := EventConsumerOptions{PartitionConfigs: []EventConsumerOptions{{
		Consumer: "storage_view_misc", DatasetRoutes: []DatasetRoute{{SpaceID: "stock_cn", DatasetID: "stock_cn_kline"}, {SpaceID: "crypto", DatasetID: "*"}},
	}}}
	_, _, dynamicExact, allowed, err := dynamicConsumerTemplate(template)
	if err != nil {
		t.Fatal(err)
	}
	var bound []datasetRef
	reconciler := &InventoryReconciler{
		metadata: metadata, primary: &syncPointAppenderFake{}, dynamicExact: dynamicExact, allowedSpaces: allowed,
		misc: EventConsumerOptions{Consumer: "storage_view_misc", DeliverPolicy: "new"}, bindings: make(map[datasetRef]*dynamicDatasetConsumerBinding),
		bind: func(_ context.Context, spec dynamicDatasetConsumerSpec) (*dynamicDatasetConsumerBinding, error) {
			bound = append(bound, spec.ref)
			return &dynamicDatasetConsumerBinding{}, nil
		},
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(bound) != 1 || bound[0] != (datasetRef{spaceID: "stock_cn", datasetID: "stock_cn_kline"}) {
		t.Fatalf("dynamic bindings = %#v, want stock_cn/stock_cn_kline", bound)
	}
}

func TestInventoryReconcilerRejectsUnconfiguredMiscExactDataset(t *testing.T) {
	metadata := &inventoryMetadataFake{views: []*pb.View{{
		SpaceId: "stock_cn", ViewId: "unconfigured_view", Status: "active", PrimaryDatasetId: "unconfigured", DatasetIds: []string{"unconfigured"},
	}}}
	template := EventConsumerOptions{PartitionConfigs: []EventConsumerOptions{{
		Consumer: "storage_view_misc", DatasetRoutes: []DatasetRoute{{SpaceID: "stock_cn", DatasetID: "stock_cn_kline"}},
	}}}
	_, _, dynamicExact, allowed, err := dynamicConsumerTemplate(template)
	if err != nil {
		t.Fatal(err)
	}
	bound := 0
	reconciler := &InventoryReconciler{
		metadata: metadata, primary: &syncPointAppenderFake{}, dynamicExact: dynamicExact, allowedSpaces: allowed,
		misc: EventConsumerOptions{Consumer: "storage_view_misc"}, bindings: make(map[datasetRef]*dynamicDatasetConsumerBinding),
		bind: func(context.Context, dynamicDatasetConsumerSpec) (*dynamicDatasetConsumerBinding, error) {
			bound++
			return &dynamicDatasetConsumerBinding{}, nil
		},
	}
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if bound != 0 {
		t.Fatalf("unconfigured misc Dataset was dynamically bound: %d", bound)
	}
}

func newInventoryReconcilerForTest(metadata ViewInventoryClient, primary DatasetSyncPointAppender, exact []DatasetRoute, bind dynamicDatasetConsumerBinder) *InventoryReconciler {
	allowedSpaces := map[string]struct{}{"crypto": {}}
	return &InventoryReconciler{
		metadata:      metadata,
		primary:       primary,
		auth:          &pb.AuthInfo{AppId: "storage-view", AppKey: "primary-key"},
		exact:         datasetRouteSet(exact),
		allowedSpaces: allowedSpaces,
		misc: EventConsumerOptions{
			PartitionID: "misc", Consumer: "storage_view_misc", AckWaitMS: 120000, FetchBatch: 4,
			MaxWorkers: 2, MaxAckPending: 16, Ordering: "dataset", DeliverPolicy: "new", MaxRetryAttempts: -1,
			AllowedDatasetSpaces: []string{"crypto"},
		},
		bindings: make(map[datasetRef]*dynamicDatasetConsumerBinding),
		bind:     bind,
	}
}
