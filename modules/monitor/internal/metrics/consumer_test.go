package metrics

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	metricspb "github.com/mooyang-code/moox/packages/metricspb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
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
	r := metricMessageStoreForTest(t, mgr)
	at := time.Unix(10, 0).UTC()
	msg := &eventpb.EventMessage{EventId: "m1"}
	report := &metricspb.MetricReport{ServiceName: "svc", InstanceId: "i", BootId: "b"}
	s := Sample{SeriesID: "series", ServiceName: "svc", InstanceID: "i", MetricName: "g", MetricType: "gauge", Value: 1, ObservedAt: at, MessageID: "m1"}
	dup, err := r.CommitIngest(context.Background(), msg, report, []Sample{s})
	if err != nil || dup {
		t.Fatalf("first commit dup=%v err=%v", dup, err)
	}
	dup, err = r.CommitIngest(context.Background(), msg, report, []Sample{{SeriesID: "series", ServiceName: "svc", InstanceID: "i", MetricName: "g", MetricType: "gauge", Value: 9, ObservedAt: at.Add(time.Second), MessageID: "m1"}})
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
	newMessage := func(id string) (*eventpb.EventMessage, *metricspb.MetricReport) {
		return &eventpb.EventMessage{EventId: id, OccurredAt: timestamppb.Now()}, &metricspb.MetricReport{ServiceName: "svc", InstanceId: "i", BootId: "b"}
	}
	newSample := func(at time.Time, id string) Sample {
		return Sample{SeriesID: "series", ServiceName: "svc", InstanceID: "i", MetricName: "g", MetricType: "gauge", Value: 1, ObservedAt: at, MessageID: id}
	}
	newest := time.Unix(20, 0).UTC()
	msg, report := newMessage("new")
	if _, err := r.CommitIngest(context.Background(), msg, report, []Sample{newSample(newest, "new")}); err != nil {
		t.Fatal(err)
	}
	msg, report = newMessage("old")
	if _, err := r.CommitIngest(context.Background(), msg, report, []Sample{newSample(newest.Add(-time.Minute), "old")}); err != nil {
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

func TestCommitIngestDoesNotMoveDatasetWatermarkBackwardsAfterRestart(t *testing.T) {
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	requireNoError(t, err)
	defer mgr.Close()
	requireNoError(t, mgr.ApplySchema(schema.SQL()))
	r := metricMessageStoreForTest(t, mgr)
	newMessage := func(id string) (*eventpb.EventMessage, *metricspb.MetricReport) {
		return &eventpb.EventMessage{EventId: id, OccurredAt: timestamppb.Now()}, &metricspb.MetricReport{ServiceName: "factor", InstanceId: "factor@node-a", BootId: "boot-a"}
	}
	name := "moox_factor_dataset_output_watermark_timestamp_seconds"
	msg, report := newMessage("new")
	if _, err := r.CommitIngest(context.Background(), msg, report, []Sample{{SeriesID: "watermark", ServiceName: "factor", InstanceID: "factor@node-a", MetricName: name, MetricType: "gauge", Value: 200, ObservedAt: time.Unix(20, 0).UTC(), MessageID: "new"}}); err != nil {
		t.Fatal(err)
	}
	msg, report = newMessage("restart")
	if _, err := r.CommitIngest(context.Background(), msg, report, []Sample{{SeriesID: "watermark", ServiceName: "factor", InstanceID: "factor@node-a", MetricName: name, MetricType: "gauge", Value: 100, ObservedAt: time.Unix(21, 0).UTC(), MessageID: "restart"}}); err != nil {
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
