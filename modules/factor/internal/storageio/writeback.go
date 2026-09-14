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
type FactorPatch struct {
	Task   *engine.FactorTask
	Result *engine.FactorResult
}

// WriteReceipt is the Storage outbox coordinate returned by PatchFactor.
type WriteReceipt struct {
	CommitID string
	NodeID   string
	StoreID  string
	Sequence uint64
}

// FactorWrite is one authorized factor patch together with its outbox receipts.
type FactorWrite struct {
	Rows     uint64
	Receipts []WriteReceipt
}

// FactorBatchWriter is implemented by StorageIO clients that can submit
// several factor patches as bounded clear/upsert batches. The task
// runner keeps the single-patch fallback for small or test-only clients.
type FactorBatchWriter interface {
	WriteFactorPatches(context.Context, []FactorPatch) ([]uint64, error)
}

// FactorReceiptWriter returns PatchFactor outbox coordinates so period
// barriers can wait on result rows instead of input watermarks.
type FactorReceiptWriter interface {
	WriteFactorReceipts(context.Context, *engine.FactorTask, *engine.FactorResult) (FactorWrite, error)
}

// FactorReceiptBatchWriter is the batch form of FactorReceiptWriter.
type FactorReceiptBatchWriter interface {
	WriteFactorReceiptsBatch(context.Context, []FactorPatch) ([]FactorWrite, error)
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

func outputManifestKey(task *engine.FactorTask) store.OutputManifestKey {
	// Persist the old routing and output definition, not a pointer into the catalog.
	cleanup := *task
	cleanup.TaskID, cleanup.TriggerEventID = "", ""
	cleanup.TriggeredAt = time.Time{}
	raw, _ := json.Marshal(cleanup)
	return store.OutputManifestKey{SourceSeriesTag: task.SourceSeriesTag, FilterSourceSeriesTag: task.FilterSourceSeriesTag, BindingID: task.BindingID, BindingGeneration: task.BindingGeneration, CleanupTaskJSON: string(raw), SubjectID: task.SubjectID, Frequency: task.Freq, PeriodTime: time.Unix(task.PeriodTime, 0).UTC()}
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
	write, err := c.WriteFactorReceipts(ctx, task, result)
	return write.Rows, err
}

func (c *Client) WriteFactorReceipts(ctx context.Context, task *engine.FactorTask, result *engine.FactorResult) (FactorWrite, error) {
	if task == nil || result == nil {
		return FactorWrite{}, fmt.Errorf("task and result are required")
	}
	if c.manifests == nil {
		return c.writeLegacyFactorPatch(ctx, task, result)
	}
	key := outputManifestKey(task)
	previous, err := c.manifests.Get(ctx, key)
	if err != nil {
		return FactorWrite{}, fmt.Errorf("load factor output manifest: %w", err)
	}
	upserts, current, err := buildFactorRows(task, result)
	if err != nil {
		return FactorWrite{}, err
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
		return FactorWrite{}, fmt.Errorf("persist factor output write intent: %w", err)
	}
	var receipts []WriteReceipt
	if len(stale) > 0 {
		clearRows, decodeErr := clearRowsForKeys(stale, task.Factor.FactorID, task.Factor.Outputs)
		if decodeErr != nil {
			return FactorWrite{}, decodeErr
		}
		cleared, err := c.writeRows(ctx, deterministicWriteID(task, "clear", clearRows), clearRows, task)
		if err != nil {
			return FactorWrite{}, err
		}
		receipts = append(receipts, cleared...)
	}
	if len(upserts) > 0 {
		written, err := c.writeRows(ctx, deterministicWriteID(task, "upsert", upserts), upserts, task)
		if err != nil {
			return FactorWrite{}, err
		}
		receipts = append(receipts, written...)
	}
	if err := c.manifests.Replace(ctx, key, current); err != nil {
		return FactorWrite{}, fmt.Errorf("replace factor output manifest: %w", err)
	}
	return FactorWrite{Rows: uint64(len(upserts)), Receipts: receipts}, nil
}

// WriteFactorPatches writes each factor through its own authorized PatchFactor
// call so owned_fields and binding_version stay scoped to one binding.
func (c *Client) WriteFactorPatches(ctx context.Context, patches []FactorPatch) ([]uint64, error) {
	writes, err := c.WriteFactorReceiptsBatch(ctx, patches)
	if err != nil {
		return nil, err
	}
	counts := make([]uint64, len(writes))
	for i, write := range writes {
		counts[i] = write.Rows
	}
	return counts, nil
}

func (c *Client) WriteFactorReceiptsBatch(ctx context.Context, patches []FactorPatch) ([]FactorWrite, error) {
	if len(patches) == 0 {
		return nil, fmt.Errorf("factor patches are required")
	}
	if c == nil || c.access == nil {
		return nil, fmt.Errorf("factor storage client is unavailable")
	}
	writes := make([]FactorWrite, len(patches))
	if c.manifests == nil {
		for i, patch := range patches {
			if patch.Task == nil || patch.Result == nil {
				return nil, fmt.Errorf("factor patch %d task and result are required", i)
			}
			write, err := c.WriteFactorReceipts(ctx, patch.Task, patch.Result)
			if err != nil {
				return nil, err
			}
			writes[i] = write
		}
		return writes, nil
	}

	prepared := make([]preparedFactorPatch, 0, len(patches))
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
		key := outputManifestKey(task)
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
		if err := c.manifests.Replace(ctx, key, pending); err != nil {
			return nil, fmt.Errorf("persist factor output write intent: %w", err)
		}
		writes[i] = FactorWrite{Rows: uint64(len(upserts))}
		prepared = append(prepared, preparedFactorPatch{key: key, task: task, current: current, stale: clearRows, upserts: upserts})
	}

	for i, patch := range prepared {
		var receipts []WriteReceipt
		if len(patch.stale) > 0 {
			cleared, err := c.writeRows(ctx, deterministicWriteID(patch.task, "clear", patch.stale), patch.stale, patch.task)
			if err != nil {
				return nil, err
			}
			receipts = append(receipts, cleared...)
		}
		if len(patch.upserts) > 0 {
			written, err := c.writeRows(ctx, deterministicWriteID(patch.task, "upsert", patch.upserts), patch.upserts, patch.task)
			if err != nil {
				return nil, err
			}
			receipts = append(receipts, written...)
		}
		writes[i].Receipts = receipts
		if err := c.manifests.Replace(ctx, patch.key, patch.current); err != nil {
			return nil, fmt.Errorf("replace factor output manifest: %w", err)
		}
	}
	return writes, nil
}

func (c *Client) writeLegacyFactorPatch(ctx context.Context, task *engine.FactorTask, result *engine.FactorResult) (FactorWrite, error) {
	if len(result.Rows) == 0 {
		return FactorWrite{}, nil
	}
	datasetID := task.ResultDatasetID
	if datasetID == "" {
		datasetID = task.TargetDataset
	}
	rows := make([]*storagepb.RowFieldUpsert, 0, len(result.Rows))
	for i, resultRow := range result.Rows {
		if resultRow.DataTime.IsZero() {
			return FactorWrite{}, fmt.Errorf("factor result row %d data_time is required", i)
		}
		row := &storagepb.RowFieldUpsert{Key: toProtoRowKey(factorRowKey{SpaceID: task.SpaceID, DatasetID: datasetID, SubjectID: task.SubjectID, Frequency: task.Freq, DataTime: resultRow.DataTime.UTC().Format(time.RFC3339Nano), SeriesTag: resultRow.SeriesTag}), Attributes: factorRowAttributes(task)}
		for _, name := range task.Factor.Outputs {
			value, exists := resultRow.Values[name]
			if !exists {
				return FactorWrite{}, fmt.Errorf("factor result row %d is missing output %s", i, name)
			}
			if value == nil {
				row.Fields = append(row.Fields, nullField(name))
				continue
			}
			number, ok := asFloat64(value)
			if !ok {
				return FactorWrite{}, fmt.Errorf("factor output %s returned non-numeric value %T", name, value)
			}
			row.Fields = append(row.Fields, doubleField(name, number))
		}
		rows = append(rows, row)
	}
	receipts, err := c.writeRows(ctx, deterministicWriteID(task, "legacy", rows), rows, task)
	if err != nil {
		return FactorWrite{}, err
	}
	return FactorWrite{Rows: uint64(len(rows)), Receipts: receipts}, nil
}

func (c *Client) ClearFactorOutputs(ctx context.Context, task *engine.FactorTask) error {
	returnError := func(err error) error { return err }
	_, err := c.WriteFactorPatch(ctx, task, &engine.FactorResult{})
	return returnError(err)
}

func buildFactorRows(task *engine.FactorTask, result *engine.FactorResult) ([]*storagepb.RowFieldUpsert, []string, error) {
	if err := ValidateDatasetOutputs(task, result); err != nil {
		return nil, nil, err
	}
	rows := make([]*storagepb.RowFieldUpsert, 0, len(result.Rows))
	keys := make([]string, 0, len(result.Rows))
	computedAt := time.Now().UTC()
	for i, resultRow := range result.Rows {
		if resultRow.DataTime.IsZero() {
			return nil, nil, fmt.Errorf("factor result row %d data_time is required", i)
		}
		subjectID, err := engine.ResultSubject(task, resultRow)
		if err != nil {
			return nil, nil, fmt.Errorf("factor result row %d: %w", i, err)
		}
		identity := factorRowKey{SpaceID: task.SpaceID, DatasetID: task.ResultDatasetID, SubjectID: subjectID, Frequency: task.Freq, DataTime: resultRow.DataTime.UTC().Format(time.RFC3339Nano), SeriesTag: resultRow.SeriesTag}
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

func (c *Client) writeRows(ctx context.Context, commitID string, rows []*storagepb.RowFieldUpsert, task *engine.FactorTask) ([]WriteReceipt, error) {
	if c == nil || c.access == nil {
		return nil, fmt.Errorf("factor storage client is unavailable")
	}
	if len(rows) == 0 {
		return nil, nil
	}
	bindingVersion, err := factorBindingVersion(task)
	if err != nil {
		return nil, err
	}
	receipts := make([]WriteReceipt, 0, len(rows))
	for index, row := range rows {
		if row == nil {
			continue
		}
		rowCommitID := commitID
		if len(rows) > 1 {
			rowCommitID = fmt.Sprintf("%s-%d", commitID, index)
		}
		rsp, err := c.access.PatchFactor(ctx, &storagepb.PrimaryPatchFactorReq{
			AuthInfo: c.auth, CommitId: rowCommitID, BindingVersion: bindingVersion,
			OwnedFields: ownedFieldIDs(row), Row: row,
		})
		if err != nil {
			return nil, fmt.Errorf("write factor patch: %w", err)
		}
		if err := ensureStorageOK("write factor patch", rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		receipts = append(receipts, receiptFromProto(rsp.GetReceipt()))
	}
	return receipts, nil
}

func factorBindingVersion(task *engine.FactorTask) (string, error) {
	if task == nil {
		return "", fmt.Errorf("binding_version is required")
	}
	if value := strings.TrimSpace(task.BindingGeneration); value != "" {
		return value, nil
	}
	if value := strings.TrimSpace(task.BindingID); value != "" {
		return value, nil
	}
	if value := strings.TrimSpace(task.Factor.FactorID); value != "" {
		return value, nil
	}
	return "", fmt.Errorf("binding_version is required")
}

func ownedFieldIDs(row *storagepb.RowFieldUpsert) []string {
	if row == nil {
		return nil
	}
	out := make([]string, 0, len(row.GetFields()))
	seen := make(map[string]struct{}, len(row.GetFields()))
	for _, field := range row.GetFields() {
		id := strings.TrimSpace(field.GetFieldId())
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func receiptFromProto(receipt *storagepb.WriteReceipt) WriteReceipt {
	if receipt == nil {
		return WriteReceipt{}
	}
	pos := receipt.GetPosition()
	return WriteReceipt{
		CommitID: receipt.GetCommitId(),
		NodeID:   pos.GetNodeId(),
		StoreID:  pos.GetStoreId(),
		Sequence: pos.GetSequence(),
	}
}

func deterministicWriteID(task *engine.FactorTask, phase string, rows []*storagepb.RowFieldUpsert) string {
	planHash := sha256.Sum256(canonicalWritePlan(rows))
	sum := sha256.Sum256([]byte(task.TriggerEventID + "\x00" + task.BindingID + "\x00" + task.BindingGeneration + "\x00" + task.SubjectID + "\x00" + strconv.FormatInt(task.PeriodTime, 10) + "\x00" + phase + "\x00" + hex.EncodeToString(planHash[:])))
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
