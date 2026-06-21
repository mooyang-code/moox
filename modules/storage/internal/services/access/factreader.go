package access

import (
	"context"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// primaryFactReader 通过 PrimaryStore 客户端回读主存事实行。
type primaryFactReader struct {
	service *Service
}

func (s *Service) primaryFactReader() *primaryFactReader {
	return &primaryFactReader{service: s}
}

func (r *primaryFactReader) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	return r.service.ReadTimeSeriesRows(ctx, req)
}

func (r *primaryFactReader) ScanTimeSeriesRows(ctx context.Context, spaceID string, datasetID string, timeRange *pb.TimeRange, columnNames []string, page *pb.Page) ([]*pb.TimeSeriesRow, *pb.PageResult, error) {
	versionRange, err := timeRangeToVersionRange(timeRange)
	if err != nil {
		return nil, nil, err
	}
	target, err := r.service.router.Resolve(ctx, spaceID, datasetID, "")
	if err != nil {
		return nil, nil, err
	}
	rows, pageResult, err := r.service.primary.ScanRows(ctx, target, &pb.ScanPrimaryRowsReq{
		Target:       target,
		DataKind:     pb.DataKind_DATA_KIND_TIME_SERIES,
		VersionRange: versionRange,
		ColumnNames:  columnNames,
		Page:         page,
	})
	if err != nil {
		return nil, nil, err
	}
	out := make([]*pb.TimeSeriesRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, primaryStoreRowToTimeSeriesRow(row, nil))
	}
	return out, pageResult, nil
}
