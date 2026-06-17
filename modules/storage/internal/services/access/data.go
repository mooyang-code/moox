package access

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/response"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/rs/xid"
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
	for _, group := range groups {
		if err := s.primary.WriteRows(ctx, group.target, group.rows, mode); err != nil {
			return &pb.WriteRowsRsp{RetInfo: response.Error(pb.ErrorCode_INNER_ERR, err)}, nil
		}
	}
	if err := s.publishRowsChanged(ctx, req.GetRows()); err != nil {
		s.reportDerivedError(ctx, "rows_changed_event", err)
	}
	if err := s.search.IndexRows(ctx, req.GetRows()); err != nil {
		s.reportDerivedError(ctx, "search_index", err)
	}
	return &pb.WriteRowsRsp{RetInfo: response.Success("success")}, nil
}

func (s *Service) ReadRows(ctx context.Context, req *pb.ReadRowsReq) (*pb.ReadRowsRsp, error) {
	ref, err := s.router.Resolve(ctx, req.GetScope())
	if err != nil {
		return &pb.ReadRowsRsp{RetInfo: response.Error(pb.ErrorCode_ROUTE_NOT_FOUND, err)}, nil
	}
	rows, page, err := s.primary.ReadRows(ctx, ref, req)
	if err != nil {
		return &pb.ReadRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ReadRowsRsp{RetInfo: response.Success("success"), Rows: rows, PageResult: page}, nil
}

type routedRows struct {
	target *pb.PrimaryTarget
	rows   []*pb.DataRow
}

func (s *Service) groupRowsByPrimaryTarget(ctx context.Context, rows []*pb.DataRow) ([]*routedRows, error) {
	groups := make(map[string]*routedRows)
	for _, row := range rows {
		ref, err := s.router.Resolve(ctx, row.GetKey().GetScope())
		if err != nil {
			return nil, err
		}
		key := ref.GetNodeId() + "|" + ref.GetEngine() + "|" + ref.GetDeviceTable()
		group := groups[key]
		if group == nil {
			group = &routedRows{target: ref}
			groups[key] = group
		}
		group.rows = append(group.rows, row)
	}
	out := make([]*routedRows, 0, len(groups))
	for _, group := range groups {
		out = append(out, group)
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
