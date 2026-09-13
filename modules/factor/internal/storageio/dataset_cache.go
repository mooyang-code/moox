package storageio

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/inputcache"
)

var datasetCacheKeys = []string{"subject_id", "freq", "data_time", "series_tag"}

type datasetSchemaSpec struct {
	columns    []inputcache.Column
	baseFields []string
}

// DatasetCache is a disposable Primary-window cache keyed by Dataset + schema.
type DatasetCache struct {
	manager *inputcache.Manager
	mu      sync.Mutex
	schemas map[string]datasetSchemaSpec
	missing map[string]struct{}
}

func NewDatasetCache(manager *inputcache.Manager) *DatasetCache {
	return &DatasetCache{
		manager: manager,
		schemas: make(map[string]datasetSchemaSpec),
		missing: make(map[string]struct{}),
	}
}

func datasetSchemaMapKey(datasetID, schemaID string) string {
	return strings.TrimSpace(datasetID) + "\x00" + strings.TrimSpace(schemaID)
}

// RegisterSchema binds a Dataset identity to its full Storage schema.
// New schema IDs open an empty generation; output-column values are not required for hits.
func (c *DatasetCache) RegisterSchema(datasetID, schemaID string, columns []inputcache.Column, baseFields []string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.schemas[datasetSchemaMapKey(datasetID, schemaID)] = datasetSchemaSpec{
		columns:    append([]inputcache.Column(nil), columns...),
		baseFields: compactUniqueStrings(baseFields),
	}
}

func (c *DatasetCache) schema(datasetID, schemaID string) (datasetSchemaSpec, bool) {
	if c == nil {
		return datasetSchemaSpec{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	spec, ok := c.schemas[datasetSchemaMapKey(datasetID, schemaID)]
	return spec, ok
}

func (c *DatasetCache) rememberMissing(ids []string) {
	if c == nil || len(ids) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range ids {
		c.missing[id] = struct{}{}
	}
}

func (c *DatasetCache) forgetMissing(ids []string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range ids {
		delete(c.missing, id)
	}
}

func (c *DatasetCache) allMissing(ids []string) bool {
	if c == nil || len(ids) == 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range ids {
		if _, ok := c.missing[id]; !ok {
			return false
		}
	}
	return true
}

// DeleteWindow removes cached keys so the next read must return to Primary.
func (c *DatasetCache) DeleteWindow(ctx context.Context, key WindowKey, period time.Time) error {
	if c == nil || c.manager == nil {
		return nil
	}
	schemaID := strings.TrimSpace(key.StorageSchemaID)
	datasetID := strings.TrimSpace(key.SourceDataset)
	spec, ok := c.schema(datasetID, schemaID)
	if !ok {
		return nil
	}
	handle, err := c.manager.Get(ctx, inputcache.SourceKey{SpaceID: key.SpaceID, DatasetID: datasetID}, schemaID, spec.columns, datasetCacheKeys)
	if err != nil {
		return err
	}
	c.forgetMissing([]string{datasetMissingID(key, period)})
	return handle.Fill(func(db *inputcache.Database, _ uint64) error {
		return db.DeleteKeys(ctx, [][]any{{key.SubjectID, key.Freq, period.UTC(), datasetWindowSeriesTag(key)}})
	})
}

// ReadPeriodChunk serves a Primary lookback from cache, filling complete ready rows on miss.
func (c *DatasetCache) ReadPeriodChunk(
	ctx context.Context,
	client *Client,
	key WindowKey,
	startTime, endTime time.Time,
	lookbackPeriods int,
	columns []string,
) (*RangeChunk, bool, error) {
	if c == nil || c.manager == nil || client == nil {
		return nil, false, nil
	}
	schemaID := strings.TrimSpace(key.StorageSchemaID)
	datasetID := strings.TrimSpace(key.SourceDataset)
	if schemaID == "" || datasetID == "" {
		return nil, false, nil
	}
	spec, ok := c.schema(datasetID, schemaID)
	if !ok {
		return nil, false, nil
	}
	if lookbackPeriods < 1 {
		lookbackPeriods = 1
	}
	times, err := lookbackPeriodTimes(startTime.UTC(), key.Freq, lookbackPeriods)
	if err != nil {
		return nil, true, nonRetryableRead(err)
	}
	missingIDs := make([]string, 0, len(times))
	for _, at := range times {
		missingIDs = append(missingIDs, datasetMissingID(key, at))
	}
	handle, err := c.manager.Get(ctx, inputcache.SourceKey{SpaceID: key.SpaceID, DatasetID: datasetID}, schemaID, spec.columns, datasetCacheKeys)
	if err != nil {
		return nil, true, err
	}
	chunk, complete, err := c.readHandleWindow(ctx, handle, spec, key, startTime, endTime, lookbackPeriods, columns)
	if err != nil {
		return nil, true, err
	}
	if complete {
		return chunk, true, nil
	}
	if c.allMissing(missingIDs) {
		return chunk, true, nonRetryableRead(ErrInsufficientHistory)
	}
	fetched, err := client.ReadDatasetWindow(ctx, key, startTime, endTime, lookbackPeriods, spec.baseFields)
	if err != nil {
		return nil, true, err
	}
	if err := c.fillReadyRows(ctx, handle, spec, key, fetched); err != nil {
		return nil, true, err
	}
	chunk, complete, err = c.readHandleWindow(ctx, handle, spec, key, startTime, endTime, lookbackPeriods, columns)
	if err != nil {
		return nil, true, err
	}
	if complete {
		return chunk, true, nil
	}
	c.rememberMissing(missingIDs)
	return chunk, true, nonRetryableRead(ErrInsufficientHistory)
}

func (c *DatasetCache) readHandleWindow(
	ctx context.Context,
	handle *inputcache.Handle,
	spec datasetSchemaSpec,
	key WindowKey,
	startTime, endTime time.Time,
	lookback int,
	columns []string,
) (*RangeChunk, bool, error) {
	var rows [][]any
	err := handle.Use(func(db *inputcache.Database, _ uint64) error {
		var readErr error
		rows, readErr = db.ReadWindow(ctx, inputcache.WindowQuery{
			TimeColumn: "data_time",
			Through:    startTime.UTC(),
			Lookback:   lookback,
			Filters: map[string][]any{
				"subject_id": {key.SubjectID},
				"freq":       {key.Freq},
			},
		})
		return readErr
	})
	if err != nil {
		return nil, false, err
	}
	chunk, err := rangeChunkFromCacheRows(spec.columns, spec.baseFields, rows, columns, startTime, endTime)
	if err != nil {
		return nil, false, nonRetryableRead(err)
	}
	return chunk, RequireDatasetLookback(chunk, lookback) == nil && cacheRowsHaveBaseFields(spec, rows), nil
}

func (c *DatasetCache) fillReadyRows(ctx context.Context, handle *inputcache.Handle, spec datasetSchemaSpec, key WindowKey, chunk *RangeChunk) error {
	if chunk == nil || chunk.Frame == nil || len(chunk.Frame.Rows) == 0 {
		return nil
	}
	positions := make(map[string]int, len(spec.columns))
	for i, col := range spec.columns {
		positions[col.Name] = i
	}
	base := make(map[string]struct{}, len(spec.baseFields))
	for _, name := range spec.baseFields {
		base[name] = struct{}{}
	}
	requested := make(map[string]int, len(chunk.Frame.Columns))
	for i, name := range chunk.Frame.Columns {
		requested[name] = i
	}
	payload := make([][]any, 0, len(chunk.Frame.Rows))
	for i, values := range chunk.Frame.Rows {
		row := make([]any, len(spec.columns))
		if idx, ok := positions["subject_id"]; ok {
			row[idx] = key.SubjectID
		}
		if idx, ok := positions["freq"]; ok {
			row[idx] = key.Freq
		}
		if idx, ok := positions["data_time"]; ok {
			row[idx] = chunk.Frame.DataTimes[i].UTC()
		}
		if idx, ok := positions["series_tag"]; ok {
			tag := datasetWindowSeriesTag(key)
			if i < len(chunk.Frame.SeriesTags) {
				tag = chunk.Frame.SeriesTags[i]
			}
			row[idx] = tag
		}
		complete := true
		for name := range base {
			idx, ok := requested[name]
			if !ok || idx >= len(values) || values[idx] == nil {
				complete = false
				break
			}
			row[positions[name]] = values[idx]
		}
		if complete {
			payload = append(payload, row)
		}
	}
	if len(payload) == 0 {
		return nil
	}
	for _, row := range payload {
		c.forgetMissing([]string{datasetMissingID(key, row[positions["data_time"]].(time.Time))})
	}
	return handle.Fill(func(db *inputcache.Database, _ uint64) error {
		return db.Upsert(ctx, payload, time.Now().UTC())
	})
}

func cacheRowsHaveBaseFields(spec datasetSchemaSpec, rows [][]any) bool {
	if len(rows) == 0 {
		return false
	}
	positions := make(map[string]int, len(spec.columns))
	for i, col := range spec.columns {
		positions[col.Name] = i
	}
	for _, row := range rows {
		for _, name := range spec.baseFields {
			idx, ok := positions[name]
			if !ok || idx >= len(row) || row[idx] == nil {
				return false
			}
		}
	}
	return true
}

func rangeChunkFromCacheRows(
	schema []inputcache.Column,
	baseFields []string,
	rows [][]any,
	columns []string,
	startTime, endTime time.Time,
) (*RangeChunk, error) {
	if len(columns) == 0 {
		columns = append([]string(nil), baseFields...)
	}
	positions := make(map[string]int, len(schema))
	for i, col := range schema {
		positions[col.Name] = i
	}
	frame := &engine.DataFrame{Columns: append([]string(nil), columns...)}
	for _, row := range rows {
		if len(row) != len(schema) {
			continue
		}
		timeIdx := positions["data_time"]
		at, _ := row[timeIdx].(time.Time)
		out := make([]any, len(columns))
		for i, name := range columns {
			idx, ok := positions[name]
			if !ok || idx >= len(row) {
				out[i] = nil
				continue
			}
			out[i] = row[idx]
		}
		frame.Rows = append(frame.Rows, out)
		frame.DataTimes = append(frame.DataTimes, at.UTC())
		if idx, ok := positions["series_tag"]; ok {
			tag, _ := row[idx].(string)
			frame.SeriesTags = append(frame.SeriesTags, tag)
		} else {
			frame.SeriesTags = append(frame.SeriesTags, "")
		}
	}
	return rangeChunkFromFrame(frame, startTime, endTime)
}

func rangeChunkFromFrame(frame *engine.DataFrame, startTime, endTime time.Time) (*RangeChunk, error) {
	if frame == nil {
		frame = &engine.DataFrame{}
	}
	target := make([]time.Time, 0)
	seen := map[int64]struct{}{}
	indexedTo := time.Time{}
	for _, at := range frame.DataTimes {
		if at.After(indexedTo) {
			indexedTo = at
		}
		if at.Before(startTime) || !at.Before(endTime) {
			continue
		}
		nanos := at.UTC().UnixNano()
		if _, ok := seen[nanos]; ok {
			continue
		}
		seen[nanos] = struct{}{}
		target = append(target, at.UTC())
	}
	return &RangeChunk{Frame: frame, TargetPeriods: target, Complete: true, IndexedTo: indexedTo}, nil
}

func datasetMissingID(key WindowKey, at time.Time) string {
	return strings.Join([]string{
		key.SpaceID, key.SourceDataset, key.StorageSchemaID, key.SubjectID, key.Freq,
		at.UTC().Format(time.RFC3339Nano), datasetWindowSeriesTag(key),
	}, "\x00")
}
