package storageio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/registry"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

type OutputManifestStore interface {
	Get(context.Context, store.OutputManifestKey) ([]string, error)
	Replace(context.Context, store.OutputManifestKey, []string) error
}

func (c *Client) WithOutputManifests(manifests OutputManifestStore) *Client {
	c.manifests = manifests
	return c
}

type factorRowKey struct {
	SpaceID   string `json:"space_id"`
	DatasetID string `json:"dataset_id"`
	SubjectID string `json:"subject_id"`
	Frequency string `json:"frequency"`
	DataTime  string `json:"data_time"`
	SeriesTag string `json:"series_tag"`
}

func (c *Client) WriteFactorPatch(ctx context.Context, task *engine.FactorTask, result *engine.FactorResult) (uint64, error) {
	if task == nil || result == nil {
		return 0, fmt.Errorf("task and result are required")
	}
	if c.manifests == nil {
		return c.writeLegacyFactorPatch(ctx, task, result)
	}
	key := store.OutputManifestKey{BindingID: task.BindingID, SubjectID: task.SubjectID, Frequency: task.Freq, PeriodTime: time.Unix(task.PeriodTime, 0).UTC()}
	previous, err := c.manifests.Get(ctx, key)
	if err != nil {
		return 0, fmt.Errorf("load factor output manifest: %w", err)
	}
	upserts, current, err := buildFactorRows(task, result)
	if err != nil {
		return 0, err
	}
	stale := difference(previous, current)
	// Record a recoverable union before touching the remote store. If the
	// process dies after remote writes but before the final Replace, the next
	// attempt still knows which older RowKeys must be cleared.
	pending := append([]string(nil), previous...)
	pending = append(pending, current...)
	sort.Strings(pending)
	pending = compactStrings(pending)
	if err := c.manifests.Replace(ctx, key, pending); err != nil {
		return 0, fmt.Errorf("persist factor output write intent: %w", err)
	}
	if len(stale) > 0 {
		clearRows, decodeErr := clearRowsForKeys(stale, task.Factor.FactorID, task.Factor.Outputs)
		if decodeErr != nil {
			return 0, decodeErr
		}
		if err := c.writeRows(ctx, deterministicWriteID(task, "clear", clearRows), clearRows); err != nil {
			return 0, err
		}
	}
	if len(upserts) > 0 {
		if err := c.writeRows(ctx, deterministicWriteID(task, "upsert", upserts), upserts); err != nil {
			return 0, err
		}
	}
	if err := c.manifests.Replace(ctx, key, current); err != nil {
		return 0, fmt.Errorf("replace factor output manifest: %w", err)
	}
	return uint64(len(upserts)), nil
}

func (c *Client) writeLegacyFactorPatch(ctx context.Context, task *engine.FactorTask, result *engine.FactorResult) (uint64, error) {
	if len(result.Rows) == 0 {
		return 0, nil
	}
	datasetID := task.ResultDatasetID
	if datasetID == "" {
		datasetID = task.TargetDataset
	}
	rows := make([]*storagepb.RowFieldUpsert, 0, len(result.Rows))
	for i, resultRow := range result.Rows {
		if resultRow.DataTime.IsZero() {
			return 0, fmt.Errorf("factor result row %d data_time is required", i)
		}
		row := &storagepb.RowFieldUpsert{Key: toProtoRowKey(factorRowKey{SpaceID: task.SpaceID, DatasetID: datasetID, SubjectID: task.SubjectID, Frequency: task.Freq, DataTime: resultRow.DataTime.UTC().Format(time.RFC3339Nano), SeriesTag: resultRow.SeriesTag}), Attributes: map[string]*storagepb.TypedValue{"factor.id": stringValue(task.Factor.FactorID), "factor.source_hash": stringValue(task.Factor.SourceHash), "factor.parent_task_id": stringValue(task.TaskID), "factor.computed_at": stringValue(time.Now().UTC().Format(time.RFC3339Nano))}}
		for _, name := range task.Factor.Outputs {
			value, exists := resultRow.Values[name]
			if !exists {
				return 0, fmt.Errorf("factor result row %d is missing output %s", i, name)
			}
			if value == nil {
				row.Fields = append(row.Fields, nullField(name))
				continue
			}
			number, ok := asFloat64(value)
			if !ok {
				return 0, fmt.Errorf("factor output %s returned non-numeric value %T", name, value)
			}
			row.Fields = append(row.Fields, doubleField(name, number))
		}
		rows = append(rows, row)
	}
	if err := c.writeRows(ctx, "", rows); err != nil {
		return 0, err
	}
	return uint64(len(rows)), nil
}

func (c *Client) ClearFactorOutputs(ctx context.Context, task *engine.FactorTask) error {
	returnError := func(err error) error { return err }
	_, err := c.WriteFactorPatch(ctx, task, &engine.FactorResult{})
	return returnError(err)
}

func buildFactorRows(task *engine.FactorTask, result *engine.FactorResult) ([]*storagepb.RowFieldUpsert, []string, error) {
	rows := make([]*storagepb.RowFieldUpsert, 0, len(result.Rows))
	keys := make([]string, 0, len(result.Rows))
	computedAt := time.Now().UTC()
	for i, resultRow := range result.Rows {
		if resultRow.DataTime.IsZero() {
			return nil, nil, fmt.Errorf("factor result row %d data_time is required", i)
		}
		identity := factorRowKey{SpaceID: task.SpaceID, DatasetID: task.ResultDatasetID, SubjectID: task.SubjectID, Frequency: task.Freq, DataTime: resultRow.DataTime.UTC().Format(time.RFC3339Nano), SeriesTag: resultRow.SeriesTag}
		encoded, _ := json.Marshal(identity)
		keys = append(keys, string(encoded))
		row := &storagepb.RowFieldUpsert{Key: toProtoRowKey(identity), Attributes: map[string]*storagepb.TypedValue{
			"factor.id": stringValue(task.Factor.FactorID), "factor.source_hash": stringValue(task.Factor.SourceHash),
			"factor.parent_task_id": stringValue(task.TaskID), "factor.computed_at": stringValue(computedAt.Format(time.RFC3339Nano)),
		}}
		for _, name := range task.Factor.Outputs {
			value, exists := resultRow.Values[name]
			if !exists {
				return nil, nil, fmt.Errorf("factor result row %d is missing output %s", i, name)
			}
			fieldID := registry.OutputField(task.Factor.FactorID, name)
			if value == nil {
				row.Fields = append(row.Fields, nullField(fieldID))
				continue
			}
			number, ok := asFloat64(value)
			if !ok {
				return nil, nil, fmt.Errorf("factor output %s returned non-numeric value %T", name, value)
			}
			row.Fields = append(row.Fields, doubleField(fieldID, number))
		}
		rows = append(rows, row)
	}
	sort.Strings(keys)
	keys = compactStrings(keys)
	return rows, keys, nil
}

func clearRowsForKeys(keys []string, factorID string, outputs []string) ([]*storagepb.RowFieldUpsert, error) {
	rows := make([]*storagepb.RowFieldUpsert, 0, len(keys))
	for _, encoded := range keys {
		var key factorRowKey
		if err := json.Unmarshal([]byte(encoded), &key); err != nil {
			return nil, fmt.Errorf("decode factor output manifest row key: %w", err)
		}
		row := &storagepb.RowFieldUpsert{Key: toProtoRowKey(key)}
		for _, name := range outputs {
			row.Fields = append(row.Fields, nullField(registry.OutputField(factorID, name)))
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (c *Client) writeRows(ctx context.Context, sourceEventID string, rows []*storagepb.RowFieldUpsert) error {
	rsp, err := c.access.UpsertFields(ctx, &storagepb.PrimaryUpsertFieldsReq{AuthInfo: c.auth, Rows: rows, SourceEventId: sourceEventID, WriteSource: "factor"})
	if err != nil {
		return fmt.Errorf("write factor patch: %w", err)
	}
	return ensureStorageOK("write factor patch", rsp.GetRetInfo())
}

func deterministicWriteID(task *engine.FactorTask, phase string, rows []*storagepb.RowFieldUpsert) string {
	planHash := sha256.Sum256(canonicalWritePlan(rows))
	sum := sha256.Sum256([]byte(task.TriggerEventID + "\x00" + task.BindingID + "\x00" + task.SubjectID + "\x00" + strconv.FormatInt(task.PeriodTime, 10) + "\x00" + phase + "\x00" + hex.EncodeToString(planHash[:])))
	return "factor-" + hex.EncodeToString(sum[:16])
}

func canonicalWritePlan(rows []*storagepb.RowFieldUpsert) []byte {
	stable := make([]*storagepb.RowFieldUpsert, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		copy := proto.Clone(row).(*storagepb.RowFieldUpsert)
		delete(copy.Attributes, "factor.computed_at")
		stable = append(stable, copy)
	}
	return canonicalRows(stable)
}

func canonicalRows(rows []*storagepb.RowFieldUpsert) []byte {
	encoded := make([][]byte, 0, len(rows))
	marshal := proto.MarshalOptions{Deterministic: true}
	for _, row := range rows {
		if row == nil {
			continue
		}
		if raw, err := marshal.Marshal(row); err == nil {
			encoded = append(encoded, raw)
		}
	}
	sort.Slice(encoded, func(i, j int) bool { return string(encoded[i]) < string(encoded[j]) })
	out := make([]byte, 0)
	for _, raw := range encoded {
		out = append(out, raw...)
		out = append(out, 0)
	}
	return out
}

func toProtoRowKey(key factorRowKey) *storagepb.RowKey {
	return &storagepb.RowKey{SpaceId: key.SpaceID, DatasetId: key.DatasetID, Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{SubjectId: key.SubjectID, Freq: key.Frequency, DataTime: key.DataTime, SeriesTag: key.SeriesTag}}}
}

func nullField(fieldID string) *storagepb.FieldValue {
	return &storagepb.FieldValue{FieldId: fieldID, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_NullValue{NullValue: storagepb.NullValue_NULL_VALUE_NULL}}}
}

func difference(previous, current []string) []string {
	seen := make(map[string]struct{}, len(current))
	for _, item := range current {
		seen[item] = struct{}{}
	}
	var result []string
	for _, item := range previous {
		if _, ok := seen[item]; !ok {
			result = append(result, item)
		}
	}
	return result
}

func compactStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value != out[len(out)-1] {
			out = append(out, value)
		}
	}
	return out
}

func stringValue(value string) *storagepb.TypedValue {
	return &storagepb.TypedValue{Value: &storagepb.TypedValue_StringValue{StringValue: value}}
}

func asFloat64(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint8:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	default:
		return 0, false
	}
}
