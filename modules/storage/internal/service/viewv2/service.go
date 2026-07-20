package viewv2

import (
	"context"
	"crypto/hmac"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewbuilder/eventconsumer"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	viewbleve "github.com/mooyang-code/moox/modules/storage/internal/service/viewindex/bleve"
	viewduckdb "github.com/mooyang-code/moox/modules/storage/internal/service/viewindex/duckdb"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/jetstream"
	"google.golang.org/protobuf/proto"
	trpc "trpc.group/trpc-go/trpc-go"
)

type Service struct {
	engines     map[string]viewindex.QueryEngine
	indexEngine map[string]string
	schemas     map[string]viewindex.ViewIndexSchema
	views       map[viewRef]*viewRuntime
	indexView   map[string]viewRef
	authSecret  string
	primaryAuth *pb.AuthInfo
	mu          sync.RWMutex
	byData      map[datasetRef]map[string]struct{}
}

type datasetRef struct{ spaceID, datasetID string }
type viewRef struct{ spaceID, viewID string }

type viewRuntime struct {
	mu     sync.Mutex
	active string
	next   string
	status string
}

func New(root, authSecret string) (*Service, error) {
	if strings.TrimSpace(authSecret) == "" {
		return nil, errors.New("view auth secret is required")
	}
	bleveEngine, err := viewbleve.Open(viewbleve.Options{Path: filepath.Join(root, "bleve")})
	if err != nil {
		return nil, err
	}
	engines := map[string]viewindex.QueryEngine{"bleve": bleveEngine}
	if duckdbEngine, err := viewduckdb.OpenIndexManager(viewduckdb.IndexManagerOptions{Root: filepath.Join(root, "duckdb")}); err == nil {
		engines["duckdb"] = duckdbEngine
	}
	service := &Service{
		engines:     engines,
		indexEngine: make(map[string]string),
		schemas:     make(map[string]viewindex.ViewIndexSchema),
		views:       make(map[viewRef]*viewRuntime),
		indexView:   make(map[string]viewRef),
		authSecret:  authSecret,
		byData:      make(map[datasetRef]map[string]struct{}),
	}
	for name, engine := range engines {
		ids, err := engine.List(trpc.BackgroundContext())
		if err != nil {
			continue
		}
		for _, id := range ids {
			service.indexEngine[id] = name
		}
	}
	return service, nil
}

var _ pb.ViewIndexService = (*Service)(nil)
var _ pb.DataViewService = (*Service)(nil)

func (s *Service) PrepareViewIndex(ctx context.Context, req *pb.PrepareViewIndexReq) (*pb.PrepareViewIndexRsp, error) {
	if req == nil || req.GetSchema() == nil || req.GetIndexId() == "" {
		return &pb.PrepareViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("index_id and schema are required"))}, nil
	}
	if err := s.authorize(req.GetAuthInfo()); err != nil {
		return &pb.PrepareViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	sch := req.GetSchema()
	engineName := strings.ToLower(strings.TrimSpace(sch.GetEngine()))
	if engineName == "" {
		engineName = strings.ToLower(strings.TrimSpace(req.GetEngine()))
	}
	engine := s.engines[engineName]
	if engine == nil {
		return &pb.PrepareViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, fmt.Errorf("view engine %q is unavailable", engineName))}, nil
	}
	schema := viewindex.ViewIndexSchema{SpaceID: sch.GetSpaceId(), ViewID: sch.GetViewId(), ViewVersion: sch.GetViewVersion(), Engine: engineName, Columns: sch.GetColumns(), SchemaHash: sch.GetViewSchemaHash()}
	err := engine.Prepare(ctx, req.GetIndexId(), schema)
	if err != nil {
		return &pb.PrepareViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	s.mu.Lock()
	s.removeIndexMappingsLocked(req.GetIndexId())
	s.indexEngine[req.GetIndexId()] = engineName
	s.schemas[req.GetIndexId()] = schema
	viewKey := viewRef{spaceID: schema.SpaceID, viewID: schema.ViewID}
	runtime := s.views[viewKey]
	if runtime == nil {
		runtime = &viewRuntime{active: req.GetIndexId(), status: "active"}
		s.views[viewKey] = runtime
	} else if runtime.active != req.GetIndexId() {
		runtime.next = req.GetIndexId()
		runtime.status = "building"
	}
	s.indexView[req.GetIndexId()] = viewKey
	for _, column := range sch.GetColumns() {
		dataset := viewColumnDataset(column)
		if dataset != "" {
			ref := datasetRef{spaceID: sch.GetSpaceId(), datasetID: dataset}
			if s.byData[ref] == nil {
				s.byData[ref] = make(map[string]struct{})
			}
			s.byData[ref][req.GetIndexId()] = struct{}{}
		}
	}
	s.mu.Unlock()
	return &pb.PrepareViewIndexRsp{RetInfo: retinfo.Success("success")}, nil
}

func (s *Service) ApplyViewIndex(ctx context.Context, req *pb.ApplyViewIndexReq) (*pb.ApplyViewIndexRsp, error) {
	if req == nil || req.GetBatch() == nil || req.GetIndexId() == "" {
		return &pb.ApplyViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("index_id and batch are required"))}, nil
	}
	if err := s.authorize(req.GetAuthInfo()); err != nil {
		return &pb.ApplyViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	b := req.GetBatch()
	writes := make([]viewindex.RowWrite, 0, len(b.GetRowWrites()))
	for _, w := range b.GetRowWrites() {
		if w == nil || w.GetKey() == nil || w.GetKey().GetRowKey() == nil {
			return &pb.ApplyViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("row key is required"))}, nil
		}
		writes = append(writes, viewindex.RowWrite{Key: viewindex.RowKey{Key: w.GetKey().GetRowKey()}, Fields: w.GetFields(), Attributes: w.GetAttributes()})
	}
	mode := viewindex.LiveWrite
	if b.GetWriteMode() == "BACKFILL" {
		mode = viewindex.Backfill
	}
	engine, err := s.engineFor(req.GetIndexId())
	if err == nil {
		err = engine.Apply(ctx, req.GetIndexId(), viewindex.ViewIndexApplyBatch{RowWrites: writes, ViewRevision: b.GetViewRevision(), ViewSchemaHash: b.GetViewSchemaHash(), WriteMode: mode})
	}
	if err != nil {
		return &pb.ApplyViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	return &pb.ApplyViewIndexRsp{RetInfo: retinfo.Success("success")}, nil
}

func (s *Service) StatViewIndex(ctx context.Context, req *pb.StatViewIndexReq) (*pb.StatViewIndexRsp, error) {
	if req == nil || req.GetIndexId() == "" {
		return &pb.StatViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("index_id is required"))}, nil
	}
	if err := s.authorize(req.GetAuthInfo()); err != nil {
		return &pb.StatViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	engine, err := s.engineFor(req.GetIndexId())
	if err != nil {
		return &pb.StatViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	st, err := engine.Stat(ctx, req.GetIndexId())
	if err != nil {
		return &pb.StatViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	return &pb.StatViewIndexRsp{RetInfo: retinfo.Success("success"), Stats: &pb.ViewIndexStats{Exists: st.Exists, EntryCount: uint64(st.EntryCount), ViewSchemaHash: st.SchemaHash, ViewVersion: st.ViewVersion, IndexedFrom: st.IndexedFrom, IndexedTo: st.IndexedTo, UpdatedAt: st.UpdatedAt}}, nil
}

func (s *Service) RemoveViewIndex(ctx context.Context, req *pb.RemoveViewIndexReq) (*pb.RemoveViewIndexRsp, error) {
	if req == nil || req.GetIndexId() == "" {
		return &pb.RemoveViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("index_id is required"))}, nil
	}
	if err := s.authorize(req.GetAuthInfo()); err != nil {
		return &pb.RemoveViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	engine, err := s.engineFor(req.GetIndexId())
	if err != nil {
		return &pb.RemoveViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	if err := engine.Remove(ctx, req.GetIndexId()); err != nil {
		return &pb.RemoveViewIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	s.mu.Lock()
	s.removeIndexMappingsLocked(req.GetIndexId())
	delete(s.indexEngine, req.GetIndexId())
	delete(s.schemas, req.GetIndexId())
	var runtime *viewRuntime
	if viewKey, ok := s.indexView[req.GetIndexId()]; ok {
		runtime = s.views[viewKey]
		delete(s.indexView, req.GetIndexId())
	}
	s.mu.Unlock()
	if runtime != nil {
		runtime.mu.Lock()
		if runtime.active == req.GetIndexId() {
			runtime.active = ""
		}
		if runtime.next == req.GetIndexId() {
			runtime.next = ""
		}
		runtime.mu.Unlock()
	}
	return &pb.RemoveViewIndexRsp{RetInfo: retinfo.Success("success")}, nil
}

func (s *Service) ListViewIndexes(ctx context.Context, req *pb.ListViewIndexesReq) (*pb.ListViewIndexesRsp, error) {
	if req == nil {
		return &pb.ListViewIndexesRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("request is required"))}, nil
	}
	if err := s.authorize(req.GetAuthInfo()); err != nil {
		return &pb.ListViewIndexesRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	var ids []string
	for _, engine := range s.engines {
		engineIDs, err := engine.List(ctx)
		if err != nil {
			return &pb.ListViewIndexesRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
		}
		ids = append(ids, engineIDs...)
	}
	sort.Strings(ids)
	out := make([]*pb.ViewIndexDescriptor, 0, len(ids))
	for _, id := range ids {
		engine, err := s.engineFor(id)
		if err != nil {
			continue
		}
		st, _ := engine.Stat(ctx, id)
		out = append(out, &pb.ViewIndexDescriptor{IndexId: id, Engine: engine.Engine(), Stats: &pb.ViewIndexStats{Exists: st.Exists, EntryCount: uint64(st.EntryCount), ViewSchemaHash: st.SchemaHash, ViewVersion: st.ViewVersion}})
	}
	return &pb.ListViewIndexesRsp{RetInfo: retinfo.Success("success"), Indexes: out}, nil
}

func (s *Service) QueryTimeSeriesIndex(ctx context.Context, req *pb.QueryTimeSeriesIndexReq) (*pb.QueryTimeSeriesIndexRsp, error) {
	if req == nil {
		return &pb.QueryTimeSeriesIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("request is required"))}, nil
	}
	if err := s.authorize(req.GetAuthInfo()); err != nil {
		return &pb.QueryTimeSeriesIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	rows, err := s.query(ctx, req.GetIndexId(), req.GetKeys(), req.GetFieldIds())
	if err != nil {
		return &pb.QueryTimeSeriesIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	return &pb.QueryTimeSeriesIndexRsp{RetInfo: retinfo.Success("success"), Rows: rows}, nil
}
func (s *Service) SearchRecordIndex(ctx context.Context, req *pb.SearchRecordIndexReq) (*pb.SearchRecordIndexRsp, error) {
	if req == nil {
		return &pb.SearchRecordIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("request is required"))}, nil
	}
	if err := s.authorize(req.GetAuthInfo()); err != nil {
		return &pb.SearchRecordIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	rows, err := s.query(ctx, req.GetIndexId(), req.GetKeys(), req.GetFieldIds())
	if err != nil {
		return &pb.SearchRecordIndexRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	return &pb.SearchRecordIndexRsp{RetInfo: retinfo.Success("success"), Rows: rows}, nil
}

// QueryTimeSeriesRows serves the public DataView read contract. The View
// process intentionally accepts explicit keys only; callers that need a
// range first resolve its keys from metadata/PrimaryStore and then issue
// bounded requests. DataNode never exposes a range scan API.
func (s *Service) QueryTimeSeriesRows(ctx context.Context, req *pb.QueryTimeSeriesRowsReq) (*pb.QueryTimeSeriesRowsRsp, error) {
	if req == nil || req.GetViewId() == "" || (len(req.GetKeys()) == 0 && req.GetTimeRange() == nil) {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("view_id and keys or time_range are required"))}, nil
	}
	if err := s.authorize(req.GetAuthInfo()); err != nil {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	keys := make([]*pb.RowKey, 0, len(req.GetKeys()))
	explicit := true
	for _, k := range req.GetKeys() {
		if k == nil {
			return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("time-series key is required"))}, nil
		}
		if k.GetDataTime() == "" {
			explicit = false
		}
		keys = append(keys, timeSeriesRowKey(k))
	}
	indexID, runtime := s.activeIndex(req.GetSpaceId(), req.GetViewId())
	if runtime != nil {
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		indexID = runtime.active
	}
	engine, err := s.engineFor(indexID)
	if err != nil {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	var rows []*pb.RowFieldValues
	if explicit && req.GetTimeRange() == nil {
		rows, err = engine.Query(ctx, indexID, keys, nil)
		if err == nil {
			for index := range rows {
				rows[index] = selectFields(rows[index], req.GetColumnNames())
			}
		}
	} else {
		rows, err = scanAll(ctx, engine, indexID)
		if err == nil {
			rows, err = filterTimeSeriesRows(rows, req)
		}
	}
	if err != nil {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	out := make([]*pb.TimeSeriesRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, &pb.TimeSeriesRow{Key: rowToTimeSeriesKey(row.GetKey()), Fields: row.GetFields()})
	}
	return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Success("success"), Rows: out, Complete: true}, nil
}

// SearchRecordRows is the explicit-key counterpart for record Views. Full
// text/range search remains an upper-layer concern; this process only serves
// materialized rows that the caller names.
func (s *Service) SearchRecordRows(ctx context.Context, req *pb.SearchRecordRowsReq) (*pb.SearchRecordRowsRsp, error) {
	if req == nil || req.GetViewId() == "" || (len(req.GetKeys()) == 0 && strings.TrimSpace(req.GetTextQuery()) == "") {
		return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("view_id and keys or text_query are required"))}, nil
	}
	if err := s.authorize(req.GetAuthInfo()); err != nil {
		return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	keys := make([]*pb.RowKey, 0, len(req.GetKeys()))
	for _, k := range req.GetKeys() {
		if k == nil {
			return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("record key is required"))}, nil
		}
		keys = append(keys, recordRowKey(k))
	}
	indexID, runtime := s.activeIndex(req.GetSpaceId(), req.GetViewId())
	if runtime != nil {
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		indexID = runtime.active
	}
	engine, err := s.engineFor(indexID)
	if err != nil {
		return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	var rows []*pb.RowFieldValues
	if searcher, ok := engine.(interface {
		Search(context.Context, string, string, []string, int) ([]*pb.RowFieldValues, error)
	}); ok && strings.TrimSpace(req.GetTextQuery()) != "" {
		pageNo, size := queryPage(req.GetPage())
		rows, err = searcher.Search(ctx, indexID, req.GetTextQuery(), nil, pageNo*size)
	} else {
		rows, err = engine.Query(ctx, indexID, keys, nil)
	}
	if err != nil {
		return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	rows = filterRecordRows(rows, req)
	out := make([]*pb.RecordRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, &pb.RecordRow{Key: rowToRecordKey(row.GetKey()), Fields: row.GetFields()})
	}
	return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Success("success"), Rows: out, Complete: true}, nil
}

func scanAll(ctx context.Context, engine viewindex.QueryEngine, id string) ([]*pb.RowFieldValues, error) {
	const batchSize = 500
	var rows []*pb.RowFieldValues
	for offset := 0; ; offset += batchSize {
		batch, err := engine.Scan(ctx, id, offset, batchSize)
		if err != nil {
			return nil, err
		}
		rows = append(rows, batch...)
		if len(batch) < batchSize {
			return rows, nil
		}
	}
}

func filterTimeSeriesRows(rows []*pb.RowFieldValues, req *pb.QueryTimeSeriesRowsReq) ([]*pb.RowFieldValues, error) {
	if len(req.GetFilters()) != 0 {
		return nil, errors.New("structured view filters are not supported")
	}
	start, end, err := parseTimeRange(req.GetTimeRange())
	if err != nil {
		return nil, err
	}
	matches := make([]*pb.RowFieldValues, 0, len(rows))
	for _, row := range rows {
		key := row.GetKey()
		ts := key.GetTimeSeries()
		if ts == nil || (req.GetSpaceId() != "" && key.GetSpaceId() != req.GetSpaceId()) {
			continue
		}
		if !matchesTimeSeriesScope(ts, req.GetKeys()) {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, ts.GetDataTime())
		if err != nil || (!start.IsZero() && at.Before(start)) || (!end.IsZero() && at.After(end)) {
			continue
		}
		matches = append(matches, selectFields(row, req.GetColumnNames()))
	}
	desc := len(req.GetSorts()) > 0 && req.GetSorts()[0].GetDesc()
	sort.Slice(matches, func(i, j int) bool {
		left, right := matches[i].GetKey().GetTimeSeries().GetDataTime(), matches[j].GetKey().GetTimeSeries().GetDataTime()
		if desc {
			return left > right
		}
		return left < right
	})
	return pageRows(matches, req.GetPage(), int(req.GetLimit())), nil
}

func filterRecordRows(rows []*pb.RowFieldValues, req *pb.SearchRecordRowsReq) []*pb.RowFieldValues {
	start, end := "", ""
	if req.GetVersionRange() != nil {
		start, end = req.GetVersionRange().GetStartVersion(), req.GetVersionRange().GetEndVersion()
	}
	matches := make([]*pb.RowFieldValues, 0, len(rows))
	for _, row := range rows {
		key := row.GetKey()
		record := key.GetRecord()
		if record == nil || (req.GetSpaceId() != "" && key.GetSpaceId() != req.GetSpaceId()) {
			continue
		}
		version := record.GetVersion()
		if (start != "" && version < start) || (end != "" && version > end) {
			continue
		}
		matches = append(matches, selectFields(row, req.GetColumnNames()))
	}
	desc := len(req.GetSorts()) > 0 && req.GetSorts()[0].GetDesc()
	sort.Slice(matches, func(i, j int) bool {
		left, right := matches[i].GetKey().GetRecord().GetVersion(), matches[j].GetKey().GetRecord().GetVersion()
		if desc {
			return left > right
		}
		return left < right
	})
	return pageRows(matches, req.GetPage(), 0)
}

func parseTimeRange(value *pb.TimeRange) (time.Time, time.Time, error) {
	if value == nil {
		return time.Time{}, time.Time{}, nil
	}
	var start, end time.Time
	var err error
	if value.GetStartTime() != "" {
		start, err = time.Parse(time.RFC3339Nano, value.GetStartTime())
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
	}
	if value.GetEndTime() != "" {
		end, err = time.Parse(time.RFC3339Nano, value.GetEndTime())
	}
	return start, end, err
}

func matchesTimeSeriesScope(row *pb.TimeSeriesRowKey, scopes []*pb.TimeSeriesKey) bool {
	if len(scopes) == 0 {
		return true
	}
	for _, scope := range scopes {
		if scope != nil && (scope.GetSubjectId() == "" || scope.GetSubjectId() == row.GetSubjectId()) &&
			(scope.GetFreq() == "" || scope.GetFreq() == row.GetFreq()) {
			return true
		}
	}
	return false
}

func queryPage(page *pb.Page) (int, int) {
	pageNo, size := 1, 100
	if page != nil {
		if page.GetPage() > 0 {
			pageNo = int(page.GetPage())
		}
		if page.GetSize() > 0 {
			size = int(page.GetSize())
		}
	}
	return pageNo, size
}

func pageRows[T any](rows []T, page *pb.Page, limit int) []T {
	pageNo, size := queryPage(page)
	if limit > 0 && limit < size {
		size = limit
		pageNo = 1
	}
	start := (pageNo - 1) * size
	if start >= len(rows) {
		return nil
	}
	end := start + size
	if end > len(rows) {
		end = len(rows)
	}
	return rows[start:end]
}

func selectFields(row *pb.RowFieldValues, fields []string) *pb.RowFieldValues {
	if len(fields) == 0 {
		return proto.Clone(row).(*pb.RowFieldValues)
	}
	want := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		want[field] = struct{}{}
	}
	out := &pb.RowFieldValues{Key: proto.Clone(row.GetKey()).(*pb.RowKey)}
	for _, field := range row.GetFields() {
		_, exact := want[field.GetFieldId()]
		name := field.GetFieldId()
		if index := strings.LastIndexByte(name, '.'); index >= 0 {
			name = name[index+1:]
		}
		_, suffix := want[name]
		if exact || suffix {
			out.Fields = append(out.Fields, proto.Clone(field).(*pb.FieldValue))
		}
	}
	return out
}

func rowToTimeSeriesKey(k *pb.RowKey) *pb.TimeSeriesKey {
	if k == nil || k.GetTimeSeries() == nil {
		return nil
	}
	return &pb.TimeSeriesKey{SpaceId: k.GetSpaceId(), DatasetId: k.GetDatasetId(), SubjectId: k.GetTimeSeries().GetSubjectId(), Freq: k.GetTimeSeries().GetFreq(), DataTime: k.GetTimeSeries().GetDataTime()}
}

func timeSeriesRowKey(k *pb.TimeSeriesKey) *pb.RowKey {
	return &pb.RowKey{SpaceId: k.GetSpaceId(), DatasetId: k.GetDatasetId(), Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: k.GetSubjectId(), Freq: k.GetFreq(), DataTime: k.GetDataTime()}}}
}

func recordRowKey(k *pb.RecordKey) *pb.RowKey {
	return &pb.RowKey{SpaceId: k.GetSpaceId(), DatasetId: k.GetDatasetId(), Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: k.GetRecordId(), Version: k.GetVersion()}}}
}

func rowToRecordKey(k *pb.RowKey) *pb.RecordKey {
	if k == nil || k.GetRecord() == nil {
		return nil
	}
	return &pb.RecordKey{SpaceId: k.GetSpaceId(), DatasetId: k.GetDatasetId(), RecordId: k.GetRecord().GetRecordId(), Version: k.GetRecord().GetVersion()}
}
func (s *Service) query(ctx context.Context, id string, keys []*pb.RowKey, fields []string) ([]*pb.RowFieldValues, error) {
	if id == "" || len(keys) == 0 {
		return nil, errors.New("index_id and keys are required")
	}
	engine, err := s.engineFor(id)
	if err != nil {
		return nil, err
	}
	rows, err := engine.Query(ctx, id, keys, fields)
	if err != nil {
		return nil, fmt.Errorf("query view index: %w", err)
	}
	return rows, nil
}

// StartEventConsumer binds the single storage_view durable and applies field
// events to prepared indexes. It deliberately uses one Fetch(1) loop; the
// same Dataset remains ordered while unrelated Dataset Subjects may interleave.
func (s *Service) StartEventConsumer(ctx context.Context, client *jetstream.Client) (func(), error) {
	if client == nil {
		return nil, errors.New("eventbus client is required")
	}
	consumer, err := client.EnsurePullConsumer(ctx, jetstream.ConsumerConfig{Stream: "MOOX_STORAGE", Durable: "storage_view", FilterSubject: eventconsumer.DatasetFieldsChangedSubjectPrefix + ".>", AckWait: 30 * time.Second, MaxDeliver: -1, MaxAckPending: 1, FetchMaxWait: time.Second})
	if err != nil {
		return nil, err
	}
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer consumer.Close()
		for loopCtx.Err() == nil {
			deliveries, fetchErr := consumer.Fetch(loopCtx, 1)
			if fetchErr != nil {
				if loopCtx.Err() != nil {
					return
				}
				timer := time.NewTimer(250 * time.Millisecond)
				select {
				case <-timer.C:
				case <-loopCtx.Done():
					if !timer.Stop() {
						<-timer.C
					}
					return
				}
				continue
			}
			for _, delivery := range deliveries {
				for loopCtx.Err() == nil {
					err := s.applyDelivery(loopCtx, delivery)
					if err == nil {
						_ = delivery.Ack(loopCtx)
						break
					}
					if isPermanentDeliveryError(err) {
						_ = delivery.Term(loopCtx)
						break
					}
					// Keep the delivery pending while retrying. NAK would release
					// MaxAckPending and allow a later event to overtake it.
					_ = delivery.InProgress(loopCtx)
					timer := time.NewTimer(time.Second)
					select {
					case <-timer.C:
					case <-loopCtx.Done():
						if !timer.Stop() {
							<-timer.C
						}
					}
				}
			}
		}
	}()
	return func() { cancel(); <-done }, nil
}

func (s *Service) applyDelivery(ctx context.Context, delivery *jetstream.Delivery) error {
	if delivery == nil || delivery.Message == nil {
		if delivery != nil && delivery.DecodeError != nil {
			return permanentDeliveryError{delivery.DecodeError}
		}
		return permanentDeliveryError{errors.New("storage event delivery is empty")}
	}
	spaceID, datasetID, err := eventconsumer.ParseDatasetFieldsChangedSubject("", delivery.Subject)
	if err != nil {
		return permanentDeliveryError{err}
	}
	event := &pb.DatasetFieldsChanged{}
	if err := proto.Unmarshal(delivery.Message.GetPayload(), event); err != nil {
		return permanentDeliveryError{err}
	}
	if event.GetSpaceId() != spaceID || event.GetDatasetId() != datasetID {
		return permanentDeliveryError{errors.New("dataset event subject and payload mismatch")}
	}
	return s.applyDatasetEvent(ctx, spaceID, datasetID, event.GetRows())
}

func (s *Service) applyDatasetEvent(ctx context.Context, spaceID, datasetID string, rows []*pb.RowFieldUpsert) error {
	s.mu.RLock()
	ref := datasetRef{spaceID: spaceID, datasetID: datasetID}
	viewKeys := make(map[viewRef]struct{})
	var standalone []string
	for id := range s.byData[ref] {
		if viewKey, ok := s.indexView[id]; ok {
			viewKeys[viewKey] = struct{}{}
		} else {
			standalone = append(standalone, id)
		}
	}
	s.mu.RUnlock()
	for viewKey := range viewKeys {
		s.mu.RLock()
		runtime := s.views[viewKey]
		s.mu.RUnlock()
		if runtime == nil {
			continue
		}
		runtime.mu.Lock()
		if err := s.applyEventToIndex(ctx, runtime.active, datasetID, rows); err != nil {
			runtime.mu.Unlock()
			return err
		}
		if runtime.next != "" {
			if err := s.applyEventToIndex(ctx, runtime.next, datasetID, rows); err != nil {
				failedID := runtime.next
				runtime.next = ""
				runtime.status = "failed"
				runtime.mu.Unlock()
				s.removeFailedBuild(ctx, failedID)
				continue
			}
		}
		runtime.mu.Unlock()
	}
	for _, id := range standalone {
		if err := s.applyEventToIndex(ctx, id, datasetID, rows); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) applyEventToIndex(ctx context.Context, id, datasetID string, rows []*pb.RowFieldUpsert) error {
	if id == "" {
		return nil
	}
	engine, err := s.engineFor(id)
	if err != nil {
		return err
	}
	s.mu.RLock()
	schema := s.schemas[id]
	s.mu.RUnlock()
	writes := eventWrites(schema, datasetID, rows)
	if len(writes) == 0 {
		return nil
	}
	return engine.Apply(ctx, id, viewindex.ViewIndexApplyBatch{RowWrites: writes, ViewRevision: schema.ViewVersion, ViewSchemaHash: schema.SchemaHash, WriteMode: viewindex.LiveWrite})
}

func (s *Service) BackfillView(ctx context.Context, spaceID, viewID string, batchSize int) error {
	return s.BackfillViewWithReader(ctx, spaceID, viewID, batchSize, nil)
}

func (s *Service) BackfillViewWithReader(ctx context.Context, spaceID, viewID string, batchSize int, reader FieldReader) error {
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if runtime == nil {
		return errors.New("view runtime is not prepared")
	}
	if batchSize <= 0 || batchSize > 100 {
		batchSize = 100
	}
	for offset := 0; ; offset += batchSize {
		runtime.mu.Lock()
		activeID, nextID := runtime.active, runtime.next
		runtime.mu.Unlock()
		if activeID == "" || nextID == "" {
			return errors.New("view build is not active")
		}
		activeEngine, err := s.engineFor(activeID)
		if err != nil {
			return err
		}
		rows, err := activeEngine.Scan(ctx, activeID, offset, batchSize)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			runtime.mu.Lock()
			if runtime.next == nextID {
				runtime.status = "ready"
			}
			runtime.mu.Unlock()
			return nil
		}
		writes := make([]viewindex.RowWrite, 0, len(rows))
		for _, row := range rows {
			writes = append(writes, viewindex.RowWrite{Key: viewindex.RowKey{Key: row.GetKey()}, Fields: row.GetFields(), Attributes: row.GetAttributes()})
		}
		if reader != nil {
			if err := s.enrichBackfillRows(ctx, reader, activeID, nextID, writes); err != nil {
				return err
			}
		}
		runtime.mu.Lock()
		if runtime.next != nextID {
			runtime.mu.Unlock()
			return errors.New("view build changed during backfill")
		}
		nextEngine, err := s.engineFor(nextID)
		if err == nil {
			s.mu.RLock()
			schema := s.schemas[nextID]
			s.mu.RUnlock()
			err = nextEngine.Apply(ctx, nextID, viewindex.ViewIndexApplyBatch{RowWrites: writes, ViewRevision: schema.ViewVersion, ViewSchemaHash: schema.SchemaHash, WriteMode: viewindex.Backfill})
		}
		if err != nil {
			failedID := runtime.next
			runtime.next = ""
			runtime.status = "failed"
			runtime.mu.Unlock()
			s.removeFailedBuild(ctx, failedID)
			return err
		}
		runtime.mu.Unlock()
	}
}

func (s *Service) enrichBackfillRows(ctx context.Context, reader FieldReader, activeID, nextID string, writes []viewindex.RowWrite) error {
	s.mu.RLock()
	activeSchema := s.schemas[activeID]
	nextSchema := s.schemas[nextID]
	s.mu.RUnlock()
	activeColumns := make(map[string]struct{}, len(activeSchema.Columns))
	for _, column := range activeSchema.Columns {
		if column != nil {
			activeColumns[column.GetColumnName()] = struct{}{}
		}
	}
	type requestedField struct {
		source string
		target string
	}
	byDataset := make(map[string][]requestedField)
	for _, column := range nextSchema.Columns {
		if column == nil {
			continue
		}
		if _, exists := activeColumns[column.GetColumnName()]; exists {
			continue
		}
		datasetID := viewColumnDataset(column)
		_, source, ok := strings.Cut(column.GetOriginId(), ".")
		if datasetID != "" && ok && source != "" {
			byDataset[datasetID] = append(byDataset[datasetID], requestedField{source: source, target: column.GetColumnName()})
		}
	}
	for datasetID, fields := range byDataset {
		keys := make([]*pb.RowKey, 0, len(writes))
		positions := make(map[string]int, len(writes))
		for index, write := range writes {
			key := proto.Clone(write.Key.Key).(*pb.RowKey)
			key.DatasetId = datasetID
			keys = append(keys, key)
			positions[viewindex.RowKeyID(key)] = index
		}
		fieldIDs := make([]string, 0, len(fields))
		targets := make(map[string]string, len(fields))
		for _, field := range fields {
			fieldIDs = append(fieldIDs, field.source)
			targets[field.source] = field.target
		}
		s.mu.RLock()
		auth := s.primaryAuth
		if auth != nil {
			auth = proto.Clone(auth).(*pb.AuthInfo)
		}
		s.mu.RUnlock()
		if auth == nil {
			return errors.New("primary auth is not configured")
		}
		rsp, err := reader.ReadFields(ctx, &pb.PrimaryReadFieldsReq{AuthInfo: auth, Keys: keys, FieldIds: fieldIDs})
		if err != nil {
			return err
		}
		if err := requireSuccess(rsp.GetRetInfo()); err != nil {
			return err
		}
		for _, row := range rsp.GetRows() {
			position, ok := positions[viewindex.RowKeyID(row.GetKey())]
			if !ok {
				continue
			}
			for _, field := range row.GetFields() {
				if target := targets[field.GetFieldId()]; target != "" {
					writes[position].Fields = append(writes[position].Fields, &pb.FieldValue{FieldId: target, Value: field.GetValue()})
				}
			}
		}
	}
	return nil
}

func (s *Service) SwitchView(ctx context.Context, spaceID, viewID string, grace time.Duration) error {
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if runtime == nil {
		return errors.New("view runtime is not prepared")
	}
	runtime.mu.Lock()
	if runtime.next == "" || runtime.status != "ready" {
		runtime.mu.Unlock()
		return errors.New("no completed view build to switch")
	}
	oldID := runtime.active
	runtime.active = runtime.next
	runtime.next = ""
	runtime.status = "active"
	runtime.mu.Unlock()
	if grace < 0 {
		grace = 0
	}
	go func() {
		timer := time.NewTimer(grace)
		<-timer.C
		s.removeFailedBuild(trpc.BackgroundContext(), oldID)
	}()
	return ctx.Err()
}

func (s *Service) AttachActiveView(view *pb.View) error {
	if view == nil || view.GetSpaceId() == "" || view.GetViewId() == "" || view.GetActiveIndexId() == "" {
		return errors.New("active view metadata is required")
	}
	engineName := strings.ToLower(strings.TrimSpace(view.GetEngine()))
	if s.engines[engineName] == nil {
		return fmt.Errorf("view engine %q is unavailable", engineName)
	}
	columns := view.GetActiveColumns()
	if len(columns) == 0 {
		columns = view.GetColumns()
	}
	schema := viewindex.ViewIndexSchema{
		SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), ViewVersion: view.GetActiveViewRevision(),
		Engine: engineName, Columns: columns, SchemaHash: view.GetActiveViewSchemaHash(),
	}
	s.mu.Lock()
	s.indexEngine[view.GetActiveIndexId()] = engineName
	s.schemas[view.GetActiveIndexId()] = schema
	viewKey := viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}
	runtime := s.views[viewKey]
	if runtime == nil {
		runtime = &viewRuntime{}
		s.views[viewKey] = runtime
	}
	runtime.active = view.GetActiveIndexId()
	runtime.status = "active"
	s.indexView[view.GetActiveIndexId()] = viewKey
	for _, column := range columns {
		if datasetID := viewColumnDataset(column); datasetID != "" {
			ref := datasetRef{spaceID: view.GetSpaceId(), datasetID: datasetID}
			if s.byData[ref] == nil {
				s.byData[ref] = make(map[string]struct{})
			}
			s.byData[ref][view.GetActiveIndexId()] = struct{}{}
		}
	}
	s.mu.Unlock()
	return nil
}

func (s *Service) MarkViewBuildReady(spaceID, viewID string) error {
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if runtime == nil {
		return errors.New("view runtime is not prepared")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.next == "" {
		runtime.status = "ready"
		return nil
	}
	runtime.status = "ready"
	return nil
}

func (s *Service) SetPrimaryAuth(auth *pb.AuthInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if auth == nil {
		s.primaryAuth = nil
		return
	}
	s.primaryAuth = proto.Clone(auth).(*pb.AuthInfo)
}

func (s *Service) removeFailedBuild(ctx context.Context, id string) {
	engine, err := s.engineFor(id)
	if err == nil {
		_ = engine.Remove(ctx, id)
	}
	s.mu.Lock()
	s.removeIndexMappingsLocked(id)
	delete(s.indexEngine, id)
	delete(s.schemas, id)
	delete(s.indexView, id)
	s.mu.Unlock()
}

func (s *Service) engineFor(id string) (viewindex.QueryEngine, error) {
	s.mu.RLock()
	name := s.indexEngine[id]
	s.mu.RUnlock()
	engine := s.engines[name]
	if engine == nil {
		return nil, fmt.Errorf("view index %q is not prepared", id)
	}
	return engine, nil
}

func (s *Service) activeIndex(spaceID, viewID string) (string, *viewRuntime) {
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if runtime != nil {
		return runtime.active, runtime
	}
	return viewID, nil
}

func (s *Service) removeIndexMappingsLocked(id string) {
	for ref, ids := range s.byData {
		delete(ids, id)
		if len(ids) == 0 {
			delete(s.byData, ref)
		}
	}
}

func eventWrites(schema viewindex.ViewIndexSchema, datasetID string, rows []*pb.RowFieldUpsert) []viewindex.RowWrite {
	columns := make(map[string]string)
	for _, column := range schema.Columns {
		if column == nil || viewColumnDataset(column) != datasetID {
			continue
		}
		_, source, ok := strings.Cut(column.GetOriginId(), ".")
		if ok && source != "" {
			columns[source] = column.GetColumnName()
		}
	}
	if len(columns) == 0 {
		return nil
	}
	writes := make([]viewindex.RowWrite, 0, len(rows))
	for _, row := range rows {
		if row == nil || row.GetKey() == nil {
			continue
		}
		fields := make([]*pb.FieldValue, 0, len(row.GetFields()))
		for _, field := range row.GetFields() {
			if name := columns[field.GetFieldId()]; name != "" {
				fields = append(fields, &pb.FieldValue{FieldId: name, Value: field.GetValue()})
			}
		}
		if len(fields) != 0 {
			writes = append(writes, viewindex.RowWrite{Key: viewindex.RowKey{Key: row.GetKey()}, Fields: fields})
		}
	}
	return writes
}

func viewColumnDataset(column *pb.ViewColumn) string {
	if column == nil {
		return ""
	}
	origin := column.GetOriginId()
	if idx := strings.LastIndexByte(origin, '.'); idx > 0 {
		return origin[:idx]
	}
	return ""
}

func (s *Service) authorize(auth *pb.AuthInfo) error {
	if s == nil || strings.TrimSpace(s.authSecret) == "" {
		return errors.New("view auth is not configured")
	}
	if auth == nil || strings.TrimSpace(auth.GetAppId()) == "" || strings.TrimSpace(auth.GetAppKey()) == "" {
		return errors.New("view auth is required")
	}
	expected := datanode.ServiceAuthKey(s.authSecret, auth.GetAppId())
	if !hmac.Equal([]byte(strings.ToLower(auth.GetAppKey())), []byte(expected)) {
		return errors.New("invalid view HMAC")
	}
	return nil
}

type permanentDeliveryError struct{ error }

func isPermanentDeliveryError(err error) bool {
	var target permanentDeliveryError
	return errors.As(err, &target)
}
