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

// NewAccessReader returns a remote Access reader when serviceName is configured,
// otherwise it uses the supplied local reader.
func NewAccessReader(local FactReader, serviceName string) FactReader {
	serviceName = strings.TrimSpace(serviceName)
	if serviceName != "" {
		return &remoteAccessReader{
			proxy: pb.NewAccessServiceClientProxy(client.WithServiceName(serviceName)),
		}
	}
	if local != nil {
		return local
	}
	return missingAccessReader{}
}

type remoteAccessReader struct {
	proxy pb.AccessServiceClientProxy
}

func (r *remoteAccessReader) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	return r.proxy.ReadTimeSeriesRows(ctx, req)
}

func (r *remoteAccessReader) ReadRecordRows(ctx context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	return r.proxy.ReadRecordRows(ctx, req)
}

type missingAccessReader struct{}

func (missingAccessReader) ReadTimeSeriesRows(context.Context, *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	return nil, errMissingAccessReader
}

func (missingAccessReader) ReadRecordRows(context.Context, *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	return nil, errMissingAccessReader
}

var errMissingAccessReader = errors.New("deriver access reader requires local reader or access service name")
