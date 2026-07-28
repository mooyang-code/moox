package report

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/observabilitypb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type errorPublisher struct {
	err error
}

func (p errorPublisher) Publish(context.Context, events.Event, proto.Message, events.PublishOptions) (*jetstream.PublishAck, error) {
	return nil, p.err
}

func TestEventReporterReportsTypedHealthCheck(t *testing.T) {
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	publisher := &fakePublisher{}
	reporter := &EventReporter{Registry: registry, Publisher: publisher}
	report := &observabilitypb.HealthCheckReport{
		ObserverId: "scf-sentinel", CheckId: "collector-ready", Target: "collector",
		Kind: "trpc", Success: true, CheckedAt: timestamppb.New(time.Now().UTC()),
	}
	if err := reporter.ReportHealth(context.Background(), report, "crypto"); err != nil {
		t.Fatal(err)
	}
	if len(publisher.events) != 1 || publisher.events[0].Name() != events.ObservabilityHealthCheckReported.Name() {
		t.Fatalf("events = %+v", publisher.events)
	}
	if got := publisher.options[0].SubjectID; got != "scf-sentinel/collector-ready" {
		t.Fatalf("subject_id = %q", got)
	}
}

func TestEventReporterValidatesBeforePublishing(t *testing.T) {
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	publisher := &fakePublisher{}
	reporter := &EventReporter{Registry: registry, Publisher: publisher}
	err = reporter.ReportHealth(context.Background(), &observabilitypb.HealthCheckReport{}, "crypto")
	if err == nil {
		t.Fatal("invalid report was accepted")
	}
	if len(publisher.events) != 0 {
		t.Fatal("invalid report was published")
	}
}

func TestEventReporterDoesNotLeakPublisherCredentials(t *testing.T) {
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	reporter := &EventReporter{Registry: registry, Publisher: errorPublisher{err: errors.New("authorization failed: password=super-secret")}}
	report := &observabilitypb.HealthCheckReport{
		ObserverId: "monitor", CheckId: "gateway-ready", Kind: "trpc",
		Success: false, CheckedAt: timestamppb.Now(),
	}
	err = reporter.ReportHealth(context.Background(), report, "moox_system")
	if err == nil {
		t.Fatal("publisher error was lost")
	}
	if strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("publisher credential leaked: %v", err)
	}
}
