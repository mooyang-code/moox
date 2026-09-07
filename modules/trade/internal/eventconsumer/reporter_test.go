package eventconsumer

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/codec"
)

func TestTargetActionReportSeparatesBusinessAndTransportOutcomes(t *testing.T) {
	cases := []struct {
		name     string
		result   jetstream.HandlerResult
		code     string
		decision string
	}{
		{"accepted", jetstream.HandlerResult{Decision: jetstream.ACK}, "accepted", "ACK"},
		{"retry", jetstream.HandlerResult{Decision: jetstream.RETRY, Err: errors.New("password=secret")}, "retryable_failure", "NAK"},
		{"expired", jetstream.HandlerResult{Decision: jetstream.TERM, Err: store.ErrTargetExpired}, "target_expired", "TERM"},
		{"superseded", targetRejection("superseded", nil), "superseded", "TERM"},
		{"identity", targetRejection("authorization_conflict", store.ErrConflict), "authorization_conflict", "TERM"},
		{"receipt", targetRejection("receipt_conflict", store.ErrConflict), "receipt_conflict", "TERM"},
		{"malformed", targetRejection("invalid_event", errors.New("payload=secret")), "invalid_event", "TERM"},
		{"permanent", jetstream.HandlerResult{Decision: jetstream.TERM, Err: errors.New("secret")}, "permanent_rejection", "TERM"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, actionErr := range []error{nil, errors.New("signed_url=secret")} {
				report := makeTargetActionReport(context.Background(), nil, tc.result, actionErr)
				require.Equal(t, tc.code, report.ErrorCode)
				require.Equal(t, tc.decision, report.Decision)
				if actionErr == nil {
					require.Equal(t, "success", report.ActionResult)
					require.Empty(t, report.ActionErrorCode)
				} else {
					require.Equal(t, "failed", report.ActionResult)
					require.Equal(t, "transport_failure", report.ActionErrorCode)
				}
				raw, err := json.Marshal(report)
				require.NoError(t, err)
				require.NotContains(t, string(raw), "secret")
			}
		})
	}
}

func TestTargetActionReportUsesOnlyValidatedBoundedIdentity(t *testing.T) {
	ctx, msg := codec.WithNewMessage(context.Background())
	msg.WithServerMetaData(codec.MetaData{"trace_id": []byte("trace-1")})
	delivery := logicalTargetDelivery(t, time.Now().UTC(), "target-report", "instance-report", "logical-report", 1, nil)
	delivery.Stream = strings.Repeat("x", 300)
	delivery.Consumer = "consumer\n\"unsafe"
	delivery.StreamSeq, delivery.DeliveryCount = 32, 2
	report := makeTargetActionReport(ctx, delivery, targetRejection("authorization_conflict", nil), nil)
	require.Equal(t, "trace-1", report.TraceID)
	require.Equal(t, "target-report", report.TargetID)
	require.Equal(t, "instance-report", report.InstanceID)
	require.Equal(t, "session-1", report.SessionID)
	require.Equal(t, "logical-report", report.LogicalAccount)
	require.NotEmpty(t, report.EventID)
	require.LessOrEqual(t, len(report.Stream), 128)
	require.True(t, strings.HasPrefix(report.Consumer, "consumer__unsafe~"))
	require.Equal(t, uint64(32), report.StreamSequence)
	require.Equal(t, uint64(2), report.DeliveryCount)
	delivery.DecodeError = errors.New("credential=secret")
	report = makeTargetActionReport(context.Background(), delivery, targetRejection("invalid_event", delivery.DecodeError), nil)
	require.Empty(t, report.TargetID)
	require.Empty(t, report.EventID)
	require.Empty(t, report.InstanceID)
	delivery.DecodeError = nil
	delivery.RawData = []byte("credential=secret")
	report = makeTargetActionReport(context.Background(), delivery, targetRejection("invalid_event", nil), nil)
	require.Empty(t, report.EventID)
	raw, err := json.Marshal(report)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "secret")
}

func TestTargetReportEventLocalTraceAndUnicodeIdentity(t *testing.T) {
	delivery := logicalTargetDelivery(t, time.Now().UTC(), "target-trace", "instance-trace", "logical-trace", 1, nil)
	report := makeTargetActionReport(context.Background(), delivery, jetstream.HandlerResult{Decision: jetstream.ACK}, nil)
	require.Len(t, report.TraceID, 32)
	require.Equal(t, "event_id", report.TraceSource)
	replayed := makeTargetActionReport(context.Background(), delivery, jetstream.HandlerResult{Decision: jetstream.ACK}, nil)
	require.Equal(t, report.TraceID, replayed.TraceID)
	require.Equal(t, "账户甲", reportIdentifier("账户甲"))
	require.NotEqual(t, reportIdentifier("账户甲"), reportIdentifier("账户乙"))
	require.NotEqual(t, reportIdentifier(strings.Repeat("x", 200)+"a"), reportIdentifier(strings.Repeat("x", 200)+"b"))
	for _, err := range []error{errors.New(strings.Repeat("secret", 1000)), context.Canceled, context.DeadlineExceeded} {
		require.Contains(t, []string{"transport_failure", "canceled", "deadline_exceeded"}, transportErrorCode(err))
	}
}

func TestHandleTargetClassifiesInvalidBusinessContract(t *testing.T) {
	now := time.Now().UTC()
	delivery := logicalTargetDelivery(t, now, "target-contract", "instance-contract", "logical-1", 1, nil)
	message := new(eventpb.EventMessage)
	require.NoError(t, proto.Unmarshal(delivery.RawData, message))
	request := new(tradeeventpb.LogicalAccountTargetWeightRequested)
	require.NoError(t, proto.Unmarshal(message.Payload, request))
	request.SessionId = ""
	var err error
	message.Payload, err = proto.Marshal(request)
	require.NoError(t, err)
	delivery.RawData, err = proto.Marshal(message)
	require.NoError(t, err)
	result := HandleTarget(context.Background(), delivery, targetOptions(openTargetStore(t), now))
	require.Equal(t, jetstream.TERM, result.Decision)
	require.Equal(t, "invalid_contract", targetErrorCode(result))
	var validation *events.PayloadValidationError
	require.ErrorAs(t, result.Err, &validation)
	for _, decodeErr := range []error{nil, validation} {
		delivery.DecodeError = decodeErr
		report := makeTargetActionReport(context.Background(), delivery, result, nil)
		require.Equal(t, message.GetEventId(), report.EventID)
		require.Equal(t, message.GetSpaceId(), report.SpaceID)
		require.Len(t, report.TraceID, 32)
		require.Equal(t, "event_id", report.TraceSource)
		require.Empty(t, report.TargetID)
		require.Empty(t, report.LogicalAccount)
		require.Empty(t, report.InstanceID)
		require.Empty(t, report.SessionID)
	}
}

func TestRunTargetReportsBusinessDecisionAfterTransportAction(t *testing.T) {
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true,
	})
	require.NoError(t, err)
	go srv.Start()
	t.Cleanup(func() { srv.Shutdown(); srv.WaitForShutdown() })
	require.True(t, srv.ReadyForConnections(5*time.Second))
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	_, err = js.AddStream(&nats.StreamConfig{Name: events.LogicalAccountTargetWeightRequested.Stream(), Subjects: []string{"moox.>"}})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	client, err := jetstream.Connect(ctx, jetstream.Config{URLs: []string{srv.ClientURL()}, Name: "target-reporter-test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	tradeStore := openTargetStore(t)
	seedLogicalTargetAccount(t, tradeStore, true, true)
	now := time.Now().UTC()
	options := targetOptions(tradeStore, now)
	options.Client, options.ConsumerName = client, "report-target-business-decision"
	done := make(chan error, 1)
	go func() { done <- RunTarget(ctx, options) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("target runner did not stop")
		}
	})
	// A valid envelope with a different owner is a permanent business rejection,
	// not a transport failure. It must be visible even though TERM succeeds.
	delivery := logicalTargetDelivery(t, now, "report-rejected-target", "other-owner", "logical-1", 1, nil)
	before := targetReportMetric(t, "TERM", "authorization_conflict", "success")
	_, err = client.PublishRaw(ctx, delivery.Subject, delivery.RawMessageID, delivery.RawData, delivery.ContentType)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		info, lookupErr := js.ConsumerInfo(events.LogicalAccountTargetWeightRequested.Stream(), options.ConsumerName)
		return lookupErr == nil && info.Delivered.Consumer > 0 && info.NumAckPending == 0
	}, 5*time.Second, 10*time.Millisecond, "the real runner must process and terminate the delivery")
	require.Eventually(t, func() bool {
		return targetReportMetric(t, "TERM", "authorization_conflict", "success") == before+1
	}, time.Second, 10*time.Millisecond, "the successful TERM currently hides its business rejection")
}

func targetReportMetric(t *testing.T, decision, code, action string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != "moox_trade_target_delivery_actions_total" {
			continue
		}
		for _, metric := range family.Metric {
			require.Len(t, metric.Label, 3, "event and account identities must not become metric labels")
			labels := map[string]string{}
			for _, label := range metric.Label {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["decision"] == decision && labels["error_code"] == code && labels["action_result"] == action {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}
