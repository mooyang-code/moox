package jobqueue

import (
	"testing"
	"time"
)

func TestNewJetStreamQueueUsesCodeOwnedDefaults(t *testing.T) {
	queue := NewJetStreamQueue(nil, QueueConfig{})
	if queue.cfg.AckWait != time.Minute || queue.cfg.MaxDeliver != 3 || queue.cfg.MaxAckPending != 1 {
		t.Fatalf("config = %+v", queue.cfg)
	}
	if err := queue.Close(); err != nil {
		t.Fatal(err)
	}
}
