package view

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/view/eventconsumer"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
)

type EventConsumerOptions = eventconsumer.Config
type DatasetRoute = eventconsumer.DatasetRoute

type dynamicConsumerBoundState struct {
	mu    sync.RWMutex
	bound bool
}

func (s *dynamicConsumerBoundState) set(bound bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.bound = bound
	s.mu.Unlock()
}

func (s *dynamicConsumerBoundState) get() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.bound
}

func (s *Service) StartEventConsumer(ctx context.Context, client *jetstream.Client, configured ...EventConsumerOptions) (func(), error) {
	if s == nil {
		return nil, errors.New("View 服务未初始化")
	}
	opts := EventConsumerOptions{}
	if len(configured) > 0 {
		opts = configured[0]
	}
	if opts.Metrics == nil {
		opts.Metrics = s.metrics
	}
	s.metrics = opts.Metrics
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	// 就绪事件用专用连接发布：发布超时（确认丢失）后只重连这条连接，不影响主连接上的其它用途。
	readyClient, err := client.Fork(ctx, "storage-view-ready")
	if err != nil {
		return nil, fmt.Errorf("为 View 就绪事件发布器建立 EventBus 连接失败：%w", err)
	}
	publisher, err := events.NewPublisher(readyClient, registry)
	if err != nil {
		_ = readyClient.Close()
		return nil, err
	}
	partitionConfigs := opts.PartitionConfigs
	if len(partitionConfigs) == 0 {
		legacy := opts
		legacy.PartitionConfigs = nil
		partitionConfigs = []EventConsumerOptions{legacy}
	}
	for i := range partitionConfigs {
		partitionConfigs[i].PartitionConfigs = nil
		partitionConfigs[i].Metrics = opts.Metrics
		if strings.TrimSpace(partitionConfigs[i].PartitionID) == "" {
			partitionConfigs[i].PartitionID = strings.TrimSpace(partitionConfigs[i].Consumer)
		}
		if partitionConfigs[i].PartitionID == "" {
			partitionConfigs[i].PartitionID = "default"
		}
	}

	type boundState struct {
		mu    sync.RWMutex
		bound bool
	}
	states := make(map[string]func(context.Context) (jetstream.ConsumerState, error), len(partitionConfigs))
	bounds := make(map[string]*boundState, len(partitionConfigs))
	var boundReader func() bool
	var boundReaderMu sync.RWMutex
	stops := make([]func(), 0, len(partitionConfigs))
	partitionClients := make([]*jetstream.Client, 0, len(partitionConfigs))
	stopAll := func() {
		for i := len(stops) - 1; i >= 0; i-- {
			stops[i]()
		}
		for i := len(partitionClients) - 1; i >= 0; i-- {
			_ = partitionClients[i].Close()
		}
		_ = readyClient.Close()
	}
	for _, partition := range partitionConfigs {
		partition := partition
		partitionID := partition.PartitionID
		if len(opts.PartitionConfigs) > 0 && len(partition.FilterSubjects) == 0 {
			// 没有静态路由的分区只为动态的按数据集消费者提供投递模板。不带过滤主题启动它，会订阅所有数据集事件。
			continue
		}
		state := &boundState{}
		bounds[partitionID] = state
		partition.BoundReporter = func(bound bool) {
			state.mu.Lock()
			state.bound = bound
			state.mu.Unlock()
			boundReaderMu.RLock()
			reader := boundReader
			boundReaderMu.RUnlock()
			if reader != nil && opts.Metrics != nil {
				opts.Metrics.SetConsumerBound(reader())
			}
		}
		partitionClient, err := client.Fork(ctx, "storage-view-"+partitionID)
		if err != nil {
			stopAll()
			return nil, fmt.Errorf("为 View 消费分区 %q 建立 EventBus 连接失败：%w", partitionID, err)
		}
		partitionClients = append(partitionClients, partitionClient)
		consumer, err := eventconsumer.New(partitionClient, s, partition)
		if err != nil {
			stopAll()
			return nil, err
		}
		stop, err := consumer.Start(ctx)
		if err != nil {
			stopAll()
			return nil, fmt.Errorf("启动 View 消费分区 %q 失败：%w", partitionID, err)
		}
		stops = append(stops, stop)
		durable := strings.TrimSpace(partition.Consumer)
		states[partitionID] = func(stateCtx context.Context) (jetstream.ConsumerState, error) {
			return partitionClient.ConsumerState(stateCtx, events.DatasetRowsUpserted.Stream(), durable)
		}
	}

	stateReader := func(stateCtx context.Context) (jetstream.ConsumerState, error) {
		// 动态的 View 清单对账可以在维护读取汇总状态的同时注册和注销分区。遍历之前在服务锁内给映射做快照，
		// 闭包就不会与这些更新竞争。
		s.mu.RLock()
		readers := make(map[string]func(context.Context) (jetstream.ConsumerState, error), len(s.consumerStates))
		for partitionID, reader := range s.consumerStates {
			readers[partitionID] = reader
		}
		s.mu.RUnlock()
		var total jetstream.ConsumerState
		for partitionID, reader := range readers {
			state, err := reader(stateCtx)
			if err != nil {
				return jetstream.ConsumerState{}, fmt.Errorf("读取消费分区 %q 的状态失败：%w", partitionID, err)
			}
			if opts.Metrics != nil {
				opts.Metrics.ObserveConsumerPartitionBacklog(partitionID, state.NumPending, uint64(state.NumAckPending))
			}
			total.NumPending += state.NumPending
			total.NumAckPending += state.NumAckPending
		}
		return total, nil
	}
	computedBoundReader := func() bool {
		for _, state := range bounds {
			state.mu.RLock()
			bound := state.bound
			state.mu.RUnlock()
			if !bound {
				return false
			}
		}
		// 动态清单的分区在静态消费者启动之后才注册。把它们的绑定状态也计入汇总的就绪信号，新发现的 View 就不会在它的
		// 专用 durable 挂上之前让服务看起来已经就绪。
		s.mu.RLock()
		dynamicBounds := make(map[string]func() bool, len(s.consumerBounds))
		for partitionID, reader := range s.consumerBounds {
			if _, static := bounds[partitionID]; static {
				continue
			}
			dynamicBounds[partitionID] = reader
		}
		s.mu.RUnlock()
		for _, reader := range dynamicBounds {
			if reader == nil || !reader() {
				return false
			}
		}
		return true
	}
	boundReaderMu.Lock()
	boundReader = computedBoundReader
	boundReaderMu.Unlock()
	s.mu.Lock()
	s.readyPublisher = publisher
	s.readyReconnect = readyClient.Reconnect
	s.consumerStates = states
	s.consumerBounds = make(map[string]func() bool, len(bounds))
	s.consumerPartitionByDataset = make(map[datasetRef]string)
	for _, partition := range partitionConfigs {
		for _, route := range partition.DatasetRoutes {
			key := datasetRef{spaceID: strings.TrimSpace(route.SpaceID), datasetID: strings.TrimSpace(route.DatasetID)}
			if key.spaceID != "" && key.datasetID != "" {
				s.consumerPartitionByDataset[key] = partition.PartitionID
			}
		}
	}
	for partitionID, state := range bounds {
		state := state
		s.consumerBounds[partitionID] = func() bool {
			state.mu.RLock()
			bound := state.bound
			state.mu.RUnlock()
			return bound
		}
	}
	s.consumerState = stateReader
	s.consumerBound = boundReader
	s.mu.Unlock()
	boundReaderMu.RLock()
	reader := boundReader
	boundReaderMu.RUnlock()
	if opts.Metrics != nil && reader != nil {
		opts.Metrics.SetConsumerBound(reader())
	}
	// 就绪事件主要随投递刷新；发布失败或进入退避后，没有新投递的 View 也要有人重试积压的事件。
	retryCtx, stopRetry := context.WithCancel(ctx)
	retryDone := make(chan struct{})
	go func() {
		defer close(retryDone)
		retry := time.NewTicker(readyRetryBackoff)
		defer retry.Stop()
		for {
			select {
			case <-retryCtx.Done():
				return
			case <-retry.C:
			}
			if !s.hasPendingReady() {
				continue
			}
			if err := s.FlushViewDataReady(retryCtx, "", ""); err != nil && retryCtx.Err() == nil {
				log.Printf("View 就绪队列后台重试失败：%v", err)
			}
		}
	}()
	// 写入围栏每 appliedPersistInterval 合并落盘一次。它要一直跑到消费者完全停下：停止时在途投递的收尾（单次写入最长
	// 几十秒）还会推进围栏，停服超时被强杀时也只丢最近一次落盘之后的更新。
	persistCtx, stopPersist := context.WithCancel(context.WithoutCancel(ctx))
	persistDone := make(chan struct{})
	go func() {
		defer close(persistDone)
		ticker := time.NewTicker(appliedPersistInterval)
		defer ticker.Stop()
		for {
			select {
			case <-persistCtx.Done():
				return
			case <-ticker.C:
				s.persistAppliedFenceLogged()
			}
		}
	}()
	stop := func() {
		stopRetry()
		<-retryDone
		stopAll()
		stopPersist()
		<-persistDone
		// 消费已停止，围栏不会再变：落盘最后一次，正常重启不丢任何更新。
		s.persistAppliedFenceLogged()
		if opts.Metrics != nil {
			opts.Metrics.SetConsumerBound(false)
		}
		s.mu.Lock()
		s.readyPublisher = nil
		s.readyReconnect = nil
		s.consumerState = nil
		s.consumerBound = nil
		s.consumerStates = make(map[string]func(context.Context) (jetstream.ConsumerState, error))
		s.consumerBounds = make(map[string]func() bool)
		s.consumerPartitionByDataset = make(map[datasetRef]string)
		s.mu.Unlock()
	}
	// 丢弃旧二进制遗留的重建日志。重建从不从这个目录发出 subject-ready。
	if err := s.ReplayPendingSubjects(ctx); err != nil {
		stop()
		return nil, fmt.Errorf("清理遗留的 View 标的日志失败：%w", err)
	}
	// 补发落盘的就绪事件：失败（EventBus 尚未恢复、发布超时）只记日志，交给退避与后台重试，不能让 View 角色退出、
	// 行事件因此完全停止消费。
	if err := s.FlushViewDataReady(ctx, "", ""); err != nil {
		log.Printf("启动时补发落盘的 View 就绪事件失败，稍后由后台重试：%v", err)
	}
	return stop, nil
}

func (s *Service) bindDynamicDatasetConsumer(ctx context.Context, client *jetstream.Client, spec dynamicDatasetConsumerSpec) (*dynamicDatasetConsumerBinding, error) {
	if s == nil {
		return nil, errors.New("View 服务未初始化")
	}
	if client == nil {
		return nil, errors.New("View 动态消费者缺少 EventBus 客户端")
	}
	if spec.ref.spaceID == "" || spec.ref.datasetID == "" || spec.partitionID == "" || spec.durable == "" {
		return nil, errors.New("View 动态消费者的身份不完整（空间、数据集、分区或 durable 为空）")
	}
	partitionClient, err := client.Fork(ctx, "storage-view-"+spec.partitionID)
	if err != nil {
		return nil, fmt.Errorf("为动态 View 消费者 %q 建立 EventBus 连接失败：%w", spec.partitionID, err)
	}
	state := &dynamicConsumerBoundState{}
	config := spec.config
	config.PartitionConfigs = nil
	config.PartitionID = spec.partitionID
	config.Consumer = spec.durable
	config.FilterSubjects = append([]string(nil), spec.filters...)
	config.DatasetRoutes = []DatasetRoute{{SpaceID: spec.ref.spaceID, DatasetID: spec.ref.datasetID}}
	config.Metrics = s.metrics
	config.BoundReporter = state.set
	consumer, err := eventconsumer.New(partitionClient, s, config)
	if err != nil {
		_ = partitionClient.Close()
		return nil, err
	}
	stopConsumer, err := consumer.Start(ctx)
	if err != nil {
		_ = partitionClient.Close()
		return nil, fmt.Errorf("启动动态 View 消费者 %q 失败：%w", spec.partitionID, err)
	}
	binding := &dynamicDatasetConsumerBinding{
		partitionID:     spec.partitionID,
		durable:         spec.durable,
		consumerIsBound: state.get,
		consumerState: func(stateCtx context.Context) (jetstream.ConsumerState, error) {
			return partitionClient.ConsumerState(stateCtx, events.DatasetRowsUpserted.Stream(), spec.durable)
		},
	}
	if err := s.registerDynamicConsumerPartition(spec.ref, binding); err != nil {
		stopConsumer()
		_ = partitionClient.Close()
		return nil, err
	}
	var stopOnce sync.Once
	binding.stop = func() {
		stopOnce.Do(func() {
			s.unregisterDynamicConsumerPartition(spec.ref, spec.partitionID)
			stopConsumer()
			_ = partitionClient.Close()
		})
	}
	return binding, nil
}

func (s *Service) registerDynamicConsumerPartition(ref datasetRef, binding *dynamicDatasetConsumerBinding) error {
	if binding == nil || binding.partitionID == "" || binding.consumerState == nil || binding.consumerIsBound == nil {
		return errors.New("View 动态消费者的绑定不完整")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.consumerPartitionByDataset[ref]; current != "" && current != binding.partitionID {
		return fmt.Errorf("数据集 %s/%s 已分配给消费分区 %q", ref.spaceID, ref.datasetID, current)
	}
	if _, exists := s.consumerStates[binding.partitionID]; exists {
		return fmt.Errorf("View 消费分区 %q 已经登记过", binding.partitionID)
	}
	if s.consumerStates == nil {
		s.consumerStates = make(map[string]func(context.Context) (jetstream.ConsumerState, error))
	}
	if s.consumerBounds == nil {
		s.consumerBounds = make(map[string]func() bool)
	}
	if s.consumerPartitionByDataset == nil {
		s.consumerPartitionByDataset = make(map[datasetRef]string)
	}
	s.consumerStates[binding.partitionID] = binding.consumerState
	s.consumerBounds[binding.partitionID] = binding.consumerIsBound
	s.consumerPartitionByDataset[ref] = binding.partitionID
	return nil
}

func (s *Service) unregisterDynamicConsumerPartition(ref datasetRef, partitionID string) {
	if s == nil || partitionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.consumerPartitionByDataset[ref] != partitionID {
		return
	}
	delete(s.consumerPartitionByDataset, ref)
	delete(s.consumerStates, partitionID)
	delete(s.consumerBounds, partitionID)
}
