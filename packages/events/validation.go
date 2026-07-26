package events

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/cloudjobpb"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"google.golang.org/protobuf/proto"
)

type EventValidator func(*eventpb.EventMessage, proto.Message) error

func validateCloudJobExecutionRequested(message *eventpb.EventMessage, value proto.Message) error {
	payload, ok := value.(*cloudjobpb.JobExecutionRequested)
	if !ok {
		return fmt.Errorf("cloud job payload has type %T", value)
	}
	if strings.TrimSpace(payload.GetJobItemId()) == "" ||
		strings.TrimSpace(payload.GetJobType()) == "" {
		return fmt.Errorf("cloud job identity is incomplete")
	}
	if payload.GetJobItemId() != message.GetEventId() {
		return fmt.Errorf("cloud job item_id does not match event_id")
	}
	if message.GetSubjectId() != payload.GetJobType() {
		return fmt.Errorf("cloud job route does not match subject_id")
	}
	return nil
}

func validateMetricsHostReported(message *eventpb.EventMessage, value proto.Message) error {
	payload, ok := value.(*hostmetricpb.HostMetric)
	if !ok {
		return fmt.Errorf("host metric payload has type %T", value)
	}
	if strings.TrimSpace(payload.GetAgentId()) == "" ||
		strings.TrimSpace(payload.GetHostname()) == "" ||
		payload.GetSnapshot() == nil {
		return fmt.Errorf("host metric identity or snapshot is incomplete")
	}
	if payload.GetAgentId() != message.GetSubjectId() {
		return fmt.Errorf("host metric agent_id does not match subject_id")
	}
	return nil
}

func validateMetricsSnapshotReported(message *eventpb.EventMessage, value proto.Message) error {
	payload, ok := value.(*metricspb.MetricReport)
	if !ok {
		return fmt.Errorf("metric report payload has type %T", value)
	}
	if strings.TrimSpace(payload.GetServiceName()) == "" ||
		strings.TrimSpace(payload.GetInstanceId()) == "" ||
		payload.GetSnapshot() == nil {
		return fmt.Errorf("metric report producer identity or snapshot is incomplete")
	}
	if message.GetSubjectId() != payload.GetServiceName()+"/"+payload.GetInstanceId() {
		return fmt.Errorf("metric report producer does not match subject_id")
	}
	return nil
}

func validateDatasetRowsUpserted(message *eventpb.EventMessage, value proto.Message) error {
	payload, ok := value.(*storagepb.DatasetRowsUpserted)
	if !ok {
		return fmt.Errorf("storage event payload has type %T", value)
	}
	if payload.GetSpaceId() == "" ||
		payload.GetSpaceId() != message.GetSpaceId() ||
		payload.GetDatasetId() == "" ||
		payload.GetDatasetId() != message.GetSubjectId() {
		return fmt.Errorf("storage event payload identity mismatch")
	}
	if len(payload.GetRows()) == 0 {
		return fmt.Errorf("storage event rows payload is empty")
	}
	for i, row := range payload.GetRows() {
		if row == nil || row.GetKey() == nil {
			return fmt.Errorf("storage event row %d key is required", i)
		}
		if row.GetKey().GetSpaceId() != payload.GetSpaceId() ||
			row.GetKey().GetDatasetId() != payload.GetDatasetId() {
			return fmt.Errorf("storage event row %d identity mismatch", i)
		}
		if err := validateStorageRow(row); err != nil {
			return fmt.Errorf("storage event row %d: %w", i, err)
		}
	}
	return nil
}

func validateTradeRebalanceRequested(message *eventpb.EventMessage, value proto.Message) error {
	payload, ok := value.(*tradeeventpb.RebalanceRequested)
	if !ok {
		return fmt.Errorf("trade rebalance payload has type %T", value)
	}
	if strings.TrimSpace(payload.GetRequestId()) == "" ||
		strings.TrimSpace(payload.GetStrategyRunId()) == "" ||
		strings.TrimSpace(payload.GetExecutionBindingId()) == "" ||
		payload.GetCommandSequence() == 0 ||
		payload.GetCommandSequence() > math.MaxInt64 {
		return fmt.Errorf("trade rebalance identity or command_sequence is incomplete")
	}
	if payload.GetRequestId() != message.GetEventId() {
		return fmt.Errorf("trade rebalance request_id does not match event_id")
	}
	if payload.GetExecutionBindingId() != message.GetSubjectId() {
		return fmt.Errorf("trade rebalance execution_binding_id does not match subject_id")
	}
	return nil
}

func validateStorageRow(row *storagepb.RowUpsert) error {
	key := row.GetKey()
	switch kind := key.GetKind().(type) {
	case *storagepb.RowKey_TimeSeries:
		series := kind.TimeSeries
		if series == nil || strings.TrimSpace(series.GetSubjectId()) == "" || strings.TrimSpace(series.GetFreq()) == "" || strings.TrimSpace(series.GetDataTime()) == "" {
			return fmt.Errorf("time-series key requires subject_id, freq, and data_time")
		}
		if err := validateStorageTime(series.GetDataTime()); err != nil {
			return fmt.Errorf("time-series data_time: %w", err)
		}
	case *storagepb.RowKey_Record:
		record := kind.Record
		if record == nil || strings.TrimSpace(record.GetRecordId()) == "" || strings.TrimSpace(record.GetVersion()) == "" {
			return fmt.Errorf("record key requires record_id and version")
		}
	default:
		return fmt.Errorf("row key kind is required")
	}
	for i, field := range row.GetFields() {
		if field == nil || strings.TrimSpace(field.GetFieldId()) == "" {
			return fmt.Errorf("field %d requires field_id", i)
		}
		if err := validateStorageValue(field.GetValue()); err != nil {
			return fmt.Errorf("field %d: %w", i, err)
		}
	}
	for name, value := range row.GetAttributes() {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("attribute name is required")
		}
		if err := validateStorageValue(value); err != nil {
			return fmt.Errorf("attribute %q: %w", name, err)
		}
	}
	return nil
}

func validateStorageValue(value *storagepb.TypedValue) error {
	if value == nil || value.GetValue() == nil {
		return fmt.Errorf("typed value is required")
	}
	switch typed := value.GetValue().(type) {
	case *storagepb.TypedValue_DoubleValue:
		if math.IsNaN(typed.DoubleValue) || math.IsInf(typed.DoubleValue, 0) {
			return fmt.Errorf("double value must be finite")
		}
	case *storagepb.TypedValue_TimeValue:
		if err := validateStorageTime(typed.TimeValue); err != nil {
			return fmt.Errorf("time value: %w", err)
		}
	case *storagepb.TypedValue_JsonValue:
		if !json.Valid([]byte(typed.JsonValue)) {
			return fmt.Errorf("json value is invalid")
		}
	case *storagepb.TypedValue_ListValue:
		if typed.ListValue == nil {
			return fmt.Errorf("list value is nil")
		}
		for i, item := range typed.ListValue.GetValues() {
			if err := validateStorageValue(item); err != nil {
				return fmt.Errorf("list item %d: %w", i, err)
			}
		}
	case *storagepb.TypedValue_NullValue:
		if typed.NullValue != storagepb.NullValue_NULL_VALUE_NULL {
			return fmt.Errorf("null value must be explicitly NULL")
		}
	}
	return nil
}

func validateStorageTime(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("time is required")
	}
	if _, err := time.Parse(time.RFC3339Nano, raw); err != nil {
		return err
	}
	return nil
}
