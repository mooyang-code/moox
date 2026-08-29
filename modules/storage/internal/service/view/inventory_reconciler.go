package view

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// InventoryReconcilerOptions configures dynamic catch-all consumers. Consumer
// must be the same normalized partition topology used by StartEventConsumer.
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

// InventoryReconciler adds one stable durable per active View Dataset not
// claimed by an exact configured route. Reconcile failures are isolated: a
// failed addition or route-ready fence never stops an existing healthy binding.
type InventoryReconciler struct {
	service       *Service
	metadata      ViewInventoryClient
	primary       DatasetSyncPointAppender
	auth          *pb.AuthInfo
	exact         map[datasetRef]struct{}
	dynamicExact  map[datasetRef]struct{}
	allowedSpaces map[string]struct{}
	misc          EventConsumerOptions
	interval      time.Duration
	bindings      map[datasetRef]*dynamicDatasetConsumerBinding
	bind          dynamicDatasetConsumerBinder

	reconcileMu sync.Mutex
}

// NewInventoryReconciler validates the dynamic consumer template and returns a
// reconciler ready to run alongside the static exact-route consumers.
func (s *Service) NewInventoryReconciler(opts InventoryReconcilerOptions) (*InventoryReconciler, error) {
	if s == nil {
		return nil, errors.New("storage view service is nil")
	}
	if opts.Metadata == nil {
		return nil, errors.New("storage view inventory metadata client is required")
	}
	if opts.Primary == nil {
		return nil, errors.New("storage view route-ready Primary client is required")
	}
	if opts.EventClient == nil {
		return nil, errors.New("storage view inventory EventBus client is required")
	}
	misc, exact, dynamicExact, allowedSpaces, err := dynamicConsumerTemplate(opts.Consumer)
	if err != nil {
		return nil, err
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	auth := s.primaryAuthSnapshot()
	if auth == nil {
		return nil, errors.New("storage view Primary auth is required for route-ready")
	}
	r := &InventoryReconciler{
		service: s, metadata: opts.Metadata, primary: opts.Primary, auth: auth,
		exact: exact, dynamicExact: dynamicExact, allowedSpaces: allowedSpaces, misc: misc, interval: interval,
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
		return nil, errors.New("storage view inventory reconciler is nil")
	}
	if ctx == nil {
		return nil, errors.New("storage view inventory context is required")
	}
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		run := func() {
			if err := r.Reconcile(loopCtx); err != nil && loopCtx.Err() == nil {
				log.Printf("storage view inventory reconcile failed: %v", err)
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
		return errors.New("storage view inventory reconciler is nil")
	}
	if ctx == nil {
		return errors.New("storage view inventory context is required")
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
		if _, exact := r.exact[ref]; exact {
			// Static exact routes are already consumed by the main consumer. They
			// still need the same route-ready fence so Catalog can wait for the
			// route after a restart or a concurrent first-time provision.
			for _, requestID := range sortedStringSet(desired[ref].routeReadyRequestIDs) {
				if err := r.appendRouteReady(ctx, ref, requestID); err != nil {
					result = errors.Join(result, err)
				}
			}
			continue
		}
		binding := r.bindings[ref]
		if binding == nil {
			spec, specErr := r.consumerSpec(ref)
			if specErr != nil {
				result = errors.Join(result, specErr)
				continue
			}
			binding, err = r.bind(ctx, spec)
			if err != nil {
				result = errors.Join(result, fmt.Errorf("bind dynamic View consumer %s/%s: %w", ref.spaceID, ref.datasetID, err))
				continue
			}
			if binding == nil {
				result = errors.Join(result, fmt.Errorf("bind dynamic View consumer %s/%s returned nil binding", ref.spaceID, ref.datasetID))
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
			return nil, fmt.Errorf("list active Views for dynamic consumers: %w", err)
		}
		if rsp == nil {
			return nil, errors.New("list active Views for dynamic consumers returned nil response")
		}
		if err := requireSuccess(rsp.GetRetInfo()); err != nil {
			return nil, fmt.Errorf("list active Views for dynamic consumers: %w", err)
		}
		for _, view := range rsp.GetViews() {
			if view == nil {
				continue
			}
			primaryID := strings.TrimSpace(view.GetPrimaryDatasetId())
			requestID := strings.TrimSpace(view.GetAttributes()[routeReadyRequestIDAttribute])
			for _, datasetID := range viewDatasetIDs(view) {
				ref := datasetRef{spaceID: strings.TrimSpace(view.GetSpaceId()), datasetID: datasetID}
				if ref.spaceID == "" || ref.datasetID == "" {
					continue
				}
				if _, exact := r.exact[ref]; !exact {
					_, allowedSpace := r.allowedSpaces[ref.spaceID]
					_, allowedExact := r.dynamicExact[ref]
					if !allowedSpace && !allowedExact {
						continue
					}
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
	partitionID, durable := dynamicDatasetConsumerIdentity(r.misc.Consumer, ref)
	registry, err := events.DefaultRegistry()
	if err != nil {
		return dynamicDatasetConsumerSpec{}, err
	}
	filters := make([]string, 0, 4)
	for _, event := range []events.Event{events.DatasetRowsUpserted, events.DatasetPeriodCollected, events.FactorPeriodComputed, events.DatasetSyncPoint} {
		filter, renderErr := registry.RenderSubject(event, ref.spaceID, ref.datasetID)
		if renderErr != nil {
			return dynamicDatasetConsumerSpec{}, fmt.Errorf("render dynamic View consumer filter: %w", renderErr)
		}
		filters = append(filters, filter)
	}
	config := r.misc
	config.PartitionConfigs = nil
	config.PartitionID = partitionID
	config.Consumer = durable
	// A per-Dataset durable is created after the View may already have emitted
	// historical rows. Replay the complete stream slice so replacing the legacy
	// catch-all durable cannot strand its pending sequence. The durable keeps
	// this policy on subsequent restarts, making the migration idempotent.
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
		return fmt.Errorf("append route-ready sync point for %s/%s request %q: %w", ref.spaceID, ref.datasetID, requestID, err)
	}
	if rsp == nil {
		return fmt.Errorf("append route-ready sync point for %s/%s request %q returned nil response", ref.spaceID, ref.datasetID, requestID)
	}
	if err := requireSuccess(rsp.GetRetInfo()); err != nil {
		return fmt.Errorf("append route-ready sync point for %s/%s request %q: %w", ref.spaceID, ref.datasetID, requestID, err)
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

func dynamicConsumerTemplate(options EventConsumerOptions) (EventConsumerOptions, map[datasetRef]struct{}, map[datasetRef]struct{}, map[string]struct{}, error) {
	if len(options.PartitionConfigs) == 0 {
		return EventConsumerOptions{}, nil, nil, nil, errors.New("storage view dynamic consumers require partition configuration")
	}
	exact := make(map[datasetRef]struct{})
	dynamicExact := make(map[datasetRef]struct{})
	allowedSpaces := make(map[string]struct{}, len(options.AllowedDatasetSpaces))
	for _, spaceID := range options.AllowedDatasetSpaces {
		if spaceID = strings.TrimSpace(spaceID); spaceID != "" {
			allowedSpaces[spaceID] = struct{}{}
		}
	}
	var misc EventConsumerOptions
	for _, partition := range options.PartitionConfigs {
		if strings.TrimSpace(partition.Consumer) == events.StorageViewMiscConsumer {
			misc = partition
			for _, route := range partition.DatasetRoutes {
				spaceID := strings.TrimSpace(route.SpaceID)
				if spaceID != "" && strings.TrimSpace(route.DatasetID) == "*" {
					allowedSpaces[spaceID] = struct{}{}
				}
				if spaceID != "" && strings.TrimSpace(route.DatasetID) != "" && strings.TrimSpace(route.DatasetID) != "*" {
					dynamicExact[datasetRef{spaceID: spaceID, datasetID: strings.TrimSpace(route.DatasetID)}] = struct{}{}
				}
			}
			// Every route in the misc partition is reconciled dynamically, including
			// its non-wildcard legacy routes. Only partitions that remain static
			// are excluded from dynamic bindings.
			continue
		}
		for _, route := range partition.DatasetRoutes {
			spaceID := strings.TrimSpace(route.SpaceID)
			datasetID := strings.TrimSpace(route.DatasetID)
			if spaceID != "" && datasetID != "" && datasetID != "*" {
				exact[datasetRef{spaceID: spaceID, datasetID: datasetID}] = struct{}{}
			}
		}
	}
	if strings.TrimSpace(misc.Consumer) == "" {
		return EventConsumerOptions{}, nil, nil, nil, errors.New("storage view misc consumer partition is required")
	}
	return misc, exact, dynamicExact, allowedSpaces, nil
}

func datasetRouteSet(routes []DatasetRoute) map[datasetRef]struct{} {
	set := make(map[datasetRef]struct{}, len(routes))
	for _, route := range routes {
		spaceID := strings.TrimSpace(route.SpaceID)
		datasetID := strings.TrimSpace(route.DatasetID)
		if spaceID != "" && datasetID != "" && datasetID != "*" {
			set[datasetRef{spaceID: spaceID, datasetID: datasetID}] = struct{}{}
		}
	}
	return set
}

func dynamicDatasetConsumerIdentity(miscDurable string, ref datasetRef) (string, string) {
	hash := sha256.Sum256([]byte(strings.TrimSpace(ref.spaceID) + "\x00" + strings.TrimSpace(ref.datasetID)))
	token := hex.EncodeToString(hash[:])[:16]
	// NATS durable names accept alphanumeric, '-' and '_' characters but not
	// the dot separator used by the human-readable identity in early drafts.
	// Keep the partition label descriptive while making the server-side durable
	// valid and stable across restarts.
	// JetStream limits durable names to 32 characters. Keep the full token in
	// the local partition identity while using a 14-character suffix for the
	// default 17-character storage_view_misc durable prefix.
	durableToken := token
	if len(durableToken) > 14 {
		durableToken = durableToken[:14]
	}
	return "misc:" + token, strings.TrimSpace(miscDurable) + "-" + durableToken
}

func viewDatasetIDs(view *pb.View) []string {
	if view == nil {
		return nil
	}
	set := make(map[string]struct{})
	for _, datasetID := range append(append([]string(nil), view.GetDatasetIds()...), view.GetPrimaryDatasetId()) {
		if datasetID = strings.TrimSpace(datasetID); datasetID != "" {
			set[datasetID] = struct{}{}
		}
	}
	for _, column := range view.GetColumns() {
		if column == nil {
			continue
		}
		if datasetID, _, ok := strings.Cut(strings.TrimSpace(column.GetOriginId()), "."); ok {
			if datasetID = strings.TrimSpace(datasetID); datasetID != "" {
				set[datasetID] = struct{}{}
			}
		}
	}
	return sortedStringSet(set)
}

func sortedStringSet(set map[string]struct{}) []string {
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}
