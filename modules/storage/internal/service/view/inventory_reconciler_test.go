package view

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
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

func TestDynamicConsumerIdentityIsStableAndCarriesNoBusiness(t *testing.T) {
	r := newInventoryReconcilerForTest(nil, nil, nil)
	first, err := r.consumerSpec(datasetRef{spaceID: "crypto", datasetID: "dataset_spot_kline_derived_4h"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := r.consumerSpec(datasetRef{spaceID: "crypto", datasetID: "dataset_spot_kline_derived_4h"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := r.consumerSpec(datasetRef{spaceID: "crypto", datasetID: "dataset_factor_x"})
	if err != nil {
		t.Fatal(err)
	}
	if first.durable != again.durable || first.partitionID != again.partitionID {
		t.Fatalf("identity changed between calls: %q/%q vs %q/%q", first.partitionID, first.durable, again.partitionID, again.durable)
	}
	if first.durable == other.durable {
		t.Fatal("different Datasets received the same consumer identity")
	}
	if want := events.StorageViewConsumerName("crypto", "dataset_spot_kline_derived_4h"); first.durable != want || !strings.HasPrefix(first.durable, "view_") {
		t.Fatalf("durable = %q, want %q", first.durable, want)
	}
	if strings.Contains(other.durable, "factor") || strings.Contains(other.durable, "misc") {
		t.Fatalf("消费者名不带业务：%q", other.durable)
	}
}

// 所有数据集共用同一个投递模板（全局并发参数），不再按数据集 ID 前缀选模板。
func TestInventoryReconcilerUsesOneTemplateForEveryDataset(t *testing.T) {
	metadata := &inventoryMetadataFake{views: []*pb.View{
		{SpaceId: "crypto", ViewId: "factor_view_a", Status: "active", DatasetId: "dataset_factor_dynamic_a"},
		{SpaceId: "crypto", ViewId: "misc_view", Status: "active", DatasetId: "dataset_misc_dynamic"},
		{SpaceId: "private_space", ViewId: "other_view", Status: "active", DatasetId: "dataset_other"},
	}}
	var specs []dynamicDatasetConsumerSpec
	reconciler := newInventoryReconcilerForTest(metadata, &syncPointAppenderFake{}, func(_ context.Context, spec dynamicDatasetConsumerSpec) (*dynamicDatasetConsumerBinding, error) {
		specs = append(specs, spec)
		return &dynamicDatasetConsumerBinding{partitionID: spec.partitionID, durable: spec.durable}, nil
	})
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(specs) != 3 {
		t.Fatalf("dynamic bindings = %d, want one per Dataset of every active View", len(specs))
	}
	for _, spec := range specs {
		if spec.config.FetchBatch != 1 || spec.config.MaxWorkers != 1 || spec.config.MaxAckPending != 1 {
			t.Fatalf("Dataset %s delivery budget = %d/%d/%d, want the global 1/1/1", spec.ref.datasetID, spec.config.FetchBatch, spec.config.MaxWorkers, spec.config.MaxAckPending)
		}
		if spec.config.DeliverPolicy != "all" {
			t.Fatalf("new durable must replay history, got deliver policy %q", spec.config.DeliverPolicy)
		}
	}
}

func TestInventoryReconcilerBindsDynamicDatasetAndPublishesRouteReadyOnce(t *testing.T) {
	metadata := &inventoryMetadataFake{views: []*pb.View{{
		SpaceId: "crypto", ViewId: "view_spot_kline_derived_4h", Status: "active",
		DatasetId:  "dataset_spot_kline_derived_4h",
		Attributes: map[string]string{routeReadyRequestIDAttribute: "kline-resample-route:rule-1:7"},
	}}}
	primary := &syncPointAppenderFake{}
	var specs []dynamicDatasetConsumerSpec
	reconciler := newInventoryReconcilerForTest(metadata, primary, func(_ context.Context, spec dynamicDatasetConsumerSpec) (*dynamicDatasetConsumerBinding, error) {
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
	if got := specs[0].ref; got != (datasetRef{spaceID: "crypto", datasetID: "dataset_spot_kline_derived_4h"}) {
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
	if primary.calls[0].GetSpaceId() != "crypto" || marker.GetDatasetId() != "dataset_spot_kline_derived_4h" || marker.GetRequestId() != "kline-resample-route:rule-1:7" || marker.GetSource() != "catchup" {
		t.Fatalf("route-ready request = %#v", primary.calls[0])
	}
}

func TestInventoryReconcilerRetriesFailuresWithoutStoppingHealthyBindings(t *testing.T) {
	metadata := &inventoryMetadataFake{views: []*pb.View{
		{SpaceId: "crypto", ViewId: "existing_view", Status: "active", DatasetId: "existing"},
	}}
	primary := &syncPointAppenderFake{}
	stopped := 0
	bindAttempts := map[string]int{}
	reconciler := newInventoryReconcilerForTest(metadata, primary, func(_ context.Context, spec dynamicDatasetConsumerSpec) (*dynamicDatasetConsumerBinding, error) {
		bindAttempts[spec.ref.datasetID]++
		if spec.ref.datasetID == "failing" {
			return nil, errors.New("bind failed")
		}
		return &dynamicDatasetConsumerBinding{partitionID: spec.partitionID, durable: spec.durable, stop: func() { stopped++ }}, nil
	})
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	metadata.views = append(metadata.views, &pb.View{SpaceId: "crypto", ViewId: "failing_view", Status: "active", DatasetId: "failing"})
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
		SpaceId: "crypto", ViewId: "target_view", Status: "active", DatasetId: "target",
		Attributes: map[string]string{routeReadyRequestIDAttribute: "route-1"},
	}}}
	primary := &syncPointAppenderFake{err: errors.New("primary unavailable")}
	stopped := 0
	binds := 0
	reconciler := newInventoryReconcilerForTest(metadata, primary, func(_ context.Context, spec dynamicDatasetConsumerSpec) (*dynamicDatasetConsumerBinding, error) {
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

func newInventoryReconcilerForTest(metadata ViewInventoryClient, primary DatasetSyncPointAppender, bind dynamicDatasetConsumerBinder) *InventoryReconciler {
	return &InventoryReconciler{
		metadata: metadata,
		primary:  primary,
		auth:     &pb.AuthInfo{AppId: "storage-view", AppKey: "primary-key"},
		template: EventConsumerOptions{
			AckWaitMS: 120000, FetchBatch: 1, MaxWorkers: 1, MaxAckPending: 1, Ordering: "dataset", DeliverPolicy: "new", MaxRetryAttempts: -1,
		},
		bindings: make(map[datasetRef]*dynamicDatasetConsumerBinding),
		bind:     bind,
	}
}

func (r *InventoryReconciler) bindingRefs() []datasetRef {
	r.reconcileMu.Lock()
	defer r.reconcileMu.Unlock()
	refs := make([]datasetRef, 0, len(r.bindings))
	for ref := range r.bindings {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].spaceID != refs[j].spaceID {
			return refs[i].spaceID < refs[j].spaceID
		}
		return refs[i].datasetID < refs[j].datasetID
	})
	return refs
}
