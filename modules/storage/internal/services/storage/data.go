package storage

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/storage/pkg/quantstore"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/rs/xid"
)

func (s *Service) WriteRows(ctx context.Context, req *pb.WriteRowsReq) (*pb.WriteRowsRsp, error) {
	mode := req.GetWriteMode()
	if mode == pb.WriteMode_WRITE_MODE_UNSPECIFIED {
		mode = pb.WriteMode_WRITE_MODE_UPSERT
	}
	if err := s.validator.ValidateWriteRows(ctx, req.GetRows()); err != nil {
		return &pb.WriteRowsRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	groups, err := s.groupRowsByDevice(ctx, req.GetRows())
	if err != nil {
		return &pb.WriteRowsRsp{RetInfo: quantstore.Error(pb.ErrorCode_ROUTE_NOT_FOUND, err)}, nil
	}
	for _, group := range groups {
		if err := s.adapter.WriteRows(ctx, group.device, group.rows, mode); err != nil {
			return &pb.WriteRowsRsp{RetInfo: quantstore.Error(pb.ErrorCode_INNER_ERR, err)}, nil
		}
	}
	if err := s.publishRowsChanged(ctx, req.GetRows()); err != nil {
		return &pb.WriteRowsRsp{RetInfo: quantstore.Error(pb.ErrorCode_INNER_ERR, err)}, nil
	}
	return &pb.WriteRowsRsp{RetInfo: quantstore.Success("success")}, nil
}

func (s *Service) ReadRows(ctx context.Context, req *pb.ReadRowsReq) (*pb.ReadRowsRsp, error) {
	rows, page, err := s.store.ReadRows(
		ctx,
		req.GetScope(),
		req.GetReadMode(),
		req.GetTimeRange(),
		req.GetSnapshotTime(),
		req.GetRowIds(),
		req.GetColumnNames(),
		req.GetPage(),
	)
	if err != nil {
		return &pb.ReadRowsRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
	}
	return &pb.ReadRowsRsp{RetInfo: quantstore.Success("success"), Rows: rows, PageResult: page}, nil
}

type routedRows struct {
	device *pb.DeviceRef
	rows   []*pb.DataRow
}

func (s *Service) groupRowsByDevice(ctx context.Context, rows []*pb.DataRow) ([]*routedRows, error) {
	groups := make(map[string]*routedRows)
	for _, row := range rows {
		ref, err := s.router.Resolve(ctx, row.GetKey().GetScope())
		if err != nil {
			return nil, err
		}
		key := ref.GetNodeId() + "|" + ref.GetEngine() + "|" + ref.GetDeviceTable()
		group := groups[key]
		if group == nil {
			group = &routedRows{device: ref}
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
	if len(rows) == 0 || s.changes == nil {
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
		if err := s.changes.PublishRowsChanged(ctx, event); err != nil {
			return err
		}
	}
	return nil
}
