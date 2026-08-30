package storageio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
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

// FactorPatch is one factor task result destined for the same result Dataset.
// A batch writer can combine several factor definitions for one subject and
// period into a single Primary write and therefore one Storage View event.
type FactorPatch struct {
	Task   *engine.FactorTask
	Result *engine.FactorResult
}

// FactorBatchWriter is implemented by StorageIO clients that can submit
// several factor patches as bounded clear/upsert batches. The task
// runner keeps the single-patch fallback for small or test-only clients.
type FactorBatchWriter interface {
	WriteFactorPatches(context.Context, []FactorPatch) ([]uint64, error)
}

type preparedFactorPatch struct {
	key     store.OutputManifestKey
	task    *engine.FactorTask
	current []string
	stale   []*storagepb.RowFieldUpsert
	upserts []*storagepb.RowFieldUpsert
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

// WriteFactorPatches combines the output rows of multiple factor definitions
// before sending them to Storage. Each task keeps its own output manifest and
// deterministic identity, while the remote write is reduced to at most one
// clear event and one upsert event for the whole subject/period batch.
func (c *Client) WriteFactorPatches(ctx context.Context, patches []FactorPatch) ([]uint64, error) {
	if len(patches) == 0 {
		return nil, fmt.Errorf("factor patches are required")
	}
	if c == nil || c.access == nil {
		return nil, fmt.Errorf("factor storage client is unavailable")
	}
	// Deployments with the legacy manifest-less writer do not have enough
	// state to safely combine clear plans. Preserve their existing semantics.
	if c.manifests == nil {
		counts := make([]uint64, len(patches))
		for i, patch := range patches {
			if patch.Task == nil || patch.Result == nil {
				return nil, fmt.Errorf("factor patch %d task and result are required", i)
			}
			count, err := c.WriteFactorPatch(ctx, patch.Task, patch.Result)
			if err != nil {
				return nil, err
			}
			counts[i] = count
		}
		return counts, nil
	}

	prepared := make([]preparedFactorPatch, 0, len(patches))
	counts := make([]uint64, len(patches))
	batchKey := ""
	for i, patch := range patches {
		if patch.Task == nil || patch.Result == nil {
			return nil, fmt.Errorf("factor patch %d task and result are required", i)
		}
		task := patch.Task
		currentBatchKey := task.SpaceID + "\x00" + task.ResultDatasetID + "\x00" + task.SubjectID + "\x00" + task.Freq + "\x00" + strconv.FormatInt(task.PeriodTime, 10)
		if batchKey == "" {
			batchKey = currentBatchKey
		} else if currentBatchKey != batchKey {
			return nil, fmt.Errorf("factor patches must share space, result dataset, subject, frequency and period")
		}
		key := store.OutputManifestKey{BindingID: task.BindingID, SubjectID: task.SubjectID, Frequency: task.Freq, PeriodTime: time.Unix(task.PeriodTime, 0).UTC()}
		previous, err := c.manifests.Get(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("load factor output manifest: %w", err)
		}
		upserts, current, err := buildFactorRows(task, patch.Result)
		if err != nil {
			return nil, err
		}
		stale := difference(previous, current)
		clearRows, err := clearRowsForKeys(stale, task.Factor.FactorID, task.Factor.Outputs)
		if err != nil {
			return nil, err
		}
		pending := append(append([]string(nil), previous...), current...)
		sort.Strings(pending)
		pending = compactStrings(pending)
		// Persist every intent before the shared remote write. If the process
		// exits midway, the pending union is replayed by the next attempt.
		if err := c.manifests.Replace(ctx, key, pending); err != nil {
			return nil, fmt.Errorf("persist factor output write intent: %w", err)
		}
		counts[i] = uint64(len(upserts))
		prepared = append(prepared, preparedFactorPatch{key: key, task: task, current: current, stale: clearRows, upserts: upserts})
	}

	clearRows := make([]*storagepb.RowFieldUpsert, 0)
	upsertRows := make([]*storagepb.RowFieldUpsert, 0)
	for _, patch := range prepared {
		clearRows = append(clearRows, patch.stale...)
		upsertRows = append(upsertRows, patch.upserts...)
	}
	clearRows = mergeFactorRows(clearRows)
	upsertRows = mergeFactorRows(upsertRows)
	if len(clearRows) > 0 {
		if err := c.writeRows(ctx, deterministicBatchWriteID(prepared, "clear", clearRows), clearRows); err != nil {
			return nil, err
		}
	}
	if len(upsertRows) > 0 {
		if err := c.writeRows(ctx, deterministicBatchWriteID(prepared, "upsert", upsertRows), upsertRows); err != nil {
			return nil, err
		}
	}
	for _, patch := range prepared {
		if err := c.manifests.Replace(ctx, patch.key, patch.current); err != nil {
			return nil, fmt.Errorf("replace factor output manifest: %w", err)
		}
	}
	return counts, nil
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
		row := &storagepb.RowFieldUpsert{Key: toProtoRowKey(factorRowKey{SpaceID: task.SpaceID, DatasetID: datasetID, SubjectID: task.SubjectID, Frequency: task.Freq, DataTime: resultRow.DataTime.UTC().Format(time.RFC3339Nano), SeriesTag: resultRow.SeriesTag}), Attributes: factorRowAttributes(task)}
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
		row := &storagepb.RowFieldUpsert{Key: toProtoRowKey(identity), Attributes: factorRowAttributesAt(task, computedAt)}
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

func factorRowAttributes(task *engine.FactorTask) map[string]*storagepb.TypedValue {
	return factorRowAttributesAt(task, time.Now().UTC())
}

func factorRowAttributesAt(task *engine.FactorTask, computedAt time.Time) map[string]*storagepb.TypedValue {
	return map[string]*storagepb.TypedValue{
		"factor.id": stringValue(task.Factor.FactorID), "factor.source_hash": stringValue(task.Factor.SourceHash),
		"factor.source_hash." + task.Factor.FactorID: stringValue(task.Factor.SourceHash),
		"factor.parent_task_id":                      stringValue(task.TaskID), "factor.computed_at": stringValue(computedAt.Format(time.RFC3339Nano)),
	}
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

func deterministicBatchWriteID(patches []preparedFactorPatch, phase string, rows []*storagepb.RowFieldUpsert) string {
	// This helper is intentionally kept generic below through the concrete
	// adapter. The task identity and canonical row plan make retries idempotent.
	parts := make([]string, 0, len(patches))
	for _, patch := range patches {
		if patch.task == nil {
			continue
		}
		parts = append(parts, patch.task.TriggerEventID+"\x00"+patch.task.BindingID+"\x00"+patch.task.SubjectID+"\x00"+strconv.FormatInt(patch.task.PeriodTime, 10)+"\x00"+patch.task.Factor.FactorID)
	}
	sort.Strings(parts)
	planHash := sha256.Sum256(canonicalWritePlan(rows))
	identity := strings.Join(parts, "\x00") + "\x00" + phase + "\x00" + hex.EncodeToString(planHash[:])
	sum := sha256.Sum256([]byte(identity))
	return "factor-batch-" + hex.EncodeToString(sum[:16])
}

// mergeFactorRows collapses rows sharing the physical time-series key. This
// is what lets multiple factor definitions contribute different output fields
// to one event without producing duplicate primary keys in DuckDB.
func mergeFactorRows(rows []*storagepb.RowFieldUpsert) []*storagepb.RowFieldUpsert {
	if len(rows) < 2 {
		return rows
	}
	positions := make(map[string]int, len(rows))
	merged := make([]*storagepb.RowFieldUpsert, 0, len(rows))
	for _, row := range rows {
		if row == nil || row.GetKey() == nil {
			continue
		}
		id := factorPhysicalRowID(row.GetKey())
		position, ok := positions[id]
		if !ok {
			clone := proto.Clone(row).(*storagepb.RowFieldUpsert)
			clone.Fields = append([]*storagepb.FieldValue(nil), row.GetFields()...)
			if len(row.GetAttributes()) > 0 {
				clone.Attributes = make(map[string]*storagepb.TypedValue, len(row.GetAttributes()))
				for name, value := range row.GetAttributes() {
					clone.Attributes[name] = value
				}
			}
			positions[id] = len(merged)
			merged = append(merged, clone)
			continue
		}
		mergeFactorRow(merged[position], row)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		return factorPhysicalRowID(merged[i].GetKey()) < factorPhysicalRowID(merged[j].GetKey())
	})
	return merged
}

func mergeFactorRow(dst, src *storagepb.RowFieldUpsert) {
	if dst == nil || src == nil {
		return
	}
	for _, field := range src.GetFields() {
		if field == nil {
			continue
		}
		replaced := false
		for index, existing := range dst.Fields {
			if existing != nil && existing.GetFieldId() == field.GetFieldId() {
				dst.Fields[index] = field
				replaced = true
				break
			}
		}
		if !replaced {
			dst.Fields = append(dst.Fields, field)
		}
	}
	if len(src.GetAttributes()) > 0 {
		if dst.Attributes == nil {
			dst.Attributes = make(map[string]*storagepb.TypedValue, len(src.GetAttributes()))
		}
		for name, value := range src.GetAttributes() {
			// A physical factor row can now carry fields from several factors,
			// while the legacy metadata attributes are scalar. Keep the first
			// deterministic owner instead of letting later factors overwrite it.
			if _, exists := dst.Attributes[name]; !exists {
				dst.Attributes[name] = value
			}
		}
	}
}

func factorPhysicalRowID(key *storagepb.RowKey) string {
	if key == nil {
		return ""
	}
	series := key.GetTimeSeries()
	if series == nil {
		return string(protoBytes(key))
	}
	dataTime := series.GetDataTime()
	if parsed, err := time.Parse(time.RFC3339Nano, dataTime); err == nil {
		dataTime = parsed.UTC().Format("2006-01-02T15:04:05.000000000Z")
	}
	return encodeFactorKeyParts(series.GetSubjectId(), series.GetFreq(), dataTime, series.GetSeriesTag())
}

func encodeFactorKeyParts(parts ...string) string {
	var builder strings.Builder
	for _, part := range parts {
		builder.WriteString(strconv.Itoa(len(part)))
		builder.WriteByte(':')
		builder.WriteString(part)
	}
	return builder.String()
}

func protoBytes(message proto.Message) []byte {
	if message == nil {
		return nil
	}
	raw, _ := (proto.MarshalOptions{Deterministic: true}).Marshal(message)
	return raw
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
