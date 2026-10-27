package view

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
)

const routeReadyRequestIDAttribute = "route_ready_request_id"

// ViewInventoryClient is the narrow metadata surface used to discover active
// View input Datasets after process startup.
type ViewInventoryClient interface {
	ListViews(context.Context, *pb.ListViewsReq, ...client.Option) (*pb.ListViewsRsp, error)
}

// DatasetSyncPointAppender appends the fence that proves a newly-bound route is
// visible to the View consumer.
type DatasetSyncPointAppender interface {
	AppendDatasetSyncPoint(context.Context, *pb.AppendDatasetSyncPointReq, ...client.Option) (*pb.AppendDatasetSyncPointRsp, error)
}

// InventoryReconcilerOptions 配置按数据集动态创建的消费者。Consumer 是它们共用的投递模板（全局并发参数）。
type InventoryReconcilerOptions struct {
	Metadata    ViewInventoryClient
	Primary     DatasetSyncPointAppender
	EventClient *jetstream.Client
	Consumer    EventConsumerOptions
	Interval    time.Duration
}

type dynamicDatasetConsumerSpec struct {
	ref         datasetRef
	partitionID string
	durable     string
	filters     []string
	config      EventConsumerOptions
}

type dynamicDatasetConsumerBinding struct {
	partitionID     string
	durable         string
	stop            func()
	routeReady      map[string]struct{}
	consumerState   func(context.Context) (jetstream.ConsumerState, error)
	consumerIsBound func() bool
}

type dynamicDatasetConsumerBinder func(context.Context, dynamicDatasetConsumerSpec) (*dynamicDatasetConsumerBinding, error)

type desiredDynamicDataset struct {
	ref                  datasetRef
	routeReadyRequestIDs map[string]struct{}
}

// InventoryReconciler 为活动 View 引用的每个数据集建一个稳定的 durable（view_<数据集哈希>）。
// 对账失败彼此隔离：一次绑定或路由就绪同步点失败不会停掉已有的健康绑定。
type InventoryReconciler struct {
	service  *Service
	metadata ViewInventoryClient
	primary  DatasetSyncPointAppender
	auth     *pb.AuthInfo
	template EventConsumerOptions
	interval time.Duration
	bindings map[datasetRef]*dynamicDatasetConsumerBinding
	bind     dynamicDatasetConsumerBinder

	reconcileMu sync.Mutex
}

// NewInventoryReconciler validates the dynamic consumer template and returns a
// reconciler ready to run alongside the static exact-route consumers.
func (s *Service) NewInventoryReconciler(opts InventoryReconcilerOptions) (*InventoryReconciler, error) {
	if s == nil {
		return nil, errors.New("View 服务未初始化")
	}
	if opts.Metadata == nil {
		return nil, errors.New("View 清单对账缺少 Metadata 客户端")
	}
	if opts.Primary == nil {
		return nil, errors.New("View 路由就绪同步缺少 Primary 客户端")
	}
	if opts.EventClient == nil {
		return nil, errors.New("View 清单对账缺少 EventBus 客户端")
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	auth := s.primaryAuthSnapshot()
	if auth == nil {
		return nil, errors.New("View 路由就绪同步缺少 Primary 鉴权")
	}
	r := &InventoryReconciler{
		service: s, metadata: opts.Metadata, primary: opts.Primary, auth: auth,
		template: opts.Consumer, interval: interval,
		bindings: make(map[datasetRef]*dynamicDatasetConsumerBinding),
	}
	r.bind = func(ctx context.Context, spec dynamicDatasetConsumerSpec) (*dynamicDatasetConsumerBinding, error) {
		return s.bindDynamicDatasetConsumer(ctx, opts.EventClient, spec)
	}
	return r, nil
}

// Start runs an immediate best-effort pass followed by periodic reconciliation.
// Metadata and binding failures are logged and retried without failing the
// already-running static consumers.
func (r *InventoryReconciler) Start(ctx context.Context) (func(), error) {
	if r == nil {
		return nil, errors.New("View 清单对账器未初始化")
	}
	if ctx == nil {
		return nil, errors.New("View 清单对账缺少上下文")
	}
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		run := func() {
			if err := r.Reconcile(loopCtx); err != nil && loopCtx.Err() == nil {
				log.Printf("View 清单对账失败：%v", err)
			}
		}
		run()
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
			r.stopAll()
		})
	}, nil
}

// Reconcile performs one metadata-to-consumer reconciliation pass.
func (r *InventoryReconciler) Reconcile(ctx context.Context) error {
	if r == nil {
		return errors.New("View 清单对账器未初始化")
	}
	if ctx == nil {
		return errors.New("View 清单对账缺少上下文")
	}
	r.reconcileMu.Lock()
	defer r.reconcileMu.Unlock()

	desired, err := r.loadDesired(ctx)
	if err != nil {
		return err
	}
	refs := make([]datasetRef, 0, len(desired))
	for ref := range desired {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].spaceID != refs[j].spaceID {
			return refs[i].spaceID < refs[j].spaceID
		}
		return refs[i].datasetID < refs[j].datasetID
	})

	var result error
	for _, ref := range refs {
		binding := r.bindings[ref]
		if binding == nil {
			spec, specErr := r.consumerSpec(ref)
			if specErr != nil {
				result = errors.Join(result, specErr)
				continue
			}
			binding, err = r.bind(ctx, spec)
			if err != nil {
				result = errors.Join(result, fmt.Errorf("绑定数据集 %s/%s 的动态 View 消费者失败：%w", ref.spaceID, ref.datasetID, err))
				continue
			}
			if binding == nil {
				result = errors.Join(result, fmt.Errorf("绑定数据集 %s/%s 的动态 View 消费者没有返回绑定", ref.spaceID, ref.datasetID))
				continue
			}
			if binding.routeReady == nil {
				binding.routeReady = make(map[string]struct{})
			}
			r.bindings[ref] = binding
		}
		requestIDs := sortedStringSet(desired[ref].routeReadyRequestIDs)
		for _, requestID := range requestIDs {
			if _, done := binding.routeReady[requestID]; done {
				continue
			}
			if err := r.appendRouteReady(ctx, ref, requestID); err != nil {
				result = errors.Join(result, err)
				continue
			}
			binding.routeReady[requestID] = struct{}{}
		}
	}

	for ref, binding := range r.bindings {
		if _, keep := desired[ref]; keep {
			continue
		}
		if binding != nil && binding.stop != nil {
			binding.stop()
		}
		delete(r.bindings, ref)
	}
	return result
}

func (r *InventoryReconciler) loadDesired(ctx context.Context) (map[datasetRef]desiredDynamicDataset, error) {
	desired := make(map[datasetRef]desiredDynamicDataset)
	for pageNo := uint32(1); ; pageNo++ {
		rsp, err := r.metadata.ListViews(ctx, &pb.ListViewsReq{AuthInfo: r.metadataAuth(), Status: "active", Page: &pb.Page{Page: pageNo, Size: 100}})
		if err != nil {
			return nil, fmt.Errorf("为动态消费者列出活动 View 失败：%w", err)
		}
		if rsp == nil {
			return nil, errors.New("为动态消费者列出活动 View 时返回为空")
		}
		if err := requireSuccess(rsp.GetRetInfo()); err != nil {
			return nil, fmt.Errorf("为动态消费者列出活动 View 失败：%w", err)
		}
		for _, view := range rsp.GetViews() {
			if view == nil {
				continue
			}
			primaryID := strings.TrimSpace(view.GetDatasetId())
			requestID := strings.TrimSpace(view.GetAttributes()[routeReadyRequestIDAttribute])
			for _, datasetID := range viewDatasetIDs(view) {
				ref := datasetRef{spaceID: strings.TrimSpace(view.GetSpaceId()), datasetID: datasetID}
				if ref.spaceID == "" || ref.datasetID == "" {
					continue
				}
				item := desired[ref]
				item.ref = ref
				if item.routeReadyRequestIDs == nil {
					item.routeReadyRequestIDs = make(map[string]struct{})
				}
				if datasetID == primaryID && requestID != "" {
					item.routeReadyRequestIDs[requestID] = struct{}{}
				}
				desired[ref] = item
			}
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() || len(rsp.GetViews()) == 0 {
			return desired, nil
		}
	}
}

func (r *InventoryReconciler) consumerSpec(ref datasetRef) (dynamicDatasetConsumerSpec, error) {
	durable := events.StorageViewConsumerName(ref.spaceID, ref.datasetID)
	partitionID := durable
	registry, err := events.DefaultRegistry()
	if err != nil {
		return dynamicDatasetConsumerSpec{}, err
	}
	filters := make([]string, 0, 4)
	for _, event := range []events.Event{events.DatasetRowsUpserted, events.DatasetPeriodCompleted, events.DatasetSyncPoint} {
		filter, renderErr := registry.RenderSubject(event, ref.spaceID, ref.datasetID)
		if renderErr != nil {
			return dynamicDatasetConsumerSpec{}, fmt.Errorf("生成动态 View 消费者的过滤主题失败：%w", renderErr)
		}
		filters = append(filters, filter)
	}
	config := r.template
	config.PartitionID = partitionID
	config.Consumer = durable
	// A per-Dataset durable is created after the View may already have emitted
	// historical rows. Replay the complete stream slice so no pending sequence
	// is stranded; the durable keeps this policy on subsequent restarts.
	config.DeliverPolicy = "all"
	config.FilterSubjects = append([]string(nil), filters...)
	config.DatasetRoutes = []DatasetRoute{{SpaceID: ref.spaceID, DatasetID: ref.datasetID}}
	return dynamicDatasetConsumerSpec{ref: ref, partitionID: partitionID, durable: durable, filters: filters, config: config}, nil
}

func (r *InventoryReconciler) appendRouteReady(ctx context.Context, ref datasetRef, requestID string) error {
	rsp, err := r.primary.AppendDatasetSyncPoint(ctx, &pb.AppendDatasetSyncPointReq{
		AuthInfo: proto.Clone(r.auth).(*pb.AuthInfo),
		SpaceId:  ref.spaceID,
		SyncPoint: &pb.DatasetSyncPointMarker{
			RequestId: requestID,
			DatasetId: ref.datasetID,
			Source:    "catchup",
		},
	})
	if err != nil {
		return fmt.Errorf("为数据集 %s/%s 追加路由就绪同步点（请求 %q）失败：%w", ref.spaceID, ref.datasetID, requestID, err)
	}
	if rsp == nil {
		return fmt.Errorf("为数据集 %s/%s 追加路由就绪同步点（请求 %q）时返回为空", ref.spaceID, ref.datasetID, requestID)
	}
	if err := requireSuccess(rsp.GetRetInfo()); err != nil {
		return fmt.Errorf("为数据集 %s/%s 追加路由就绪同步点（请求 %q）失败：%w", ref.spaceID, ref.datasetID, requestID, err)
	}
	return nil
}

func (r *InventoryReconciler) metadataAuth() *pb.AuthInfo {
	if r.service != nil {
		return r.service.internalAuth()
	}
	return proto.Clone(r.auth).(*pb.AuthInfo)
}

func (r *InventoryReconciler) stopAll() {
	r.reconcileMu.Lock()
	defer r.reconcileMu.Unlock()
	for ref, binding := range r.bindings {
		if binding != nil && binding.stop != nil {
			binding.stop()
		}
		delete(r.bindings, ref)
	}
}

func sortedStringSet(set map[string]struct{}) []string {
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}
