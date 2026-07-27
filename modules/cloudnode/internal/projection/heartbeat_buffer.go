package projection

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	defaultHeartbeatMaxKeys       = 2048
	defaultHeartbeatFlushInterval = time.Second
)

// HeartbeatWriter is the catalog write surface needed by heartbeat buffering.
type HeartbeatWriter interface {
	UpdateHeartbeat(ctx context.Context, spaceID string, nodeID string, version string, supported string, metadata string) error
}

// HeartbeatSink accepts heartbeats on the RPC path and persists them later.
type HeartbeatSink interface {
	Enqueue(req *pb.ReportHeartbeatReq) error
	Flush(ctx context.Context) error
	Close(ctx context.Context) error
}

// HeartbeatBufferOptions configures the latest-wins heartbeat buffer.
type HeartbeatBufferOptions struct {
	MaxKeys       int
	FlushInterval time.Duration
}

type HeartbeatBuffer struct {
	writer HeartbeatWriter
	opts   HeartbeatBufferOptions

	mu      sync.Mutex
	pending map[string]heartbeatEvent
	stop    chan struct{}
	done    chan struct{}
}

type heartbeatEvent struct {
	spaceID            string
	nodeID             string
	runningVersion     string
	supportedWorkloads []string
	metadata           map[string]any
}

// NewHeartbeatBuffer creates a buffer and starts its periodic flusher.
func NewHeartbeatBuffer(writer HeartbeatWriter, opts HeartbeatBufferOptions) *HeartbeatBuffer {
	if opts.MaxKeys <= 0 {
		opts.MaxKeys = defaultHeartbeatMaxKeys
	}
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = defaultHeartbeatFlushInterval
	}
	b := &HeartbeatBuffer{
		writer:  writer,
		opts:    opts,
		pending: make(map[string]heartbeatEvent, opts.MaxKeys),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	go b.loop()
	return b
}

func (b *HeartbeatBuffer) Enqueue(req *pb.ReportHeartbeatReq) error {
	if req == nil {
		return fmt.Errorf("heartbeat request is required")
	}
	spaceID := strings.TrimSpace(req.GetSpaceId())
	nodeID := strings.TrimSpace(req.GetNodeId())
	if spaceID == "" {
		return fmt.Errorf("space_id is required")
	}
	if nodeID == "" {
		return fmt.Errorf("node_id is required")
	}
	key := heartbeatKey(spaceID, nodeID)
	event := heartbeatEvent{
		spaceID:            spaceID,
		nodeID:             nodeID,
		runningVersion:     strings.TrimSpace(req.GetRunningVersion()),
		supportedWorkloads: append([]string(nil), req.GetSupportedWorkloads()...),
		metadata:           map[string]any{},
	}
	if req.GetMetadata() != nil {
		event.metadata = req.GetMetadata().AsMap()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.pending[key]; !exists && len(b.pending) >= b.opts.MaxKeys {
		return fmt.Errorf("heartbeat buffer is full")
	}
	b.pending[key] = event
	return nil
}

func (b *HeartbeatBuffer) Flush(ctx context.Context) error {
	events := b.drain()
	for i, event := range events {
		supported, _ := json.Marshal(event.supportedWorkloads)
		metadata, _ := json.Marshal(event.metadata)
		if b.writer == nil {
			continue
		}
		if err := b.writer.UpdateHeartbeat(ctx, event.spaceID, event.nodeID, event.runningVersion, string(supported), string(metadata)); err != nil {
			b.requeue(events[i:])
			return err
		}
	}
	return nil
}

func (b *HeartbeatBuffer) Close(ctx context.Context) error {
	select {
	case <-b.done:
	default:
		close(b.stop)
		select {
		case <-b.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return b.Flush(ctx)
}

func (b *HeartbeatBuffer) loop() {
	defer close(b.done)
	ticker := time.NewTicker(b.opts.FlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := b.Flush(trpc.BackgroundContext()); err != nil {
				log.Warnf("[CloudNode] heartbeat buffer flush failed: %v", err)
			}
		case <-b.stop:
			return
		}
	}
}

func (b *HeartbeatBuffer) requeue(events []heartbeatEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, event := range events {
		key := heartbeatKey(event.spaceID, event.nodeID)
		if _, exists := b.pending[key]; exists {
			continue
		}
		if len(b.pending) >= b.opts.MaxKeys {
			continue
		}
		b.pending[key] = event
	}
}

func (b *HeartbeatBuffer) drain() []heartbeatEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pending) == 0 {
		return nil
	}
	out := make([]heartbeatEvent, 0, len(b.pending))
	for key, event := range b.pending {
		out = append(out, event)
		delete(b.pending, key)
	}
	return out
}

func heartbeatKey(spaceID string, nodeID string) string {
	return spaceID + "\x00" + nodeID
}
