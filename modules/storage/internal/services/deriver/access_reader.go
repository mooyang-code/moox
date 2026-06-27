package deriver

import (
	"context"
	"errors"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/client"
)

// FactReader reads fact rows from Access.
type FactReader interface {
	ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error)
	ReadRecordRows(ctx context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error)
}

// AccessReader reads fact rows from Access, including dataset scans used by view rebuilds.
type AccessReader interface {
	FactReader
	ScanTimeSeriesRows(ctx context.Context, spaceID string, datasetID string, timeRange *pb.TimeRange, columnNames []string, page *pb.Page) ([]*pb.TimeSeriesRow, *pb.PageResult, error)
	ScanRecordRows(ctx context.Context, spaceID string, datasetID string, versionRange *pb.VersionRange, columnNames []string, page *pb.Page) ([]*pb.RecordRow, *pb.PageResult, error)
}

// NewAccessReader returns a remote Access reader when serviceName is configured,
// otherwise it uses the supplied local reader.
func NewAccessReader(local AccessReader, serviceName string) AccessReader {
	serviceName = strings.TrimSpace(serviceName)
	if serviceName != "" {
		return &remoteAccessReader{
			proxy: pb.NewAccessClientProxy(client.WithServiceName(serviceName)),
		}
	}
	if local != nil {
		return local
	}
	return missingAccessReader{}
}

type remoteAccessReader struct {
	proxy pb.AccessClientProxy
}

func (r *remoteAccessReader) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	return r.proxy.ReadTimeSeriesRows(ctx, req)
}

func (r *remoteAccessReader) ReadRecordRows(ctx context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	return r.proxy.ReadRecordRows(ctx, req)
}

func (r *remoteAccessReader) ScanTimeSeriesRows(ctx context.Context, spaceID string, datasetID string, timeRange *pb.TimeRange, columnNames []string, page *pb.Page) ([]*pb.TimeSeriesRow, *pb.PageResult, error) {
	rsp, err := r.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{
		Keys:        []*pb.TimeSeriesKey{{SpaceId: spaceID, DatasetId: datasetID}},
		TimeRange:   timeRange,
		ColumnNames: columnNames,
		Page:        page,
	})
	if err != nil {
		return nil, nil, err
	}
	if rsp == nil {
		return nil, nil, errors.New("scan time-series rows returned nil response")
	}
	if err := retInfoError(rsp.GetRetInfo()); err != nil {
		return nil, nil, err
	}
	return rsp.GetRows(), rsp.GetPageResult(), nil
}

func (r *remoteAccessReader) ScanRecordRows(ctx context.Context, spaceID string, datasetID string, versionRange *pb.VersionRange, columnNames []string, page *pb.Page) ([]*pb.RecordRow, *pb.PageResult, error) {
	rsp, err := r.ReadRecordRows(ctx, &pb.ReadRecordRowsReq{
		Keys:         []*pb.RecordKey{{SpaceId: spaceID, DatasetId: datasetID}},
		VersionRange: versionRange,
		ColumnNames:  columnNames,
		Page:         page,
	})
	if err != nil {
		return nil, nil, err
	}
	if rsp == nil {
		return nil, nil, errors.New("scan record rows returned nil response")
	}
	if err := retInfoError(rsp.GetRetInfo()); err != nil {
		return nil, nil, err
	}
	return rsp.GetRows(), rsp.GetPageResult(), nil
}

type missingAccessReader struct{}

func (missingAccessReader) ReadTimeSeriesRows(context.Context, *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	return nil, errMissingAccessReader
}

func (missingAccessReader) ReadRecordRows(context.Context, *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	return nil, errMissingAccessReader
}

func (missingAccessReader) ScanTimeSeriesRows(context.Context, string, string, *pb.TimeRange, []string, *pb.Page) ([]*pb.TimeSeriesRow, *pb.PageResult, error) {
	return nil, nil, errMissingAccessReader
}

func (missingAccessReader) ScanRecordRows(context.Context, string, string, *pb.VersionRange, []string, *pb.Page) ([]*pb.RecordRow, *pb.PageResult, error) {
	return nil, nil, errMissingAccessReader
}

var errMissingAccessReader = errors.New("deriver access reader requires local reader or access service name")
