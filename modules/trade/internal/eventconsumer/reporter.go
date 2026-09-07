package eventconsumer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"gorm.io/gorm"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

// Keep classification separate from error text: exchange and database errors
// may contain credentials or payloads and are never serialized into this log.
type targetBusinessError struct {
	code  string
	cause error
}

func (e *targetBusinessError) Error() string {
	if e.cause != nil {
		return e.cause.Error()
	}
	return e.code
}

func (e *targetBusinessError) Unwrap() error { return e.cause }

func targetRejection(code string, cause error) jetstream.HandlerResult {
	return jetstream.HandlerResult{Decision: jetstream.TERM, Err: &targetBusinessError{code: code, cause: cause}}
}

type targetActionReport struct {
	Decision        string `json:"decision"`
	ErrorCode       string `json:"error_code"`
	ActionResult    string `json:"action_result"`
	ActionErrorCode string `json:"action_error_code,omitempty"`
	Stream          string `json:"stream,omitempty"`
	Consumer        string `json:"consumer,omitempty"`
	StreamSequence  uint64 `json:"stream_sequence,omitempty"`
	DeliveryCount   uint64 `json:"delivery_count,omitempty"`
	EventID         string `json:"event_id,omitempty"`
	SpaceID         string `json:"space_id,omitempty"`
	TargetID        string `json:"target_id,omitempty"`
	LogicalAccount  string `json:"logical_account_id,omitempty"`
	InstanceID      string `json:"instance_id,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	TraceID         string `json:"trace_id,omitempty"`
	TraceSource     string `json:"trace_source,omitempty"`
}

type targetActionReporter struct {
	emit func(context.Context, targetActionReport)
}

func (r targetActionReporter) ReportAction(ctx context.Context, delivery *jetstream.Delivery, result jetstream.HandlerResult, actionErr error) {
	report := makeTargetActionReport(ctx, delivery, result, actionErr)
	telemetry.TargetDeliveryActions.WithLabelValues(report.Decision, report.ErrorCode, report.ActionResult).Inc()
	if r.emit != nil {
		r.emit(ctx, report)
		return
	}
	raw, _ := json.Marshal(report)
	if result.Decision == jetstream.ACK && actionErr == nil {
		log.InfoContextf(ctx, "trade target delivery %s", raw)
	} else {
		log.WarnContextf(ctx, "trade target delivery %s", raw)
	}
}

func makeTargetActionReport(ctx context.Context, delivery *jetstream.Delivery, result jetstream.HandlerResult, actionErr error) targetActionReport {
	report := targetActionReport{Decision: targetDecisionName(result.Decision), ErrorCode: targetErrorCode(result), ActionResult: "success"}
	if actionErr != nil {
		report.ActionResult = "failed"
		report.ActionErrorCode = transportErrorCode(actionErr)
	}
	if ctx != nil {
		report.TraceID = reportIdentifier(string(trpc.Message(ctx).ServerMetaData()["trace_id"]))
		if report.TraceID != "" {
			report.TraceSource = "rpc_metadata"
		}
	}
	if delivery == nil {
		return report
	}
	report.Stream, report.Consumer = reportIdentifier(delivery.Stream), reportIdentifier(delivery.Consumer)
	report.StreamSequence, report.DeliveryCount = delivery.StreamSeq, delivery.DeliveryCount
	registry, err := events.DefaultRegistry()
	var invalidPayload *events.PayloadValidationError
	if err != nil || delivery.DecodeError != nil && !errors.As(delivery.DecodeError, &invalidPayload) {
		return report
	}
	message, payload, err := events.DecodeRaw(registry, delivery.RawData, delivery.Subject, delivery.RawMessageID, delivery.ContentType)
	if err != nil && !errors.As(err, &invalidPayload) {
		return report
	}
	report.EventID, report.SpaceID = reportIdentifier(message.GetEventId()), reportIdentifier(message.GetSpaceId())
	if report.TraceID == "" {
		// Governed events do not carry an upstream span context. Use a stable
		// event-local correlation trace, explicitly not a distributed RPC trace.
		digest := sha256.Sum256([]byte(message.GetEventId()))
		report.TraceID = fmt.Sprintf("%x", digest[:16])
		report.TraceSource = "event_id"
	}
	if err != nil {
		return report
	}
	request, ok := payload.(*tradeeventpb.LogicalAccountTargetWeightRequested)
	if ok {
		report.TargetID, report.LogicalAccount = reportIdentifier(request.GetTargetId()), reportIdentifier(request.GetLogicalAccountId())
		report.InstanceID, report.SessionID = reportIdentifier(request.GetInstanceId()), reportIdentifier(request.GetSessionId())
	}
	return report
}

func transportErrorCode(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	default:
		return "transport_failure"
	}
}

func targetDecisionName(decision jetstream.HandlerDecision) string {
	switch decision {
	case jetstream.ACK:
		return "ACK"
	case jetstream.RETRY:
		return "NAK"
	case jetstream.TERM:
		return "TERM"
	default:
		return "INVALID"
	}
}

func targetErrorCode(result jetstream.HandlerResult) string {
	var classified *targetBusinessError
	if errors.As(result.Err, &classified) {
		switch classified.code {
		case "invalid_event", "invalid_contract", "authorization_conflict", "receipt_conflict", "superseded", "resolver_missing":
			return classified.code
		}
	}
	switch {
	case errors.Is(result.Err, store.ErrTargetExpired):
		return "target_expired"
	case errors.Is(result.Err, jetstream.ErrInvalidDelivery):
		return "invalid_event"
	case errors.Is(result.Err, store.ErrConflict):
		return "conflict"
	case errors.Is(result.Err, gorm.ErrRecordNotFound):
		return "not_found"
	case errors.Is(result.Err, store.ErrInvalidRecord):
		return "invalid_record"
	case errors.Is(result.Err, targetapp.ErrPermanent):
		return "permanent_rejection"
	case errors.Is(result.Err, context.Canceled):
		return "canceled"
	case errors.Is(result.Err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case result.Err != nil && result.Decision == jetstream.RETRY:
		return "retryable_failure"
	case result.Err != nil:
		return "permanent_rejection"
	case result.Decision == jetstream.ACK:
		return "accepted"
	case result.Decision == jetstream.RETRY:
		return "batch_deferred"
	default:
		return "unspecified"
	}
}

func reportIdentifier(value string) string {
	var out strings.Builder
	changed := false
	for _, char := range value {
		if out.Len()+len(string(char)) > 96 {
			changed = true
			break
		}
		if unicode.IsControl(char) || char == '"' || char == '\\' {
			out.WriteByte('_')
			changed = true
		} else {
			out.WriteRune(char)
		}
	}
	if changed {
		digest := sha256.Sum256([]byte(value))
		return out.String() + fmt.Sprintf("~%x", digest[:8])
	}
	return out.String()
}
