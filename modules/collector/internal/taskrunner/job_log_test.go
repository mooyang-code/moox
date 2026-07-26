package taskrunner

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/cloudjobpb"
	"github.com/mooyang-code/moox/packages/cloudjobqueue"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestJobLifecycleLogFieldsAreStable(t *testing.T) {
	fields := jobLogFields{
		Event: "collector_job_deferred", SpaceID: "crypto", JobID: "job-1",
		JobItemID: "item-1", TaskID: "task-1", JobType: "collect.kline",
		RuntimeCodePackageID: "pkg-1", NodeID: "node-1", Consumer: "consumer-1",
		MessageID: "message-1", DeliveryCount: 2,
		ExecuteAt: time.Date(2026, 7, 26, 10, 0, 1, 123, time.UTC),
		DatasetID: "kline", SubjectID: "BTC-USDT", Symbol: "BTCUSDT", Interval: "1m",
		Decision: "RETRY", Delay: 1500 * time.Millisecond, Status: "pending",
		Duration: 2500 * time.Millisecond, ErrorCode: "NOT_DUE", Err: errors.New("not due"),
	}
	got := fields.String()
	want := `event="collector_job_deferred" space_id="crypto" job_id="job-1" job_item_id="item-1" ` +
		`task_id="task-1" job_type="collect.kline" runtime_code_package_id="pkg-1" node_id="node-1" ` +
		`consumer="consumer-1" message_id="message-1" delivery_count=2 ` +
		`execute_at="2026-07-26T10:00:01.000000123Z" dataset_id="kline" subject_id="BTC-USDT" ` +
		`symbol="BTCUSDT" interval="1m" decision="RETRY" delay_ms=1500 status="pending" ` +
		`duration_ms=2500 error_code="NOT_DUE" error="not due"`
	if got != want {
		t.Fatalf("job log:\n got: %s\nwant: %s", got, want)
	}
}

func TestJobLifecycleLogOmitsSensitiveParams(t *testing.T) {
	fields := jobLogFields{
		Event: "collector_job_started",
		Err:   errors.New(strings.Repeat("x", 300)),
	}
	got := fields.String()
	for _, sensitive := range []string{"params=", "access_key", "secret_key", "request_body", "authorization"} {
		if strings.Contains(strings.ToLower(got), sensitive) {
			t.Fatalf("job log contains sensitive field %q: %s", sensitive, got)
		}
	}
	if len(safeLogError(fields.Err)) != 256 {
		t.Fatalf("safe error length = %d, want 256", len(safeLogError(fields.Err)))
	}
}

func TestDecisionName(t *testing.T) {
	if decisionName(0) != "UNKNOWN" {
		t.Fatalf("decisionName(0) = %q", decisionName(0))
	}
}

func TestValidatedReceivedLogContainsJobIdentityAndDeliveryCount(t *testing.T) {
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	const jobType = "collect.kline"
	identity := cloudjobqueue.Identity{SpaceID: "crypto", JobType: jobType}
	consumer, err := identity.ConsumerName()
	if err != nil {
		t.Fatal(err)
	}
	subjectID, err := identity.SubjectID()
	if err != nil {
		t.Fatal(err)
	}
	subject, err := registry.RenderSubject(events.CloudJobExecutionRequested, "crypto", subjectID)
	if err != nil {
		t.Fatal(err)
	}
	params, err := structpb.NewStruct(map[string]any{"task_id": "task-1", "dataset_id": "kline"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := registry.MarshalMessage(events.CloudJobExecutionRequested, &cloudjobpb.JobExecutionRequested{
		JobId: "job-1", JobItemId: "item-1", JobType: jobType, Params: params,
	}, events.PublishOptions{
		EventID: "item-1", SpaceID: "crypto", SubjectID: subjectID,
		OccurredAt: time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	delivery := &jetstream.Delivery{
		Consumer: consumer, Subject: subject, RawData: raw, RawMessageID: "item-1",
		ContentType: events.ContentType, DeliveryCount: 3,
	}
	fields := validatedDeliveryLogFields(
		registry,
		[]queueBinding{{name: consumer, subject: subject, subjectID: subjectID, jobType: jobType}},
		"crypto",
		"node-1",
		delivery,
	)
	fields.Event = "collector_job_received"
	got := fields.String()
	for _, want := range []string{
		`event="collector_job_received"`,
		`space_id="crypto"`,
		`job_id="job-1"`,
		`job_item_id="item-1"`,
		`job_type="collect.kline"`,
		`message_id="item-1"`,
		`delivery_count=3`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("valid received log missing %q: %s", want, got)
		}
	}
}

func TestBareReceivedLogKeepsTransportIdentityForInvalidDelivery(t *testing.T) {
	fields := baseDeliveryLogFields(&jetstream.Delivery{
		Consumer: "consumer-1", RawMessageID: "message-1", DeliveryCount: 4,
	}, "node-1")
	fields.Event = "collector_job_received"
	got := fields.String()
	for _, want := range []string{
		`consumer="consumer-1"`,
		`message_id="message-1"`,
		`delivery_count=4`,
		`job_item_id=""`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("bare received log missing %q: %s", want, got)
		}
	}
}

func TestJobActionReporterReportsAckNakAndTerm(t *testing.T) {
	for _, test := range []struct {
		decision jetstream.HandlerDecision
		want     string
	}{
		{decision: jetstream.ACK, want: "ACK"},
		{decision: jetstream.RETRY, want: "RETRY"},
		{decision: jetstream.TERM, want: "TERM"},
	} {
		entries := actionLogEntries(jobLogFields{}, jetstream.HandlerResult{
			Decision: test.decision,
			Delay:    2 * time.Second,
		}, nil)
		if len(entries) != 1 ||
			entries[0].fields.Event != "collector_job_delivery_action" ||
			entries[0].fields.Decision != test.want ||
			entries[0].failed {
			t.Fatalf("action entries for %v = %+v", test.decision, entries)
		}
	}
}

func TestJobActionReporterReportsTransportFailure(t *testing.T) {
	actionErr := errors.New("nak transport failed")
	entries := actionLogEntries(jobLogFields{}, jetstream.HandlerResult{
		Decision: jetstream.RETRY,
		Delay:    3 * time.Second,
	}, actionErr)
	if len(entries) != 2 {
		t.Fatalf("action entries = %+v, want action and transport events", entries)
	}
	if entries[0].fields.Event != "collector_job_delivery_action" ||
		entries[1].fields.Event != "collector_job_transport_error" {
		t.Fatalf("action event names = %q, %q", entries[0].fields.Event, entries[1].fields.Event)
	}
	for _, entry := range entries {
		if !entry.failed || entry.fields.ErrorCode != "DELIVERY_ACTION_FAILED" ||
			!errors.Is(entry.fields.Err, actionErr) {
			t.Fatalf("failed action entry = %+v", entry)
		}
	}
}
