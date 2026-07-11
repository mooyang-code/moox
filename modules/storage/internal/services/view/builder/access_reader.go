package builder

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
	ScanRecordRows(ctx context.Context, spaceID string, datasetID string, columnNames []string, page *pb.Page) ([]*pb.RecordRow, *pb.PageResult, error)
}

// RecordReplayReader exposes the stable Record snapshot and durable journal
// path used by ViewBuilder backfills and reconciliation.
type RecordReplayReader interface {
	OpenRecordSnapshot(ctx context.Context, req *pb.OpenRecordAccessSnapshotReq) (*pb.OpenRecordAccessSnapshotRsp, error)
	ReadRecordSnapshot(ctx context.Context, req *pb.ReadRecordAccessSnapshotReq) (*pb.ReadRecordAccessSnapshotRsp, error)
	ScanRecordSnapshot(ctx context.Context, req *pb.ScanRecordAccessSnapshotReq) (*pb.ScanRecordAccessSnapshotRsp, error)
	RenewRecordSnapshot(ctx context.Context, snapshotID string) error
	CloseRecordSnapshot(ctx context.Context, snapshotID string) error
	RecordWatermark(ctx context.Context, scope *pb.RecordAccessScope) (sourceID string, commitSeq uint64, err error)
	ScanRecordJournal(ctx context.Context, scope *pb.RecordAccessScope, after, through uint64, page *pb.Page) ([]*pb.RecordRowsCommittedEvent, uint64, *pb.PageResult, error)
}

// NewAccessReader returns a remote Access reader when serviceName is configured,
// otherwise it uses the supplied local reader.
func NewAccessReader(local AccessReader, serviceName string, scanServiceName string) AccessReader {
	serviceName = strings.TrimSpace(serviceName)
	if serviceName != "" {
		scanServiceName = strings.TrimSpace(scanServiceName)
		if scanServiceName == "" {
			scanServiceName = "trpc.moox.storage.AccessScan"
		}
		return &remoteAccessReader{
			proxy:     pb.NewAccessClientProxy(client.WithServiceName(serviceName)),
			scanProxy: pb.NewAccessScanClientProxy(client.WithServiceName(scanServiceName)),
		}
	}
	if local != nil {
		return local
	}
	return missingAccessReader{}
}

type remoteAccessReader struct {
	proxy     pb.AccessClientProxy
	scanProxy pb.AccessScanClientProxy
}

func (r *remoteAccessReader) OpenRecordSnapshot(ctx context.Context, req *pb.OpenRecordAccessSnapshotReq) (*pb.OpenRecordAccessSnapshotRsp, error) {
	return r.scanProxy.OpenRecordAccessSnapshot(ctx, req)
}
func (r *remoteAccessReader) ReadRecordSnapshot(ctx context.Context, req *pb.ReadRecordAccessSnapshotReq) (*pb.ReadRecordAccessSnapshotRsp, error) {
	return r.scanProxy.ReadRecordAccessSnapshot(ctx, req)
}
func (r *remoteAccessReader) ScanRecordSnapshot(ctx context.Context, req *pb.ScanRecordAccessSnapshotReq) (*pb.ScanRecordAccessSnapshotRsp, error) {
	return r.scanProxy.ScanRecordAccessSnapshot(ctx, req)
}
func (r *remoteAccessReader) RenewRecordSnapshot(ctx context.Context, snapshotID string) error {
	rsp, err := r.scanProxy.RenewRecordAccessSnapshot(ctx, &pb.RenewRecordAccessSnapshotReq{SnapshotId: snapshotID})
	if err != nil {
		return err
	}
	return retInfoError(rsp.GetRetInfo())
}
func (r *remoteAccessReader) CloseRecordSnapshot(ctx context.Context, snapshotID string) error {
	rsp, err := r.scanProxy.CloseRecordAccessSnapshot(ctx, &pb.CloseRecordAccessSnapshotReq{SnapshotId: snapshotID})
	if err != nil {
		return err
	}
	return retInfoError(rsp.GetRetInfo())
}
func (r *remoteAccessReader) RecordWatermark(ctx context.Context, scope *pb.RecordAccessScope) (string, uint64, error) {
	rsp, err := r.scanProxy.RecordAccessWatermark(ctx, &pb.RecordAccessWatermarkReq{Scope: scope})
	if err != nil {
		return "", 0, err
	}
	if err := retInfoError(rsp.GetRetInfo()); err != nil {
		return "", 0, err
	}
	return rsp.GetSourceId(), rsp.GetCommitSeq(), nil
}
func (r *remoteAccessReader) ScanRecordJournal(ctx context.Context, scope *pb.RecordAccessScope, after, through uint64, page *pb.Page) ([]*pb.RecordRowsCommittedEvent, uint64, *pb.PageResult, error) {
	rsp, err := r.scanProxy.ScanRecordAccessJournal(ctx, &pb.ScanRecordAccessJournalReq{Scope: scope, AfterCommitSeq: after, ThroughCommitSeq: through, Page: page})
	if err != nil {
		return nil, 0, nil, err
	}
	if err := retInfoError(rsp.GetRetInfo()); err != nil {
		return nil, 0, nil, err
	}
	return rsp.GetEvents(), rsp.GetScannedThroughCommitSeq(), rsp.GetPageResult(), nil
}

func (r *remoteAccessReader) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	return r.proxy.ReadTimeSeriesRows(ctx, req)
}

func (r *remoteAccessReader) ReadRecordRows(ctx context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	return r.proxy.ReadRecordRows(ctx, req)
}

func (r *remoteAccessReader) ScanTimeSeriesRows(ctx context.Context, spaceID string, datasetID string, timeRange *pb.TimeRange, columnNames []string, page *pb.Page) ([]*pb.TimeSeriesRow, *pb.PageResult, error) {
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
		return nil, nil, errors.New("scan time-series rows returned nil response")
	}
	if err := retInfoError(rsp.GetRetInfo()); err != nil {
		return nil, nil, err
	}
	return rsp.GetRows(), rsp.GetPageResult(), nil
}

func (r *remoteAccessReader) ScanRecordRows(ctx context.Context, spaceID string, datasetID string, columnNames []string, page *pb.Page) ([]*pb.RecordRow, *pb.PageResult, error) {
	rsp, err := r.scanProxy.ScanRecordRows(ctx, &pb.ScanRecordRowsReq{
		SpaceId:      spaceID,
		DatasetId:    datasetID,
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

func (missingAccessReader) ScanRecordRows(context.Context, string, string, []string, *pb.Page) ([]*pb.RecordRow, *pb.PageResult, error) {
	return nil, nil, errMissingAccessReader
}

func (missingAccessReader) OpenRecordSnapshot(context.Context, *pb.OpenRecordAccessSnapshotReq) (*pb.OpenRecordAccessSnapshotRsp, error) {
	return nil, errMissingAccessReader
}
func (missingAccessReader) ReadRecordSnapshot(context.Context, *pb.ReadRecordAccessSnapshotReq) (*pb.ReadRecordAccessSnapshotRsp, error) {
	return nil, errMissingAccessReader
}
func (missingAccessReader) ScanRecordSnapshot(context.Context, *pb.ScanRecordAccessSnapshotReq) (*pb.ScanRecordAccessSnapshotRsp, error) {
	return nil, errMissingAccessReader
}
func (missingAccessReader) RenewRecordSnapshot(context.Context, string) error {
	return errMissingAccessReader
}
func (missingAccessReader) CloseRecordSnapshot(context.Context, string) error {
	return errMissingAccessReader
}
func (missingAccessReader) RecordWatermark(context.Context, *pb.RecordAccessScope) (string, uint64, error) {
	return "", 0, errMissingAccessReader
}
func (missingAccessReader) ScanRecordJournal(context.Context, *pb.RecordAccessScope, uint64, uint64, *pb.Page) ([]*pb.RecordRowsCommittedEvent, uint64, *pb.PageResult, error) {
	return nil, 0, nil, errMissingAccessReader
}

var errMissingAccessReader = errors.New("view builder access reader requires local reader or access service name")
