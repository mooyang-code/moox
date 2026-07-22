package metrics

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/jetstream"
	messagepb "github.com/mooyang-code/moox/packages/messagepb"
	"gorm.io/gorm"
)

func TestHandleDeliveryAppliesTermAndReturnsBusinessError(t *testing.T) {
	err := (&Consumer{}).HandleDelivery(context.Background(), nil)
	if err == nil || !errors.Is(err, jetstream.ErrInvalidDelivery) || err.Error() == "empty metric delivery" {
		t.Fatalf("HandleDelivery() error = %v, want business and transport errors", err)
	}
}

func TestCommitIngestDeduplicatesAndKeepsNewestLatest(t *testing.T) {
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	r := metricMessageStoreForTest(t, mgr)
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
	r := metricMessageStoreForTest(t, mgr)
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
	var queryErr error
	_, err = store.WithDatabase(mgr, func(db *gorm.DB) struct{} {
		queryErr = db.Where("c_series_id = ?", "series").First(&series).Error
		return struct{}{}
	})
	if err != nil {
		t.Fatal(err)
	}
	if queryErr != nil {
		t.Fatal(queryErr)
	}
	if !series.LastSeenAt.Equal(newest) {
		t.Fatalf("series last_seen_at=%s, want %s", series.LastSeenAt, newest)
	}
}

func TestCommitIngestDoesNotMoveBusinessWatermarkBackwardsAfterRestart(t *testing.T) {
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	requireNoError(t, err)
	defer mgr.Close()
	requireNoError(t, mgr.ApplySchema(schema.SQL()))
	r := metricMessageStoreForTest(t, mgr)
	producer := &messagepb.Producer{ServiceName: "factor", InstanceId: "factor@node-a", BootId: "boot-a"}
	newMessage := func(id string) *messagepb.MooxMessage {
		return &messagepb.MooxMessage{MessageId: id, Producer: producer}
	}
	name := "moox_module_input_watermark_timestamp_seconds"
	if _, err := r.CommitIngest(context.Background(), newMessage("new"), []Sample{{SeriesID: "watermark", ServiceName: "factor", InstanceID: "factor@node-a", MetricName: name, MetricType: "gauge", Value: 200, ObservedAt: time.Unix(20, 0).UTC(), MessageID: "new"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CommitIngest(context.Background(), newMessage("restart"), []Sample{{SeriesID: "watermark", ServiceName: "factor", InstanceID: "factor@node-a", MetricName: name, MetricType: "gauge", Value: 100, ObservedAt: time.Unix(21, 0).UTC(), MessageID: "restart"}}); err != nil {
		t.Fatal(err)
	}
	latest, err := r.GetLatest(context.Background(), "watermark")
	requireNoError(t, err)
	if latest.Value != 200 {
		t.Fatalf("watermark regressed to %v", latest.Value)
	}
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunWhenReadyRequiresStorage(t *testing.T) {
	if err := RunWhenReady(context.Background(), ConsumerOptions{}); err == nil {
		t.Fatal("expected missing storage error")
	}
}
