package metrics

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	monconfig "github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/storageauth"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	commonpb "github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/trpcretry"
	"trpc.group/trpc-go/trpc-go/client"
)

type AccessClient interface {
	UpsertFields(context.Context, *storagepb.PrimaryUpsertFieldsReq, ...client.Option) (*storagepb.PrimaryUpsertFieldsRsp, error)
	ReadTimeSeriesRows(context.Context, *storagepb.ReadTimeSeriesRowsReq, ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error)
}
type MetadataClient interface {
	GetSpace(context.Context, *storagepb.GetSpaceReq, ...client.Option) (*storagepb.GetSpaceRsp, error)
	GetDataset(context.Context, *storagepb.GetDatasetReq, ...client.Option) (*storagepb.GetDatasetRsp, error)
	GetDataNode(context.Context, *storagepb.GetDataNodeReq, ...client.Option) (*storagepb.GetDataNodeRsp, error)
	ListDatasetColumns(context.Context, *storagepb.ListDatasetColumnsReq, ...client.Option) (*storagepb.ListDatasetColumnsRsp, error)
}

type StorageAdapter struct {
	access   AccessClient
	metadata MetadataClient
	auth     *commonpb.AuthInfo
	cfg      monconfig.MetricsStorageConfig
	mu       sync.RWMutex
	schema   SchemaStatus
}

func NewStorageAdapter(access AccessClient, metadata MetadataClient, cfg monconfig.MetricsStorageConfig) *StorageAdapter {
	return &StorageAdapter{access: access, metadata: metadata, auth: storageauth.Primary(cfg.KeyID), cfg: cfg, schema: SchemaStatus{Error: "metrics schema has not been checked"}}
}
func NewStorageAdapterFromConfig(cfg monconfig.MetricsStorageConfig) *StorageAdapter {
	target := gatewayauth.ServiceGatewayTarget(cfg.GatewayTarget)
	credentials, err := gatewayauth.ResolveCredentials(cfg.KeyID, cfg.HMACKeyFile)
	if err != nil {
		return NewStorageAdapter(nil, nil, cfg)
	}
	nodeID := cfg.GatewayNodeID
	if strings.TrimSpace(nodeID) == "" {
		nodeID = gatewayauth.ServiceGatewayNodeID()
	}
	options := gatewayauth.NewTRPCClientOptions(normalizeTarget(target, "11003"), nodeID, credentials)
	return NewStorageAdapter(storagepb.NewPrimaryStoreClientProxy(options...), storagepb.NewMetadataClientProxy(options...), cfg)
}

func normalizeTarget(raw, port string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "ip://127.0.0.1:" + port
	}
	if strings.HasPrefix(raw, "ip://") {
		return raw
	}
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" {
		if u.Scheme == "http" || u.Scheme == "https" {
			return raw
		}
		return raw
	}
	if !strings.Contains(raw, ":") {
		return raw
	}
	return "ip://" + raw
}

type SchemaStatus struct {
	Valid     bool
	CheckedAt time.Time
	Error     string
}
type columnContract struct {
	typ      storagepb.FieldValueType
	origin   storagepb.DatasetColumnOriginType
	originID string
	required bool
}

func (a *StorageAdapter) ValidateSchema(ctx context.Context) error {
	err := a.validateSchema(ctx)
	if a != nil {
		a.mu.Lock()
		a.schema = SchemaStatus{Valid: err == nil, CheckedAt: time.Now().UTC()}
		if err != nil {
			a.schema.Error = err.Error()
		}
		a.mu.Unlock()
	}
	return err
}

// SchemaStatus returns the last read-only metadata validation result. It is
// intentionally separate from process health so metric ingestion can be
// degraded without making existing monitor health checks fail.
func (a *StorageAdapter) SchemaStatus() SchemaStatus {
	if a == nil {
		return SchemaStatus{Error: "metrics storage adapter is nil"}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.schema
}

func (a *StorageAdapter) validateSchema(ctx context.Context) error {
	if a == nil || a.metadata == nil {
		return errors.New("metrics storage metadata client is not initialized")
	}
	if strings.TrimSpace(a.cfg.SpaceID) == "" || strings.TrimSpace(a.cfg.DatasetID) == "" || strings.TrimSpace(a.cfg.Frequency) == "" {
		return errors.New("metrics storage space, dataset, and frequency are required")
	}
	if err := ValidateMetricSpace(a.cfg.SpaceID); err != nil {
		return err
	}
	spaceRsp, err := a.metadata.GetSpace(ctx, &storagepb.GetSpaceReq{SpaceId: a.cfg.SpaceID})
	if err != nil {
		return fmt.Errorf("get metrics space: %w", err)
	}
	if err := storageOK("get metrics space", spaceRsp.GetRetInfo()); err != nil {
		return err
	}
	if spaceRsp.GetSpace() == nil || !isActive(spaceRsp.GetSpace().GetStatus()) {
		return fmt.Errorf("metrics space %q is missing or inactive", a.cfg.SpaceID)
	}
	datasetRsp, err := a.metadata.GetDataset(ctx, &storagepb.GetDatasetReq{SpaceId: a.cfg.SpaceID, DatasetId: a.cfg.DatasetID})
	if err != nil {
		return fmt.Errorf("get metrics dataset: %w", err)
	}
	if err := storageOK("get metrics dataset", datasetRsp.GetRetInfo()); err != nil {
		return err
	}
	dataset := datasetRsp.GetDataset()
	if dataset == nil || !isActive(dataset.GetStatus()) {
		return fmt.Errorf("metrics dataset %q is missing or inactive", a.cfg.DatasetID)
	}
	if !dataset.GetBindingLocked() || strings.TrimSpace(dataset.GetDataNodeId()) == "" {
		return fmt.Errorf("metrics dataset %q must be active, binding_locked, and bound to a data node", a.cfg.DatasetID)
	}
	nodeRsp, err := a.metadata.GetDataNode(ctx, &storagepb.GetDataNodeReq{NodeId: dataset.GetDataNodeId()})
	if err != nil {
		return fmt.Errorf("get metrics data node %q: %w", dataset.GetDataNodeId(), err)
	}
	if err := storageOK("get metrics data node", nodeRsp.GetRetInfo()); err != nil {
		return err
	}
	if nodeRsp.GetNode() == nil || !isActive(nodeRsp.GetNode().GetStatus()) {
		return fmt.Errorf("metrics data node %q is missing or inactive", dataset.GetDataNodeId())
	}
	if dataset.GetDataKind() != storagepb.DataKind_DATA_KIND_TIME_SERIES {
		return fmt.Errorf("metrics dataset kind is %s, want TIME_SERIES", dataset.GetDataKind())
	}
	if !contains(dataset.GetFreqs(), a.cfg.Frequency) {
		return fmt.Errorf("metrics dataset does not support frequency %q", a.cfg.Frequency)
	}
	columnsRsp, err := a.metadata.ListDatasetColumns(ctx, &storagepb.ListDatasetColumnsReq{SpaceId: a.cfg.SpaceID, DatasetId: a.cfg.DatasetID, Page: &commonpb.Page{Page: 1, Size: 500}})
	if err != nil {
		return fmt.Errorf("list metrics dataset columns: %w", err)
	}
	if err := storageOK("list metrics dataset columns", columnsRsp.GetRetInfo()); err != nil {
		return err
	}
	required := map[string]columnContract{"value": {storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, "monitor_metric_value", true}, "labels_json": {storagepb.FieldValueType_FIELD_VALUE_TYPE_JSON, storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, "monitor_metric_labels", true}, "producer_node_id": {storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING, storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, "monitor_metric_producer_node_id", false}, "producer_version": {storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING, storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, "monitor_metric_producer_version", false}, "message_id": {storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING, storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, "monitor_metric_message_id", true}}
	for _, column := range columnsRsp.GetColumns() {
		if want, ok := required[column.GetColumnName()]; ok {
			if column.GetValueType() != want.typ || column.GetOriginType() != want.origin || column.GetOriginId() != want.originID || column.GetRequired() != want.required || !isActive(column.GetStatus()) {
				return fmt.Errorf("metrics column %q has mismatched type/status", column.GetColumnName())
			}
			delete(required, column.GetColumnName())
		}
	}
	if len(required) > 0 {
		return fmt.Errorf("metrics dataset missing columns: %v", sortedKeys(required))
	}
	return nil
}
func isActive(status string) bool { return strings.EqualFold(strings.TrimSpace(status), "active") }
func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
func sortedKeys(m map[string]columnContract) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func storageOK(action string, ret *commonpb.RetInfo) error {
	if ret == nil {
		return fmt.Errorf("%s: empty ret_info", action)
	}
	if ret.GetCode() != commonpb.ErrorCode_SUCCESS {
		return fmt.Errorf("%s: %s", action, ret.GetMsg())
	}
	return nil
}

type HistoryPoint struct {
	SeriesID   string
	Value      float64
	ObservedAt time.Time
	LabelsJSON string
	MessageID  string
}

type HistorySelector struct {
	SeriesID string
}

func HistorySelectorForSeries(series MetricSeries) HistorySelector {
	return HistorySelector{SeriesID: series.SeriesID}
}

func (a *StorageAdapter) WriteSamples(ctx context.Context, samples []Sample) error {
	if a == nil || a.access == nil {
		return errors.New("metrics storage-primary client is not initialized")
	}
	batch := a.cfg.WriteBatchSize
	if batch <= 0 {
		batch = 1000
	}
	for start := 0; start < len(samples); start += batch {
		end := start + batch
		if end > len(samples) {
			end = len(samples)
		}
		rows := make([]*storagepb.RowFieldUpsert, 0, end-start)
		for _, sample := range samples[start:end] {
			if sample.ObservedAt.IsZero() {
				return errors.New("metric sample observed_at is required")
			}
			rows = append(rows, sampleRow(a.cfg, sample))
		}
		rsp, err := a.access.UpsertFields(ctx, &storagepb.PrimaryUpsertFieldsReq{AuthInfo: a.auth, Rows: rows})
		if err != nil {
			return fmt.Errorf("write metrics history: %w", err)
		}
		if err := storageOK("write metrics history", rsp.GetRetInfo()); err != nil {
			return err
		}
	}
	return nil
}
func sampleRow(cfg monconfig.MetricsStorageConfig, s Sample) *storagepb.RowFieldUpsert {
	return &storagepb.RowFieldUpsert{
		Key: &storagepb.RowKey{
			SpaceId: cfg.SpaceID, DatasetId: cfg.DatasetID,
			Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: s.SeriesID, Freq: cfg.Frequency, DataTime: s.ObservedAt.UTC().Format(time.RFC3339Nano)}},
		},
		Attributes: map[string]*storagepb.TypedValue{"service_name": metricStringValue(s.ServiceName), "instance_id": metricStringValue(s.InstanceID), "metric_name": metricStringValue(s.MetricName), "metric_type": metricStringValue(s.MetricType)},
		Fields: []*storagepb.FieldValue{
			{FieldId: "value", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: s.Value}}},
			{FieldId: "labels_json", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_JsonValue{JsonValue: s.LabelsJSON}}},
			{FieldId: "producer_node_id", Value: metricStringValue(s.ProducerNodeID)},
			{FieldId: "producer_version", Value: metricStringValue(s.ProducerVersion)},
			{FieldId: "message_id", Value: metricStringValue(s.MessageID)},
		},
	}
}

func (a *StorageAdapter) QueryHistorySelectors(ctx context.Context, selectors []HistorySelector, start, end time.Time, desc bool, limit int) ([]HistoryPoint, error) {
	if a == nil || a.access == nil {
		return nil, errors.New("metrics storage-primary client is not initialized")
	}
	if limit <= 0 {
		limit = 500
	}
	if limit > 500 {
		limit = 500
	}
	keys := make([]*storagepb.TimeSeriesKey, 0, len(selectors))
	for _, selector := range selectors {
		if selector.SeriesID != "" {
			key := &storagepb.TimeSeriesKey{SpaceId: a.cfg.SpaceID, DatasetId: a.cfg.DatasetID, SubjectId: selector.SeriesID, Freq: a.cfg.Frequency}
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	order := storagepb.SortOrder_SORT_ORDER_ASC
	if desc {
		order = storagepb.SortOrder_SORT_ORDER_DESC
	}
	tr := &storagepb.TimeRange{}
	if !start.IsZero() {
		tr.StartTime = start.UTC().Format(time.RFC3339Nano)
	}
	if !end.IsZero() {
		tr.EndTime = end.UTC().Format(time.RFC3339Nano)
	}
	columnPrefix := a.cfg.DatasetID + "."
	rsp, err := a.access.ReadTimeSeriesRows(ctx, &storagepb.ReadTimeSeriesRowsReq{AuthInfo: a.auth, Keys: keys, TimeRange: tr, Order: order, ColumnNames: []string{columnPrefix + "value", columnPrefix + "labels_json", columnPrefix + "message_id"}, Page: &commonpb.Page{Page: 1, Size: uint32(limit)}}, client.WithFilter(trpcretry.ReadOnly()))
	if err != nil {
		return nil, fmt.Errorf("read metrics history: %w", err)
	}
	if err := storageOK("read metrics history", rsp.GetRetInfo()); err != nil {
		return nil, err
	}
	out := make([]HistoryPoint, 0, len(rsp.GetRows()))
	for _, row := range rsp.GetRows() {
		if row == nil || row.GetKey() == nil {
			continue
		}
		p := HistoryPoint{SeriesID: row.GetKey().GetSubjectId()}
		if t, err := time.Parse(time.RFC3339Nano, row.GetKey().GetDataTime()); err == nil {
			p.ObservedAt = t
		}
		for _, field := range row.GetFields() {
			switch strings.TrimPrefix(field.GetFieldId(), columnPrefix) {
			case "value":
				p.Value = field.GetValue().GetDoubleValue()
			case "labels_json":
				p.LabelsJSON = field.GetValue().GetJsonValue()
			case "message_id":
				p.MessageID = field.GetValue().GetStringValue()
			}
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if desc {
			return out[i].ObservedAt.After(out[j].ObservedAt)
		}
		return out[i].ObservedAt.Before(out[j].ObservedAt)
	})
	return out, nil
}

func metricStringValue(value string) *storagepb.TypedValue {
	return &storagepb.TypedValue{Value: &storagepb.TypedValue_StringValue{StringValue: value}}
}
