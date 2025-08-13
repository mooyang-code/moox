package queue

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	ErrQueueClosed = errors.New("queue is closed")
	ErrQueueEmpty  = errors.New("queue is empty")
)

// TaskMessage 通用任务消息
type TaskMessage struct {
	TaskID    string                 `json:"task_id"`
	TaskType  string                 `json:"task_type"`
	Data      map[string]interface{} `json:"data"`
	CreatedAt time.Time              `json:"created_at"`
}

// TaskQueue 通用任务队列接口
type TaskQueue interface {
	Enqueue(msg TaskMessage) error
	Dequeue(ctx context.Context) (TaskMessage, error)
	Close()
}

// MemoryTaskQueue 基于内存的任务队列实现
type MemoryTaskQueue struct {
	mu       sync.RWMutex
	messages []TaskMessage
	ch       chan TaskMessage
	closed   bool
}

// NewMemoryTaskQueue 创建内存任务队列
func NewMemoryTaskQueue(bufferSize int) *MemoryTaskQueue {
	return &MemoryTaskQueue{
		messages: make([]TaskMessage, 0),
		ch:       make(chan TaskMessage, bufferSize),
	}
}

// Enqueue 将消息添加到队列
func (q *MemoryTaskQueue) Enqueue(msg TaskMessage) error {
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
func (q *MemoryTaskQueue) Dequeue(ctx context.Context) (TaskMessage, error) {
	// 首先尝试从通道获取
	select {
	case msg := <-q.ch:
		return msg, nil
	case <-ctx.Done():
		return TaskMessage{}, ctx.Err()
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
		return TaskMessage{}, ErrQueueEmpty
	}
}

// Channel 返回用于消费消息的通道
func (q *MemoryTaskQueue) Channel() <-chan TaskMessage {
	return q.ch
}

// Close 关闭队列
func (q *MemoryTaskQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()

	if !q.closed {
		q.closed = true
		close(q.ch)
	}
}