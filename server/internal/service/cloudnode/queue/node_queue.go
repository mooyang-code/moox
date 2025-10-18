package queue

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/cloudnode/model"

	"trpc.group/trpc-go/trpc-go/log"
)

var (
	ErrQueueClosed = errors.New("queue is closed")
	ErrQueueEmpty  = errors.New("queue is empty")
)

// NodeCreationMessage 表示异步节点创建的消息
type NodeCreationMessage struct {
	NodeID         string         `json:"node_id"`
	CloudAccountID string         `json:"cloud_account_id"`
	PackageID      int64          `json:"package_id"` // 代码包ID
	Namespace      string         `json:"namespace"`
	Region         string         `json:"region"`
	NodeType       string         `json:"node_type"`
	FunctionName   string         `json:"function_name"`
	ZipFilePath    string         `json:"zip_file_path"`
	TaskID         string         `json:"task_id"`
	ItemID         string         `json:"item_id"`
	NodeData       *model.SCFNode `json:"node_data"`
	CreatedAt      time.Time      `json:"created_at"`
}

// NodeDeletionMessage 表示异步节点删除的消息
type NodeDeletionMessage struct {
	NodeID         string    `json:"node_id"`
	CloudAccountID string    `json:"cloud_account_id"`
	Namespace      string    `json:"namespace"`
	Region         string    `json:"region"`
	FunctionName   string    `json:"function_name"`
	TaskID         string    `json:"task_id,omitempty"` // 关联的异步任务ID
	ItemID         string    `json:"item_id,omitempty"` // 任务中的项目ID
	CreatedAt      time.Time `json:"created_at"`
}

// FunctionUpdateMessage 表示异步函数更新的消息
type FunctionUpdateMessage struct {
	NodeID         string    `json:"node_id"`
	CloudAccountID string    `json:"cloud_account_id"`
	Namespace      string    `json:"namespace"`
	Region         string    `json:"region"`
	FunctionName   string    `json:"function_name"`
	ZipFilePath    string    `json:"zip_file_path"`
	CreatedAt      time.Time `json:"created_at"`
}

// NodeDeploymentMessage 表示异步节点部署的消息
type NodeDeploymentMessage struct {
	NodeID         string    `json:"node_id"`
	CloudAccountID string    `json:"cloud_account_id"`
	Namespace      string    `json:"namespace"`
	Region         string    `json:"region"`
	PackageID      int64     `json:"package_id,omitempty"` // 代码包ID，优先使用
	ZipFileBase64  string    `json:"zip_file_base64"`
	FileName       string    `json:"file_name"`
	TaskID         string    `json:"task_id,omitempty"` // 关联的异步任务ID
	ItemID         string    `json:"item_id,omitempty"` // 任务中的项目ID
	EnqueueTime    time.Time `json:"enqueue_time"`
}

// NodeCreationQueue 管理异步节点创建的队列
type NodeCreationQueue struct {
	mu       sync.RWMutex
	messages []NodeCreationMessage
	ch       chan NodeCreationMessage
	closed   bool
}

// NewNodeCreationQueue 创建新的节点创建队列
func NewNodeCreationQueue(bufferSize int) *NodeCreationQueue {
	return &NodeCreationQueue{
		messages: make([]NodeCreationMessage, 0),
		ch:       make(chan NodeCreationMessage, bufferSize),
	}
}

// Enqueue 将消息添加到队列
func (q *NodeCreationQueue) Enqueue(msg NodeCreationMessage) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return ErrQueueClosed
	}

	select {
	case q.ch <- msg:
		return nil
	default:
		// 通道已满，存储在内存中（生产环境中可能需要持久化到数据库）
		q.messages = append(q.messages, msg)
		return nil
	}
}

// Dequeue 从队列中获取消息
func (q *NodeCreationQueue) Dequeue(ctx context.Context) (NodeCreationMessage, error) {
	// 首先尝试从通道获取
	select {
	case msg := <-q.ch:
		return msg, nil
	case <-ctx.Done():
		return NodeCreationMessage{}, ctx.Err()
	default:
		// 通道为空，检查内存队列
		q.mu.Lock()
		defer q.mu.Unlock()

		if len(q.messages) > 0 {
			msg := q.messages[0]
			q.messages = q.messages[1:]
			return msg, nil
		}

		// 没有可用的消息
		return NodeCreationMessage{}, ErrQueueEmpty
	}
}

// Channel 返回用于消费消息的通道
func (q *NodeCreationQueue) Channel() <-chan NodeCreationMessage {
	return q.ch
}

// Close 关闭队列
func (q *NodeCreationQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()

	if !q.closed {
		q.closed = true
		close(q.ch)
	}
}

// Size 返回队列的当前大小
func (q *NodeCreationQueue) Size() int {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return len(q.messages) + len(q.ch)
}

// NodeDeletionQueue 管理异步节点删除的队列
type NodeDeletionQueue struct {
	mu       sync.RWMutex
	messages []NodeDeletionMessage
	ch       chan NodeDeletionMessage
	closed   bool
}

// NewNodeDeletionQueue 创建新的节点删除队列
func NewNodeDeletionQueue(bufferSize int) *NodeDeletionQueue {
	return &NodeDeletionQueue{
		messages: make([]NodeDeletionMessage, 0),
		ch:       make(chan NodeDeletionMessage, bufferSize),
	}
}

// Enqueue 将消息添加到删除队列
func (q *NodeDeletionQueue) Enqueue(msg NodeDeletionMessage) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return ErrQueueClosed
	}

	select {
	case q.ch <- msg:
		log.Infof("[NodeDeletionQueue] 消息已发送到channel: NodeID=%s", msg.NodeID)
		return nil
	default:
		q.messages = append(q.messages, msg)
		log.Infof("[NodeDeletionQueue] Channel满，消息已加入内存队列: NodeID=%s, 队列长度=%d", msg.NodeID, len(q.messages))
		return nil
	}
}

// Channel 返回用于消费消息的通道
func (q *NodeDeletionQueue) Channel() <-chan NodeDeletionMessage {
	return q.ch
}

// Close 关闭删除队列
func (q *NodeDeletionQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()

	if !q.closed {
		q.closed = true
		close(q.ch)
	}
}

// FunctionUpdateQueue 管理异步函数更新的队列
type FunctionUpdateQueue struct {
	mu       sync.RWMutex
	messages []FunctionUpdateMessage
	ch       chan FunctionUpdateMessage
	closed   bool
}

// NewFunctionUpdateQueue 创建新的函数更新队列
func NewFunctionUpdateQueue(bufferSize int) *FunctionUpdateQueue {
	return &FunctionUpdateQueue{
		messages: make([]FunctionUpdateMessage, 0),
		ch:       make(chan FunctionUpdateMessage, bufferSize),
	}
}

// Enqueue 将消息添加到更新队列
func (q *FunctionUpdateQueue) Enqueue(msg FunctionUpdateMessage) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return ErrQueueClosed
	}

	select {
	case q.ch <- msg:
		return nil
	default:
		q.messages = append(q.messages, msg)
		return nil
	}
}

// Channel 返回用于消费消息的通道
func (q *FunctionUpdateQueue) Channel() <-chan FunctionUpdateMessage {
	return q.ch
}

// Close 关闭更新队列
func (q *FunctionUpdateQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()

	if !q.closed {
		q.closed = true
		close(q.ch)
	}
}

// NodeDeploymentQueue 管理异步节点部署的队列
type NodeDeploymentQueue struct {
	mu       sync.RWMutex
	messages []NodeDeploymentMessage
	ch       chan NodeDeploymentMessage
	closed   bool
}

// NewNodeDeploymentQueue 创建新的节点部署队列
func NewNodeDeploymentQueue(bufferSize int) *NodeDeploymentQueue {
	return &NodeDeploymentQueue{
		messages: make([]NodeDeploymentMessage, 0),
		ch:       make(chan NodeDeploymentMessage, bufferSize),
	}
}

// Enqueue 将消息添加到部署队列
func (q *NodeDeploymentQueue) Enqueue(msg NodeDeploymentMessage) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return ErrQueueClosed
	}

	select {
	case q.ch <- msg:
		return nil
	default:
		// 通道已满，存储在内存中
		q.messages = append(q.messages, msg)
		return nil
	}
}

// Dequeue 从队列中获取消息
func (q *NodeDeploymentQueue) Dequeue(ctx context.Context) (NodeDeploymentMessage, error) {
	// 首先尝试从通道获取
	select {
	case msg := <-q.ch:
		return msg, nil
	case <-ctx.Done():
		return NodeDeploymentMessage{}, ctx.Err()
	default:
		// 通道为空，检查内存队列
		q.mu.Lock()
		defer q.mu.Unlock()

		if len(q.messages) > 0 {
			msg := q.messages[0]
			q.messages = q.messages[1:]
			return msg, nil
		}

		// 队列为空
		return NodeDeploymentMessage{}, ErrQueueEmpty
	}
}

// Channel 返回用于消费消息的通道
func (q *NodeDeploymentQueue) Channel() <-chan NodeDeploymentMessage {
	return q.ch
}

// Size 返回队列中的消息数量
func (q *NodeDeploymentQueue) Size() int {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return len(q.messages) + len(q.ch)
}

// Close 关闭部署队列
func (q *NodeDeploymentQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()

	if !q.closed {
		q.closed = true
		close(q.ch)
		log.Info("NodeDeploymentQueue closed")
	}
}
