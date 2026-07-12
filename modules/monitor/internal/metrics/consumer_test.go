package metrics

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	messagepb "github.com/mooyang-code/moox/packages/messagepb"
)

func TestCommitIngestDeduplicatesAndKeepsNewestLatest(t *testing.T) {
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	r := NewRepository(mgr.DB())
	at := time.Unix(10, 0).UTC()
	msg := &messagepb.MooxMessage{MessageId: "m1", Producer: &messagepb.Producer{ServiceName: "svc", InstanceId: "i", BootId: "b"}, OccurredAt: nil}
	s := Sample{SeriesID: "series", ServiceName: "svc", InstanceID: "i", MetricName: "g", MetricType: "gauge", Value: 1, ObservedAt: at, MessageID: "m1"}
	dup, err := r.CommitIngest(context.Background(), msg, []Sample{s})
	if err != nil || dup {
		t.Fatalf("first commit dup=%v err=%v", dup, err)
	}
	dup, err = r.CommitIngest(context.Background(), msg, []Sample{{SeriesID: "series", ServiceName: "svc", InstanceID: "i", MetricName: "g", MetricType: "gauge", Value: 9, ObservedAt: at.Add(time.Second), MessageID: "m1"}})
	if err != nil || !dup {
		t.Fatalf("duplicate dup=%v err=%v", dup, err)
	}
	latest, err := r.GetLatest(context.Background(), "series")
	if err != nil {
		t.Fatal(err)
	}
	if latest.Value != 1 {
		t.Fatalf("duplicate changed latest=%v", latest.Value)
	}
}

func TestCommitIngestDoesNotMoveSeriesLastSeenBackwards(t *testing.T) {
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	r := NewRepository(mgr.DB())
	producer := &messagepb.Producer{ServiceName: "svc", InstanceId: "i", BootId: "b"}
	newMessage := func(id string) *messagepb.MooxMessage {
		return &messagepb.MooxMessage{MessageId: id, Producer: producer}
	}
	newSample := func(at time.Time, id string) Sample {
		return Sample{SeriesID: "series", ServiceName: "svc", InstanceID: "i", MetricName: "g", MetricType: "gauge", Value: 1, ObservedAt: at, MessageID: id}
	}
	newest := time.Unix(20, 0).UTC()
	if _, err := r.CommitIngest(context.Background(), newMessage("new"), []Sample{newSample(newest, "new")}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CommitIngest(context.Background(), newMessage("old"), []Sample{newSample(newest.Add(-time.Minute), "old")}); err != nil {
		t.Fatal(err)
	}
	var series MetricSeries
	if err := mgr.DB().Where("c_series_id = ?", "series").First(&series).Error; err != nil {
		t.Fatal(err)
	}
	if !series.LastSeenAt.Equal(newest) {
		t.Fatalf("series last_seen_at=%s, want %s", series.LastSeenAt, newest)
	}
}

func TestRunWhenReadyRequiresStorage(t *testing.T) {
	if err := RunWhenReady(context.Background(), ConsumerOptions{}); err == nil {
		t.Fatal("expected missing storage error")
	}
}
