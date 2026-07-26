package projection

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestHeartbeatBufferCollapsesLatestByNode(t *testing.T) {
	writer := &fakeHeartbeatWriter{}
	buffer := NewHeartbeatBuffer(writer, HeartbeatBufferOptions{
		MaxKeys:       8,
		FlushInterval: time.Hour,
	})
	defer buffer.Close(context.Background())

	firstMeta, _ := structpb.NewStruct(map[string]any{"seq": "old"})
	latestMeta, _ := structpb.NewStruct(map[string]any{"seq": "latest"})
	if err := buffer.Enqueue(&pb.ReportHeartbeatReq{
		SpaceId:            "crypto",
		NodeId:             "node-1",
		NodeType:           "scf-event",
		RunningVersion:     "v1",
		SupportedWorkloads: []string{"collect.kline"},
		Metadata:           firstMeta,
	}); err != nil {
		t.Fatalf("Enqueue(first) error = %v", err)
	}
	if err := buffer.Enqueue(&pb.ReportHeartbeatReq{
		SpaceId:            "crypto",
		NodeId:             "node-1",
		NodeType:           "scf-event",
		RunningVersion:     "v2",
		SupportedWorkloads: []string{"collect.symbol"},
		Metadata:           latestMeta,
	}); err != nil {
		t.Fatalf("Enqueue(latest) error = %v", err)
	}
	if err := buffer.Flush(context.Background()); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	if len(writer.calls) != 1 {
		t.Fatalf("writer calls = %d, want 1", len(writer.calls))
	}
	call := writer.calls[0]
	if call.version != "v2" || call.supported != `["collect.symbol"]` {
		t.Fatalf("call = %+v", call)
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(call.metadata), &meta); err != nil {
		t.Fatalf("metadata json: %v", err)
	}
	if meta["seq"] != "latest" {
		t.Fatalf("metadata = %+v", meta)
	}
}

func TestHeartbeatBufferRetriesAfterWriteFailure(t *testing.T) {
	writer := &fakeHeartbeatWriter{failures: 1}
	buffer := NewHeartbeatBuffer(writer, HeartbeatBufferOptions{
		MaxKeys:       8,
		FlushInterval: time.Hour,
	})
	defer buffer.Close(context.Background())

	if err := buffer.Enqueue(&pb.ReportHeartbeatReq{
		SpaceId:        "crypto",
		NodeId:         "node-1",
		RunningVersion: "v1",
	}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if err := buffer.Flush(context.Background()); !errors.Is(err, errFakeHeartbeatWrite) {
		t.Fatalf("first Flush() error = %v, want fake write failure", err)
	}
	if err := buffer.Flush(context.Background()); err != nil {
		t.Fatalf("second Flush() error = %v", err)
	}
	if len(writer.calls) != 1 || writer.calls[0].nodeID != "node-1" {
		t.Fatalf("writer calls = %+v", writer.calls)
	}
}

type fakeHeartbeatWriter struct {
	failures int
	calls    []heartbeatWriteCall
}

type heartbeatWriteCall struct {
	spaceID   string
	nodeID    string
	nodeType  string
	version   string
	supported string
	metadata  string
}

func (w *fakeHeartbeatWriter) UpdateHeartbeat(_ context.Context, spaceID string, nodeID string, nodeType string, version string, supported string, metadata string) error {
	if w.failures > 0 {
		w.failures--
		return errFakeHeartbeatWrite
	}
	w.calls = append(w.calls, heartbeatWriteCall{
		spaceID:   spaceID,
		nodeID:    nodeID,
		nodeType:  nodeType,
		version:   version,
		supported: supported,
		metadata:  metadata,
	})
	return nil
}

var errFakeHeartbeatWrite = errors.New("fake heartbeat write failed")
