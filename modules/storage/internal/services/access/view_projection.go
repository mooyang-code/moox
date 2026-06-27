package access

import (
	"context"

	"github.com/mooyang-code/moox/modules/storage/internal/services/deriver"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

func (s *Service) timeSeriesRowsForView(ctx context.Context, item *pb.View, columns []*pb.ViewColumn, rows []*pb.TimeSeriesRow) ([]*pb.TimeSeriesRow, bool, error) {
	return deriver.TimeSeriesRowsForView(ctx, item, columns, rows, s.readTimeSeriesProjectionRow)
}

func (s *Service) readTimeSeriesProjectionRow(ctx context.Context, base *pb.TimeSeriesKey, datasetID string) (*pb.TimeSeriesRow, error) {
	key := proto.Clone(base).(*pb.TimeSeriesKey)
	key.DatasetId = datasetID
	rsp, err := s.timeSeriesFactReaderOrDefault().ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{Keys: []*pb.TimeSeriesKey{key}})
	if err != nil {
		return nil, err
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return nil, errText(rsp.GetRetInfo().GetMsg())
	}
	if len(rsp.GetRows()) == 0 {
		return nil, nil
	}
	return rsp.GetRows()[0], nil
}
