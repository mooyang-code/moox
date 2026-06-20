package access

import (
	"context"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/response"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/rs/xid"
	"google.golang.org/protobuf/proto"
)

func (s *Service) WriteRows(ctx context.Context, req *pb.WriteRowsReq) (*pb.WriteRowsRsp, error) {
	mode := req.GetWriteMode()
	if mode == pb.WriteMode_WRITE_MODE_UNSPECIFIED {
		mode = pb.WriteMode_WRITE_MODE_UPSERT
	}
	if err := s.validator.ValidateWriteRows(ctx, req.GetRows()); err != nil {
		return &pb.WriteRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	groups, err := s.groupRowsByPrimaryTarget(ctx, req.GetRows())
	if err != nil {
		return &pb.WriteRowsRsp{RetInfo: response.Error(pb.ErrorCode_ROUTE_NOT_FOUND, err)}, nil
	}
	var writtenRows []*pb.DataRow
	for _, group := range groups {
		if err := s.primary.WriteRows(ctx, group.target, group.rows, mode); err != nil {
			if publishErr := s.publishRowsChanged(ctx, writtenRows); publishErr != nil {
				s.reportDerivedError(ctx, "rows_changed_event", publishErr)
			}
			return &pb.WriteRowsRsp{RetInfo: response.Error(primaryErrorCode(err), err)}, nil
		}
		writtenRows = append(writtenRows, group.rows...)
	}
	// 写入主链路只对主存负责。派生（Search 索引等）通过事件总线异步消费，
	// 不再阻塞主写入返回。
	if err := s.publishRowsChanged(ctx, writtenRows); err != nil {
		s.reportDerivedError(ctx, "rows_changed_event", err)
	}
	return &pb.WriteRowsRsp{RetInfo: response.Success("success")}, nil
}

func (s *Service) WriteTimeSeriesRows(ctx context.Context, req *pb.WriteTimeSeriesRowsReq) (*pb.WriteTimeSeriesRowsRsp, error) {
	rows := make([]*pb.DataRow, 0, len(req.GetRows()))
	for _, row := range req.GetRows() {
		converted, err := timeSeriesRowToDataRow(row)
		if err != nil {
			return &pb.WriteTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
		rows = append(rows, converted)
	}
	rsp, err := s.WriteRows(ctx, &pb.WriteRowsReq{
		AuthInfo:  req.GetAuthInfo(),
		WriteMode: req.GetWriteMode(),
		Rows:      rows,
	})
	if err != nil {
		return nil, err
	}
	return &pb.WriteTimeSeriesRowsRsp{RetInfo: rsp.GetRetInfo()}, nil
}

func (s *Service) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	if err := validateTimeRange(req.GetTimeRange()); err != nil {
		return &pb.ReadTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	var out []*pb.TimeSeriesRow
	for _, key := range req.GetKeys() {
		if err := validateTimeSeriesKeyTemplate(key); err != nil {
			return &pb.ReadTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
		timeRange := req.GetTimeRange()
		if timeRange == nil && key.GetDataTime() != "" {
			timeRange = &pb.TimeRange{StartTime: key.GetDataTime(), EndTime: key.GetDataTime()}
		}
		rows, ret, err := s.readAllRows(ctx, &pb.ReadRowsReq{
			AuthInfo: req.GetAuthInfo(),
			Scope: &pb.DataScope{
				SpaceId:    key.GetSpaceId(),
				DatasetId:  key.GetDatasetId(),
				SubjectId:  key.GetSubjectId(),
				Freq:       key.GetFreq(),
				Dimensions: key.GetDimensions(),
			},
			ReadMode:    pb.ReadMode_READ_MODE_RANGE,
			TimeRange:   timeRange,
			ColumnNames: req.GetColumnNames(),
		})
		if err != nil {
			return nil, err
		}
		if ret.GetCode() != pb.ErrorCode_SUCCESS {
			return &pb.ReadTimeSeriesRowsRsp{RetInfo: ret}, nil
		}
		for _, row := range rows {
			out = append(out, dataRowToTimeSeriesRow(row))
		}
	}
	sortTimeSeriesRows(out)
	if req.GetOrder() == pb.SortOrder_SORT_ORDER_DESC {
		reverseTimeSeriesRows(out)
	}
	out, pageResult := pageTimeSeriesRows(out, req.GetPage())
	return &pb.ReadTimeSeriesRowsRsp{RetInfo: response.Success("success"), Rows: out, PageResult: pageResult}, nil
}

func (s *Service) WriteObjectRows(ctx context.Context, req *pb.WriteObjectRowsReq) (*pb.WriteObjectRowsRsp, error) {
	rows := make([]*pb.DataRow, 0, len(req.GetRows()))
	for _, row := range req.GetRows() {
		converted, err := objectRowToDataRow(row)
		if err != nil {
			return &pb.WriteObjectRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
		rows = append(rows, converted)
	}
	rsp, err := s.WriteRows(ctx, &pb.WriteRowsReq{
		AuthInfo:  req.GetAuthInfo(),
		WriteMode: req.GetWriteMode(),
		Rows:      rows,
	})
	if err != nil {
		return nil, err
	}
	return &pb.WriteObjectRowsRsp{RetInfo: rsp.GetRetInfo()}, nil
}

func (s *Service) ReadObjectRows(ctx context.Context, req *pb.ReadObjectRowsReq) (*pb.ReadObjectRowsRsp, error) {
	var out []*pb.ObjectRow
	for _, key := range req.GetKeys() {
		if err := validateObjectKeyTemplate(key); err != nil {
			return &pb.ReadObjectRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
		}
		rows, ret, err := s.readAllRows(ctx, &pb.ReadRowsReq{
			AuthInfo: req.GetAuthInfo(),
			Scope: &pb.DataScope{
				SpaceId:   key.GetSpaceId(),
				DatasetId: key.GetDatasetId(),
			},
			ReadMode:    pb.ReadMode_READ_MODE_RANGE,
			ObjectId:    key.GetObjectId(),
			ColumnNames: req.GetColumnNames(),
		})
		if err != nil {
			return nil, err
		}
		if ret.GetCode() != pb.ErrorCode_SUCCESS {
			return &pb.ReadObjectRowsRsp{RetInfo: ret}, nil
		}
		for _, row := range rows {
			if !dataRowMatchesObjectKey(row, key) {
				continue
			}
			converted := dataRowToObjectRow(row)
			if objectVersionMatches(converted.GetKey().GetVersion(), key.GetVersion(), req.GetVersionRange()) {
				out = append(out, converted)
			}
		}
	}
	sortObjectRows(out)
	if req.GetOrder() == pb.SortOrder_SORT_ORDER_DESC {
		reverseObjectRows(out)
	}
	out, pageResult := pageObjectRows(out, req.GetPage())
	return &pb.ReadObjectRowsRsp{RetInfo: response.Success("success"), Rows: out, PageResult: pageResult}, nil
}

func (s *Service) readAllRows(ctx context.Context, req *pb.ReadRowsReq) ([]*pb.DataRow, *pb.RetInfo, error) {
	const size = uint32(1000)
	var out []*pb.DataRow
	for pageNo := uint32(1); ; pageNo++ {
		next := proto.Clone(req).(*pb.ReadRowsReq)
		next.Page = &pb.Page{Page: pageNo, Size: size}
		rsp, err := s.ReadRows(ctx, next)
		if err != nil {
			return nil, nil, err
		}
		if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			return nil, rsp.GetRetInfo(), nil
		}
		out = append(out, rsp.GetRows()...)
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() {
			return out, rsp.GetRetInfo(), nil
		}
	}
}

// handleRowsChangedForSearch 是 Search 索引的派生消费者。订阅自事件总线，
// 把行变更异步同步到 Bleve 全文索引。Search 是派生结果，不提供写后立即可搜契约。
func (s *Service) handleRowsChangedForSearch(ctx context.Context, event *pb.DataRowsChangedEvent) error {
	if len(event.GetRows()) == 0 {
		return nil
	}
	copied := proto.Clone(event).(*pb.DataRowsChangedEvent)
	s.indexMu.Lock()
	if s.closing {
		s.indexMu.Unlock()
		return nil
	}
	s.indexWG.Add(1)
	s.indexJobs = append(s.indexJobs, indexJob{
		ctx:   context.WithoutCancel(ctx),
		event: copied,
	})
	s.indexCond.Signal()
	s.indexMu.Unlock()
	return nil
}

type indexJob struct {
	ctx   context.Context
	event *pb.DataRowsChangedEvent
}

func (s *Service) runSearchIndexWorker() {
	for {
		s.indexMu.Lock()
		for len(s.indexJobs) == 0 && !s.closing {
			s.indexCond.Wait()
		}
		if len(s.indexJobs) == 0 && s.closing {
			s.indexMu.Unlock()
			return
		}
		job := s.indexJobs[0]
		copy(s.indexJobs, s.indexJobs[1:])
		s.indexJobs[len(s.indexJobs)-1] = indexJob{}
		s.indexJobs = s.indexJobs[:len(s.indexJobs)-1]
		s.indexMu.Unlock()

		s.indexRowsFromAccess(job.ctx, job.event)
		s.indexWG.Done()
	}
}

func (s *Service) indexRowsFromAccess(ctx context.Context, event *pb.DataRowsChangedEvent) {
	rows, err := s.currentAccessRows(ctx, event.GetRows())
	if err != nil {
		s.reportDerivedError(ctx, "search_index", err)
		return
	}
	if err := s.search.IndexRows(ctx, rows); err != nil {
		s.reportDerivedError(ctx, "search_index", err)
	}
}

func (s *Service) currentAccessRows(ctx context.Context, rows []*pb.DataRow) ([]*pb.DataRow, error) {
	out := make([]*pb.DataRow, 0, len(rows))
	for _, row := range rows {
		current, err := s.currentAccessRow(ctx, row)
		if err != nil {
			return nil, err
		}
		if current != nil {
			out = append(out, current)
		}
	}
	return out, nil
}

func (s *Service) currentAccessRow(ctx context.Context, row *pb.DataRow) (*pb.DataRow, error) {
	key := row.GetKey()
	scope := proto.Clone(key.GetScope()).(*pb.DataScope)
	req := &pb.ReadRowsReq{Scope: scope, ReadMode: pb.ReadMode_READ_MODE_RANGE}
	if scope.GetFreq() == "" && key.GetRowId() != "" {
		req.ObjectId = key.GetRowId()
	}
	if key.GetDataTime() != "" {
		req.TimeRange = &pb.TimeRange{
			StartTime: key.GetDataTime(),
			EndTime:   key.GetDataTime(),
		}
	}
	reader := s.factReader
	if reader == nil {
		reader = s
	}
	rsp, err := reader.ReadRows(ctx, req)
	if err != nil {
		return nil, err
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return nil, errText(rsp.GetRetInfo().GetMsg())
	}
	for _, current := range rsp.GetRows() {
		if sameDataKey(current.GetKey(), key) {
			return current, nil
		}
	}
	return nil, nil
}

// WaitForIndex 等待所有异步索引任务完成，用于优雅关闭或测试同步点。
func (s *Service) WaitForIndex() {
	s.indexWG.Wait()
}

func (s *Service) ReadRows(ctx context.Context, req *pb.ReadRowsReq) (*pb.ReadRowsRsp, error) {
	ref, err := s.router.Resolve(ctx, req.GetScope())
	if err != nil {
		return &pb.ReadRowsRsp{RetInfo: response.Error(pb.ErrorCode_ROUTE_NOT_FOUND, err)}, nil
	}
	rows, page, err := s.primary.ReadRows(ctx, ref, req)
	if err != nil {
		return &pb.ReadRowsRsp{RetInfo: response.Error(primaryErrorCode(err), err)}, nil
	}
	return &pb.ReadRowsRsp{RetInfo: response.Success("success"), Rows: rows, PageResult: page}, nil
}

func primaryErrorCode(err error) pb.ErrorCode {
	if err == nil {
		return pb.ErrorCode_SUCCESS
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "engine_capability_unsupported") ||
		(strings.Contains(text, "unsupported") && strings.Contains(text, "engine")) {
		return pb.ErrorCode_ENGINE_CAPABILITY_UNSUPPORTED
	}
	if strings.Contains(text, "invalid_param") ||
		strings.Contains(text, " is required") ||
		strings.Contains(text, "invalid ") {
		return pb.ErrorCode_INVALID_PARAM
	}
	return pb.ErrorCode_INNER_ERR
}

type routedRows struct {
	target *pb.PrimaryTarget
	rows   []*pb.DataRow
}

func (s *Service) groupRowsByPrimaryTarget(ctx context.Context, rows []*pb.DataRow) ([]*routedRows, error) {
	groups := make(map[string]*routedRows)
	var order []string
	// 同一批写入中，相同路由维度（space/dataset/subject）的行共享解析结果，
	// 避免逐行重复 Resolve 带来的元数据查询与排序开销。
	resolved := make(map[string]*pb.PrimaryTarget)
	for _, row := range rows {
		scope := row.GetKey().GetScope()
		scopeKey := scope.GetSpaceId() + "|" + scope.GetDatasetId() + "|" + scope.GetSubjectId()
		ref, ok := resolved[scopeKey]
		if !ok {
			var err error
			ref, err = s.router.Resolve(ctx, scope)
			if err != nil {
				return nil, err
			}
			resolved[scopeKey] = ref
		}
		key := ref.GetNodeId() + "|" + ref.GetEngine() + "|" + ref.GetDeviceTable()
		group := groups[key]
		if group == nil {
			group = &routedRows{target: ref}
			groups[key] = group
			order = append(order, key)
		}
		group.rows = append(group.rows, row)
	}
	out := make([]*routedRows, 0, len(groups))
	for _, key := range order {
		out = append(out, groups[key])
	}
	return out, nil
}

func (s *Service) publishRowsChanged(ctx context.Context, rows []*pb.DataRow) error {
	if len(rows) == 0 || s.events == nil {
		return nil
	}
	events := make(map[string]*pb.DataRowsChangedEvent)
	for _, row := range rows {
		scope := row.GetKey().GetScope()
		key := scope.GetSpaceId() + "|" + scope.GetDatasetId() + "|" + scope.GetSubjectId() + "|" + scope.GetFreq()
		event := events[key]
		if event == nil {
			event = &pb.DataRowsChangedEvent{
				EventId:   xid.New().String(),
				Scope:     scope,
				EventTime: time.Now().Format(time.RFC3339Nano),
			}
			events[key] = event
		}
		event.Rows = append(event.Rows, row)
	}
	for _, event := range events {
		if err := s.events.PublishRowsChanged(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) reportDerivedError(ctx context.Context, stage string, err error) {
	if s == nil || s.report == nil || err == nil {
		return
	}
	s.report(ctx, stage, err)
}

func sameDataKey(left, right *pb.DataKey) bool {
	leftScope := left.GetScope()
	rightScope := right.GetScope()
	if left.GetDataTime() != right.GetDataTime() || left.GetRowId() != right.GetRowId() {
		return false
	}
	if leftScope.GetSpaceId() != rightScope.GetSpaceId() ||
		leftScope.GetDatasetId() != rightScope.GetDatasetId() ||
		leftScope.GetSubjectId() != rightScope.GetSubjectId() ||
		leftScope.GetFreq() != rightScope.GetFreq() {
		return false
	}
	if len(leftScope.GetDimensions()) != len(rightScope.GetDimensions()) {
		return false
	}
	for key, value := range leftScope.GetDimensions() {
		if rightScope.GetDimensions()[key] != value {
			return false
		}
	}
	return true
}
