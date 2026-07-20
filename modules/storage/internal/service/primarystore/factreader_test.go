//go:build legacy_storage

package primarystore

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/primarystore/shardrouter"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScanTimeSeriesRowsRPCRejectsMissingIDs(t *testing.T) {
	svc := &Service{}
	rsp, err := svc.ScanTimeSeriesRows(context.Background(), &pb.ScanTimeSeriesRowsReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestScanTimeSeriesRowsReturnsPagedRows(t *testing.T) {
	svc := &Service{
		primary: fakePrimaryScanner{rowsPerPage: 2, pages: 1},
		router:  router.NewResolver(fakeRouteReader{}),
	}
	rsp, err := svc.ScanTimeSeriesRows(context.Background(), &pb.ScanTimeSeriesRowsReq{
		SpaceId: "crypto", DatasetId: "kline", Page: &pb.Page{Size: 10},
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Len(t, rsp.GetRows(), 2)
}

func TestFactReaderDelegatesToService(t *testing.T) {
	svc := &Service{
		primary: fakePrimaryScanner{rowsPerPage: 1, pages: 1},
		router:  router.NewResolver(fakeRouteReader{}),
	}
	reader := svc.FactReader()
	rows, page, err := reader.ScanTimeSeriesRows(context.Background(), "crypto", "kline", nil, nil, &pb.Page{Size: 5})
	require.NoError(t, err)
	assert.Len(t, rows, 1)
	assert.NotNil(t, page)
}

func TestScanRecordRowsReturnsPagedRows(t *testing.T) {
	svc := &Service{
		primary: recordPrimaryScanner{rowsPerPage: 1},
		router:  router.NewResolver(fakeRouteReader{}),
	}
	rsp, err := svc.ScanRecordRows(context.Background(), &pb.ScanRecordRowsReq{
		SpaceId: "crypto", DatasetId: "news", Page: &pb.Page{Size: 5},
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Len(t, rsp.GetRows(), 1)
}

type recordPrimaryScanner struct {
	rowsPerPage int
}

func (r recordPrimaryScanner) WriteRows(context.Context, *pb.ShardTarget, []*pb.ShardRow) error {
	return nil
}

func (r recordPrimaryScanner) ReadRows(context.Context, *pb.ShardTarget, *pb.ReadRowsReq) ([]*pb.ShardRow, *pb.PageResult, error) {
	return nil, nil, nil
}

func (r recordPrimaryScanner) ScanRows(_ context.Context, _ *pb.ShardTarget, req *pb.ScanRowsReq) ([]*pb.ShardRow, *pb.PageResult, error) {
	rows := make([]*pb.ShardRow, r.rowsPerPage)
	for i := range rows {
		rows[i] = &pb.ShardRow{Key: &pb.ShardKey{
			SpaceId: "crypto", DatasetId: "news", DataKind: pb.DataKind_DATA_KIND_RECORD,
			Key: "news-1", Version: "2026-07-11T00:00:00Z",
		}}
	}
	return rows, &pb.PageResult{}, nil
}
