package builder

import (
	"context"
	"errors"
	"os"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"trpc.group/trpc-go/trpc-go/client"
)

// FactReader reads fact rows from PrimaryStore.
type FactReader interface {
	ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error)
	ReadRecordRows(ctx context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error)
}

// PrimaryStoreReader reads fact rows from PrimaryStore, including dataset scans used by view rebuilds.
type PrimaryStoreReader interface {
	FactReader
	ScanTimeSeriesRows(ctx context.Context, spaceID string, datasetID string, timeRange *pb.TimeRange, columnNames []string, page *pb.Page) ([]*pb.TimeSeriesRow, *pb.PageResult, error)
	ScanRecordRows(ctx context.Context, spaceID string, datasetID string, versionRange *pb.VersionRange, columnNames []string, page *pb.Page) ([]*pb.RecordRow, *pb.PageResult, error)
}

// NewPrimaryStoreReader returns a remote PrimaryStore reader when serviceName is configured,
// otherwise it uses the supplied local reader.
func NewPrimaryStoreReader(local PrimaryStoreReader, serviceName string, scanServiceName string) PrimaryStoreReader {
	return NewPrimaryStoreReaderWithGateway(local, serviceName, scanServiceName, "ip://127.0.0.1:11003", os.Getenv("MOOX_GATEWAY_TARGET_NODE"), gatewayauth.CredentialsFromEnv())
}

func NewPrimaryStoreReaderWithGateway(local PrimaryStoreReader, serviceName string, scanServiceName string, gatewayTarget string, gatewayNodeID string, credentials gatewayauth.Credentials) PrimaryStoreReader {
	serviceName = strings.TrimSpace(serviceName)
	if serviceName != "" {
		scanServiceName = strings.TrimSpace(scanServiceName)
		if scanServiceName == "" {
			scanServiceName = "trpc.moox.storage.PrimaryStoreScan"
		}
		gatewayTarget = gatewayauth.ServiceGatewayTarget(gatewayTarget)
		gatewayOptions := gatewayauth.NewTRPCClientOptions(gatewayTarget, strings.TrimSpace(gatewayNodeID), credentials)
		primaryOptions := append(append([]client.Option(nil), gatewayOptions...), client.WithServiceName(serviceName))
		scanOptions := append(append([]client.Option(nil), gatewayOptions...), client.WithServiceName(scanServiceName))
		return &remotePrimaryStoreReader{
			proxy:     pb.NewPrimaryStoreClientProxy(primaryOptions...),
			scanProxy: pb.NewPrimaryStoreScanClientProxy(scanOptions...),
		}
	}
	if local != nil {
		return local
	}
	return missingPrimaryStoreReader{}
}

type remotePrimaryStoreReader struct {
	proxy     pb.PrimaryStoreClientProxy
	scanProxy pb.PrimaryStoreScanClientProxy
}

func (r *remotePrimaryStoreReader) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	return r.proxy.ReadTimeSeriesRows(ctx, req)
}

func (r *remotePrimaryStoreReader) ReadRecordRows(ctx context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	return r.proxy.ReadRecordRows(ctx, req)
}

func (r *remotePrimaryStoreReader) ScanTimeSeriesRows(ctx context.Context, spaceID string, datasetID string, timeRange *pb.TimeRange, columnNames []string, page *pb.Page) ([]*pb.TimeSeriesRow, *pb.PageResult, error) {
	rsp, err := r.scanProxy.ScanTimeSeriesRows(ctx, &pb.ScanTimeSeriesRowsReq{
		SpaceId:     spaceID,
		DatasetId:   datasetID,
		TimeRange:   timeRange,
		ColumnNames: columnNames,
		Page:        page,
	})
	if err != nil {
		return nil, nil, err
	}
	if rsp == nil {
		return nil, nil, errors.New("scan time-series rows returned nil retinfo")
	}
	if err := retInfoError(rsp.GetRetInfo()); err != nil {
		return nil, nil, err
	}
	return rsp.GetRows(), rsp.GetPageResult(), nil
}

func (r *remotePrimaryStoreReader) ScanRecordRows(ctx context.Context, spaceID string, datasetID string, versionRange *pb.VersionRange, columnNames []string, page *pb.Page) ([]*pb.RecordRow, *pb.PageResult, error) {
	rsp, err := r.scanProxy.ScanRecordRows(ctx, &pb.ScanRecordRowsReq{
		SpaceId:      spaceID,
		DatasetId:    datasetID,
		VersionRange: versionRange,
		ColumnNames:  columnNames,
		Page:         page,
	})
	if err != nil {
		return nil, nil, err
	}
	if rsp == nil {
		return nil, nil, errors.New("scan record rows returned nil retinfo")
	}
	if err := retInfoError(rsp.GetRetInfo()); err != nil {
		return nil, nil, err
	}
	return rsp.GetRows(), rsp.GetPageResult(), nil
}

type missingPrimaryStoreReader struct{}

func (missingPrimaryStoreReader) ReadTimeSeriesRows(context.Context, *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	return nil, errMissingPrimaryStoreReader
}

func (missingPrimaryStoreReader) ReadRecordRows(context.Context, *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	return nil, errMissingPrimaryStoreReader
}

func (missingPrimaryStoreReader) ScanTimeSeriesRows(context.Context, string, string, *pb.TimeRange, []string, *pb.Page) ([]*pb.TimeSeriesRow, *pb.PageResult, error) {
	return nil, nil, errMissingPrimaryStoreReader
}

func (missingPrimaryStoreReader) ScanRecordRows(context.Context, string, string, *pb.VersionRange, []string, *pb.Page) ([]*pb.RecordRow, *pb.PageResult, error) {
	return nil, nil, errMissingPrimaryStoreReader
}

var errMissingPrimaryStoreReader = errors.New("view builder access reader requires local reader or access service name")
