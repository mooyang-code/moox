package view

import (
	"context"
	"errors"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQueryTimeSeriesRowsRejectsMissingViewID(t *testing.T) {
	svc := NewService(ServiceOptions{})
	rsp, err := svc.QueryTimeSeriesRows(context.Background(), &pb.QueryTimeSeriesRowsReq{SpaceId: "crypto"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_VIEW_NOT_FOUND, rsp.GetRetInfo().GetCode())
}

func TestQueryTimeSeriesRowsRejectsWrongEngine(t *testing.T) {
	svc := NewService(ServiceOptions{
		Metadata: &activeSchemaMetadata{
			view: &pb.View{SpaceId: "crypto", ViewId: "spot_view", Engine: "bleve", PrimaryDatasetId: "ds1", ActiveIndexId: "idx-1"},
			datasetKind: pb.DataKind_DATA_KIND_TIME_SERIES,
		},
	})
	rsp, err := svc.QueryTimeSeriesRows(context.Background(), &pb.QueryTimeSeriesRowsReq{
		SpaceId: "crypto", ViewId: "spot_view", Limit: 1, TotalMode: pb.TotalMode_NONE,
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestSearchRecordRowsRejectsMissingPrimaryDataset(t *testing.T) {
	svc := NewService(ServiceOptions{
		Metadata: &activeSchemaMetadata{
			view: &pb.View{SpaceId: "crypto", ViewId: "spot_view", Engine: "bleve", ActiveIndexId: "idx-1"},
			datasetKind: pb.DataKind_DATA_KIND_RECORD,
		},
		RecordIndexes: &activeSchemaRecordQuery{},
	})
	rsp, err := svc.SearchRecordRows(context.Background(), &pb.SearchRecordRowsReq{
		SpaceId: "crypto", ViewId: "spot_view",
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestValidateRequestedColumnsAgainstActiveSchemaAllowsActiveFields(t *testing.T) {
	view := &pb.View{ViewVersion: 2, ActiveViewVersion: 1}
	err := ValidateRequestedColumnsAgainstActiveSchema(view, []string{"close"}, []string{"close"}, []*pb.ViewColumn{{ColumnName: "volume"}})
	assert.NoError(t, err)
}

func TestNeedsActiveSchemaValidationRequiresVersionGap(t *testing.T) {
	assert.False(t, NeedsActiveSchemaValidation(&pb.View{ViewVersion: 1, ActiveViewVersion: 1}, []string{"close"}))
	assert.True(t, NeedsActiveSchemaValidation(&pb.View{ViewVersion: 2, ActiveViewVersion: 1}, []string{"close"}))
}

func TestIsViewNotReadyErrorDetectsMarker(t *testing.T) {
	assert.True(t, IsViewNotReadyError(errViewNotReady))
	assert.False(t, IsViewNotReadyError(errors.New("other")))
}

func TestViewColumnNamesSkipsBlankNames(t *testing.T) {
	names := ViewColumnNames([]*pb.ViewColumn{{ColumnName: "close"}, {ColumnName: " "}})
	assert.Equal(t, []string{"close"}, names)
}

func TestServiceCloseReturnsNil(t *testing.T) {
	assert.NoError(t, NewService(ServiceOptions{}).Close())
}
