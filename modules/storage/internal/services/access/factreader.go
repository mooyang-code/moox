package access

import (
	"context"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

type primaryFactReader struct {
	service *Service
}

func (s *Service) primaryFactReader() *primaryFactReader {
	return &primaryFactReader{service: s}
}

func (r *primaryFactReader) ReadRows(ctx context.Context, scope *pb.DataScope, mode pb.ReadMode, timeRange *pb.TimeRange, snapshotTime string, objectID string, columnNames []string, page *pb.Page) ([]*pb.DataRow, *pb.PageResult, error) {
	ref, err := r.service.router.Resolve(ctx, scope)
	if err != nil {
		return nil, nil, err
	}
	return r.service.primary.ReadRows(ctx, ref, &pb.ReadRowsReq{
		Scope:        scope,
		ReadMode:     mode,
		TimeRange:    timeRange,
		SnapshotTime: snapshotTime,
		ObjectId:     objectID,
		ColumnNames:  columnNames,
		Page:         page,
	})
}
