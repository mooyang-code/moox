package taskrunner

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/cloudjobpb"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"trpc.group/trpc-go/trpc-go/log"
)

type jobLogFields struct {
	Event                string
	SpaceID              string
	JobID                string
	JobItemID            string
	TaskID               string
	JobType              string
	RuntimeCodePackageID string
	NodeID               string
	Consumer             string
	MessageID            string
	DeliveryCount        uint64
	ExecuteAt            time.Time
	DatasetID            string
	SubjectID            string
	Symbol               string
	Interval             string
	Decision             string
	Delay                time.Duration
	Status               string
	Duration             time.Duration
	ErrorCode            string
	Err                  error
}

func (fields jobLogFields) String() string {
	return fmt.Sprintf(
		"event=%s space_id=%s job_id=%s job_item_id=%s task_id=%s job_type=%s "+
			"runtime_code_package_id=%s node_id=%s consumer=%s message_id=%s "+
			"delivery_count=%d execute_at=%s dataset_id=%s subject_id=%s symbol=%s interval=%s "+
			"decision=%s delay_ms=%d status=%s duration_ms=%d error_code=%s error=%s",
		logValue(fields.Event),
		logValue(fields.SpaceID),
		logValue(fields.JobID),
		logValue(fields.JobItemID),
		logValue(fields.TaskID),
		logValue(fields.JobType),
		logValue(fields.RuntimeCodePackageID),
		logValue(fields.NodeID),
		logValue(fields.Consumer),
		logValue(fields.MessageID),
		fields.DeliveryCount,
		logValue(formatLogTime(fields.ExecuteAt)),
		logValue(fields.DatasetID),
		logValue(fields.SubjectID),
		logValue(fields.Symbol),
		logValue(fields.Interval),
		logValue(fields.Decision),
		fields.Delay.Milliseconds(),
		logValue(fields.Status),
		fields.Duration.Milliseconds(),
		logValue(fields.ErrorCode),
		logValue(safeLogError(fields.Err)),
	)
}

func logValue(value string) string {
	return strconv.Quote(strings.TrimSpace(value))
}

func formatLogTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func safeLogError(err error) string {
	if err == nil {
		return ""
	}
	value := strings.Join(strings.Fields(err.Error()), " ")
	const maxErrorBytes = 256
	if len(value) > maxErrorBytes {
		value = value[:maxErrorBytes]
	}
	return value
}

func writeJobLog(ctx context.Context, fields jobLogFields, failed bool) {
	if failed {
		log.ErrorContextf(ctx, "%s", fields.String())
		return
	}
	log.InfoContextf(ctx, "%s", fields.String())
}

func baseDeliveryLogFields(delivery *jetstream.Delivery, nodeID string) jobLogFields {
	fields := jobLogFields{
		RuntimeCodePackageID: strings.TrimSpace(os.Getenv("MOOX_CODE_PACKAGE_ID")),
		NodeID:               strings.TrimSpace(nodeID),
	}
	if delivery != nil {
		fields.Consumer = delivery.Consumer
		fields.MessageID = delivery.RawMessageID
		fields.DeliveryCount = delivery.DeliveryCount
	}
	return fields
}

func validatedDeliveryLogFields(
	registry *events.Registry,
	bindings []queueBinding,
	spaceID string,
	nodeID string,
	delivery *jetstream.Delivery,
) jobLogFields {
	fields := baseDeliveryLogFields(delivery, nodeID)
	if delivery == nil {
		return fields
	}
	var binding *queueBinding
	for i := range bindings {
		if bindings[i].name == delivery.Consumer && bindings[i].subject == delivery.Subject {
			binding = &bindings[i]
			break
		}
	}
	if binding == nil {
		return fields
	}
	decoded := events.DecodeDelivery(registry, delivery)
	payload, ok := decoded.Payload.(*cloudjobpb.JobExecutionRequested)
	if decoded.Err != nil || !ok || decoded.Message.GetSpaceId() != spaceID ||
		decoded.Message.GetSubjectId() != binding.subjectID ||
		payload.GetJobType() != binding.jobType {
		return fields
	}
	fields.SpaceID = decoded.Message.GetSpaceId()
	fields.JobID = payload.GetJobId()
	fields.JobItemID = payload.GetJobItemId()
	fields.JobType = payload.GetJobType()
	if executeAt, err := requestedExecutionTime(payload); err == nil {
		fields.ExecuteAt = executeAt
	}
	params := payload.GetParams().AsMap()
	fields.TaskID = stringValue(params, "task_id")
	fields.DatasetID = stringValue(params, "dataset_id")
	fields.SubjectID = stringValue(params, "subject_id")
	fields.Symbol = stringValue(params, "symbol")
	fields.Interval = stringValue(params, "interval")
	return fields
}

type jobActionReporter struct {
	registry *events.Registry
	bindings []queueBinding
	spaceID  string
	nodeID   string
}

type jobLogEntry struct {
	fields jobLogFields
	failed bool
}

func (r *jobActionReporter) ReportAction(
	ctx context.Context,
	delivery *jetstream.Delivery,
	result jetstream.HandlerResult,
	actionErr error,
) {
	fields := validatedDeliveryLogFields(r.registry, r.bindings, r.spaceID, r.nodeID, delivery)
	for _, entry := range actionLogEntries(fields, result, actionErr) {
		writeJobLog(ctx, entry.fields, entry.failed)
	}
}

func actionLogEntries(
	fields jobLogFields,
	result jetstream.HandlerResult,
	actionErr error,
) []jobLogEntry {
	fields.Event = "collector_job_delivery_action"
	fields.Decision = decisionName(result.Decision)
	fields.Delay = result.Delay
	fields.Err = actionErr
	if actionErr == nil {
		return []jobLogEntry{{fields: fields}}
	}
	fields.ErrorCode = "DELIVERY_ACTION_FAILED"
	actionEntry := jobLogEntry{fields: fields, failed: true}
	fields.Event = "collector_job_transport_error"
	return []jobLogEntry{actionEntry, {fields: fields, failed: true}}
}

func decisionName(decision jetstream.HandlerDecision) string {
	switch decision {
	case jetstream.ACK:
		return "ACK"
	case jetstream.RETRY:
		return "RETRY"
	case jetstream.TERM:
		return "TERM"
	default:
		return "UNKNOWN"
	}
}

func reportTransportError(ctx context.Context, nodeID string, err error) {
	fields := baseDeliveryLogFields(nil, nodeID)
	fields.Event = "collector_job_transport_error"
	fields.ErrorCode = "JETSTREAM_TRANSPORT"
	fields.Err = err
	writeJobLog(ctx, fields, true)
}
