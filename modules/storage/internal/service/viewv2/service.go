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
	authSecret  string
	mu          sync.RWMutex
	byData      map[datasetRef]map[string]struct{}
}

type datasetRef struct{ spaceID, datasetID string }

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
	s.mu.Unlock()
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
	if req == nil || req.GetViewId() == "" || len(req.GetKeys()) == 0 {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("view_id and keys are required"))}, nil
	}
	if err := s.authorize(req.GetAuthInfo()); err != nil {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_NO_PERMISSION, err)}, nil
	}
	keys := make([]*pb.RowKey, 0, len(req.GetKeys()))
	for _, k := range req.GetKeys() {
		if k == nil {
			return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("time-series key is required"))}, nil
		}
		keys = append(keys, timeSeriesRowKey(k))
	}
	engine, err := s.engineFor(req.GetViewId())
	if err != nil {
		return &pb.QueryTimeSeriesRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	rows, err := engine.Query(ctx, req.GetViewId(), keys, req.GetColumnNames())
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
	if req == nil || req.GetViewId() == "" || len(req.GetKeys()) == 0 {
		return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INVALID_PARAM, errors.New("view_id and keys are required"))}, nil
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
	engine, err := s.engineFor(req.GetViewId())
	if err != nil {
		return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	var rows []*pb.RowFieldValues
	if searcher, ok := engine.(interface {
		Search(context.Context, string, string, []string, int) ([]*pb.RowFieldValues, error)
	}); ok && strings.TrimSpace(req.GetTextQuery()) != "" {
		rows, err = searcher.Search(ctx, req.GetViewId(), req.GetTextQuery(), req.GetColumnNames(), int(req.GetPage().GetSize()))
	} else {
		rows, err = engine.Query(ctx, req.GetViewId(), keys, req.GetColumnNames())
	}
	if err != nil {
		return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	out := make([]*pb.RecordRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, &pb.RecordRow{Key: rowToRecordKey(row.GetKey()), Fields: row.GetFields()})
	}
	return &pb.SearchRecordRowsRsp{RetInfo: retinfo.Success("success"), Rows: out, Complete: true}, nil
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
		return errors.New("storage event delivery is empty")
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
	s.mu.RLock()
	ref := datasetRef{spaceID: spaceID, datasetID: datasetID}
	ids := make([]string, 0, len(s.byData[ref]))
	for id := range s.byData[ref] {
		ids = append(ids, id)
	}
	s.mu.RUnlock()
	for _, id := range ids {
		engine, err := s.engineFor(id)
		if err != nil {
			return err
		}
		stat, err := engine.Stat(ctx, id)
		if err != nil {
			return err
		}
		s.mu.RLock()
		schema := s.schemas[id]
		s.mu.RUnlock()
		writes := eventWrites(schema, datasetID, event.GetRows())
		if len(writes) == 0 {
			continue
		}
		if err := engine.Apply(ctx, id, viewindex.ViewIndexApplyBatch{RowWrites: writes, ViewRevision: stat.ViewVersion, ViewSchemaHash: stat.SchemaHash, WriteMode: viewindex.LiveWrite}); err != nil {
			return err
		}
	}
	return nil
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
