package event

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/pkg/logger"
)

var (
	ErrNotifierClosed = errors.New("notifier is closed")
	ErrNoSubscribers  = errors.New("no subscribers for event type")
)

// Notification 通知消息
type Notification struct {
	Type      string                 `json:"type"`
	Data      interface{}            `json:"data"`
	Metadata  map[string]interface{} `json:"metadata"`
	Timestamp time.Time              `json:"timestamp"`
}

// NotificationHandler 通知处理器
type NotificationHandler func(notification Notification)

// Notifier 通知器接口
type Notifier interface {
	// 订阅通知
	Subscribe(eventType string, handler NotificationHandler) error
	// 取消订阅
	Unsubscribe(eventType string, handler NotificationHandler) error
	// 发布通知
	Publish(eventType string, data interface{}) error
	PublishWithMetadata(eventType string, data interface{}, metadata map[string]interface{}) error
	// 生命周期
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	// 获取统计信息
	GetStats() NotifierStats
}

// NotifierStats 通知器统计信息
type NotifierStats struct {
	PublishedTotal    int64          `json:"published_total"`
	ProcessedTotal    int64          `json:"processed_total"`
	ErrorsTotal       int64          `json:"errors_total"`
	QueueSize         int            `json:"queue_size"`
	SubscribersCount  map[string]int `json:"subscribers_count"`
	LastPublishedTime time.Time      `json:"last_published_time"`
}

// Config 通知器配置
type Config struct {
	BufferSize int `json:"buffer_size" yaml:"buffer_size"`
	Workers    int `json:"workers" yaml:"workers"`
}

// DefaultConfig 默认配置
var DefaultConfig = Config{
	BufferSize: 1000,
	Workers:    4,
}

// memoryNotifier 基于内存的通知器实现
type memoryNotifier struct {
	config Config
	logger logger.Logger

	// 订阅管理
	mu          sync.RWMutex
	subscribers map[string][]NotificationHandler

	// 通知队列
	notifications []Notification
	notifyChan    chan Notification
	closed        bool

	// 工作协程管理
	wg      sync.WaitGroup
	workers int

	// 统计信息
	stats NotifierStats
}

// NewNotifier 创建新的通知器
func NewNotifier(cfg Config, log logger.Logger) Notifier {
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = DefaultConfig.BufferSize
	}
	if cfg.Workers <= 0 {
		cfg.Workers = DefaultConfig.Workers
	}

	return &memoryNotifier{
		config:        cfg,
		logger:        log.With("component", "notifier"),
		subscribers:   make(map[string][]NotificationHandler),
		notifications: make([]Notification, 0),
		notifyChan:    make(chan Notification, cfg.BufferSize),
		workers:       cfg.Workers,
		stats: NotifierStats{
			SubscribersCount: make(map[string]int),
		},
	}
}

// NewDefaultNotifier 创建默认通知器
func NewDefaultNotifier() Notifier {
	return NewNotifier(DefaultConfig, logger.NewDefault())
}

// 预定义通知类型常量
const (
	NotificationTypeTaskCreated   = "task.created"
	NotificationTypeTaskUpdated   = "task.updated"
	NotificationTypeTaskDeleted   = "task.deleted"
	NotificationTypeTaskStarted   = "task.started"
	NotificationTypeTaskCompleted = "task.completed"
	NotificationTypeTaskFailed    = "task.failed"

	NotificationTypeConfigUpdated = "config.updated"
	NotificationTypeConfigSynced  = "config.synced"

	NotificationTypeHeartbeatSent   = "heartbeat.sent"
	NotificationTypeHeartbeatFailed = "heartbeat.failed"

	NotificationTypeCollectorStarted = "collector.started"
	NotificationTypeCollectorStopped = "collector.stopped"
	NotificationTypeCollectorFailed  = "collector.failed"

	NotificationTypeDataCollected = "data.collected"
	NotificationTypeDataStored    = "data.stored"
)

// Subscribe 订阅通知
func (n *memoryNotifier) Subscribe(eventType string, handler NotificationHandler) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.closed {
		return ErrNotifierClosed
	}

	if n.subscribers[eventType] == nil {
		n.subscribers[eventType] = make([]NotificationHandler, 0)
		n.stats.SubscribersCount[eventType] = 0
	}

	n.subscribers[eventType] = append(n.subscribers[eventType], handler)
	n.stats.SubscribersCount[eventType]++

	n.logger.Debug("订阅通知", "event_type", eventType, "subscribers_count", len(n.subscribers[eventType]))
	return nil
}

// Unsubscribe 取消订阅（简化实现，实际项目中可能需要更精确的匹配）
func (n *memoryNotifier) Unsubscribe(eventType string, handler NotificationHandler) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.closed {
		return ErrNotifierClosed
	}

	handlers := n.subscribers[eventType]
	if len(handlers) == 0 {
		return ErrNoSubscribers
	}

	// 注意：函数比较在Go中比较复杂，这里简化处理
	// 实际实现中可能需要使用ID或其他方式来标识handler
	if len(handlers) > 0 {
		n.subscribers[eventType] = handlers[:len(handlers)-1]
		n.stats.SubscribersCount[eventType]--
		if n.stats.SubscribersCount[eventType] <= 0 {
			delete(n.subscribers, eventType)
			delete(n.stats.SubscribersCount, eventType)
		}
	}

	n.logger.Debug("取消订阅通知", "event_type", eventType)
	return nil
}

// Publish 发布通知
func (n *memoryNotifier) Publish(eventType string, data interface{}) error {
	return n.PublishWithMetadata(eventType, data, nil)
}

// PublishWithMetadata 发布带元数据的通知
func (n *memoryNotifier) PublishWithMetadata(eventType string, data interface{}, metadata map[string]interface{}) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.closed {
		return ErrNotifierClosed
	}

	notification := Notification{
		Type:      eventType,
		Data:      data,
		Metadata:  metadata,
		Timestamp: time.Now(),
	}

	// 尝试放入通道
	select {
	case n.notifyChan <- notification:
		n.stats.PublishedTotal++
		n.stats.LastPublishedTime = notification.Timestamp
		n.logger.Debug("通知已发布", "event_type", eventType)
		return nil
	default:
		// 通道已满，存储在内存队列中
		n.notifications = append(n.notifications, notification)
		n.stats.PublishedTotal++
		n.stats.LastPublishedTime = notification.Timestamp
		n.logger.Warn("通知通道已满，存储到内存队列", "event_type", eventType, "queue_size", len(n.notifications))
		return nil
	}
}

// Start 启动通知器
func (n *memoryNotifier) Start(ctx context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.closed {
		return ErrNotifierClosed
	}

	// 启动工作协程
	for i := 0; i < n.workers; i++ {
		n.wg.Add(1)
		go n.worker(i)
	}

	return nil
}

// Stop 停止通知器
func (n *memoryNotifier) Stop(ctx context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.closed {
		return nil
	}

	n.closed = true
	close(n.notifyChan)

	// 等待所有工作协程结束
	done := make(chan struct{})
	go func() {
		n.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}

	return nil
}

// GetStats 获取统计信息
func (n *memoryNotifier) GetStats() NotifierStats {
	n.mu.RLock()
	defer n.mu.RUnlock()

	stats := n.stats
	stats.QueueSize = len(n.notifications)

	// 复制订阅者计数
	stats.SubscribersCount = make(map[string]int)
	for k, v := range n.stats.SubscribersCount {
		stats.SubscribersCount[k] = v
	}

	return stats
}

// worker 工作协程
func (n *memoryNotifier) worker(workerID int) {
	defer n.wg.Done()

	workerLogger := n.logger.With("worker_id", workerID)
	workerLogger.Debug("通知器工作协程已启动")

	for {
		var notification Notification
		var ok bool

		// 首先尝试从通道获取
		select {
		case notification, ok = <-n.notifyChan:
			if !ok {
				workerLogger.Debug("通知器工作协程已停止")
				return
			}
		default:
			// 通道为空，检查内存队列
			n.mu.Lock()
			if len(n.notifications) > 0 {
				notification = n.notifications[0]
				n.notifications = n.notifications[1:]
				n.mu.Unlock()
			} else {
				n.mu.Unlock()
				// 没有通知，短暂休眠后重试
				time.Sleep(10 * time.Millisecond)
				continue
			}
		}

		// 处理通知
		n.handleNotification(notification, workerLogger)
	}
}

// handleNotification 处理通知
func (n *memoryNotifier) handleNotification(notification Notification, workerLogger logger.Logger) {
	n.mu.RLock()
	handlers := n.subscribers[notification.Type]
	n.mu.RUnlock()

	if len(handlers) == 0 {
		workerLogger.Debug("没有订阅者", "event_type", notification.Type)
		return
	}

	// 并发处理所有订阅者
	var wg sync.WaitGroup
	for _, handler := range handlers {
		wg.Add(1)
		go func(h NotificationHandler) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					n.mu.Lock()
					n.stats.ErrorsTotal++
					n.mu.Unlock()
					workerLogger.Error("通知处理器发生panic", "error", r, "event_type", notification.Type)
				}
			}()

			h(notification)

			n.mu.Lock()
			n.stats.ProcessedTotal++
			n.mu.Unlock()
		}(handler)
	}

	wg.Wait()
	workerLogger.Debug("通知处理完成", "event_type", notification.Type, "handlers_count", len(handlers))
}
