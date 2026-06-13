package logic

import (
	"context"
	"errors"
	"testing"

	metadataDAO "github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao"
	"github.com/mooyang-code/moox/modules/storage/internal/services/metadata/dao/model"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	mocker "github.com/tencent/goom"
)

func TestCreateDataSet(t *testing.T) {
	tests := []struct {
		name          string
		req           *pb.CreateDataSetReq
		mockDAO       func(t *testing.T, dao *metadataDAO.DataInterfacer) **model.Dataset
		wantCode      pb.EnumErrorCode
		wantDatasetID uint32
	}{
		{
			name: "success",
			req: &pb.CreateDataSetReq{
				ProjId:      12,
				DatasetName: "kline",
				DataType:    pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE,
				Freqs:       "1m+1H",
				CheckRules:  "not_empty",
				Comment:     "for test",
			},
			mockDAO: func(t *testing.T, dao *metadataDAO.DataInterfacer) **model.Dataset {
				var addedDataset *model.Dataset
				mock := mocker.Create()
				t.Cleanup(func() {
					mock.Reset()
				})
				mock.Interface(dao).Method("GetProjectByID").Apply(
					func(ctx *mocker.IContext, projID int) (*model.Project, error) {
						if projID != 12 {
							t.Fatalf("GetProjectByID projID = %d, want 12", projID)
						}
						return &model.Project{ProjID: projID, ProjName: "proj"}, nil
					},
				)
				mock.Interface(dao).Method("GetMaxDatasetIDInRange").Apply(
					func(ctx *mocker.IContext, minID, maxID int) (int, error) {
						if minID != 1200 || maxID != 1299 {
							t.Fatalf("GetMaxDatasetIDInRange = (%d, %d), want (1200, 1299)", minID, maxID)
						}
						return 1205, nil
					},
				)
				mock.Interface(dao).Method("AddDataset").Apply(
					func(ctx *mocker.IContext, dataset *model.Dataset) error {
						addedDataset = dataset
						return nil
					},
				)
				return &addedDataset
			},
			wantCode:      pb.EnumErrorCode_SUCCESS,
			wantDatasetID: 1206,
		},
		{
			name: "invalid request",
			req: &pb.CreateDataSetReq{
				ProjId:      12,
				DatasetName: "",
				DataType:    pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE,
			},
			wantCode: pb.EnumErrorCode_INVALID_PARAM,
		},
		{
			name: "project not found",
			req: &pb.CreateDataSetReq{
				ProjId:      12,
				DatasetName: "kline",
				DataType:    pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE,
			},
			mockDAO: func(t *testing.T, dao *metadataDAO.DataInterfacer) **model.Dataset {
				mock := mocker.Create()
				t.Cleanup(func() {
					mock.Reset()
				})
				mock.Interface(dao).Method("GetProjectByID").Apply(
					func(ctx *mocker.IContext, projID int) (*model.Project, error) {
						return nil, nil
					},
				)
				return nil
			},
			wantCode: pb.EnumErrorCode_INVALID_PARAM,
		},
		{
			name: "get max dataset id failed",
			req: &pb.CreateDataSetReq{
				ProjId:      12,
				DatasetName: "kline",
				DataType:    pb.EnumDataTypeCategory_TIME_SERIES_DATA_TYPE,
			},
			mockDAO: func(t *testing.T, dao *metadataDAO.DataInterfacer) **model.Dataset {
				mock := mocker.Create()
				t.Cleanup(func() {
					mock.Reset()
				})
				mock.Interface(dao).Method("GetProjectByID").Apply(
					func(ctx *mocker.IContext, projID int) (*model.Project, error) {
						return &model.Project{ProjID: projID}, nil
					},
				)
				mock.Interface(dao).Method("GetMaxDatasetIDInRange").Apply(
					func(ctx *mocker.IContext, minID, maxID int) (int, error) {
						return 0, errors.New("db failed")
					},
				)
				return nil
			},
			wantCode: pb.EnumErrorCode_FAILED_UPDATE,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			var dbDAO metadataDAO.DataInterfacer
			var addedDataset **model.Dataset
			if tt.mockDAO != nil {
				addedDataset = tt.mockDAO(t, &dbDAO)
			}
			servicer := &MetaServicerImpl{dbDAO: dbDAO}

			rsp, err := servicer.CreateDataSet(context.Background(), tt.req)
			if err != nil {
				t.Fatalf("CreateDataSet returned error: %v", err)
			}
			if rsp.GetRetInfo().GetCode() != tt.wantCode {
				t.Fatalf("CreateDataSet code = %v, want %v, msg=%s", rsp.GetRetInfo().GetCode(), tt.wantCode, rsp.GetRetInfo().GetMsg())
			}
			if rsp.GetDatasetId() != tt.wantDatasetID {
				t.Fatalf("CreateDataSet datasetID = %d, want %d", rsp.GetDatasetId(), tt.wantDatasetID)
			}
			if tt.wantCode == pb.EnumErrorCode_SUCCESS {
				if addedDataset == nil || *addedDataset == nil {
					t.Fatalf("AddDataset was not called")
				}
				if (*addedDataset).DatasetID != int(tt.wantDatasetID) {
					t.Fatalf("added datasetID = %d, want %d", (*addedDataset).DatasetID, tt.wantDatasetID)
				}
				if (*addedDataset).ObjectTableID == "" || (*addedDataset).DataTableID == "" {
					t.Fatalf("table IDs should not be empty: %+v", *addedDataset)
				}
			}
		})
	}
}

func TestUpdateDataSet(t *testing.T) {
	datasetName := "kline_updated"
	freqs := "5m+1H"
	comment := "updated"

	var dbDAO metadataDAO.DataInterfacer
	mock := mocker.Create()
	t.Cleanup(func() {
		mock.Reset()
	})
	mock.Interface(&dbDAO).Method("GetDatasetByID").Apply(
		func(ctx *mocker.IContext, datasetID int) (*model.Dataset, error) {
			if datasetID != 1206 {
				t.Fatalf("GetDatasetByID datasetID = %d, want 1206", datasetID)
			}
			return &model.Dataset{
				DatasetID:   datasetID,
				DatasetName: "kline",
				ProjID:      12,
				Freqs:       "1m",
				Comment:     "old",
			}, nil
		},
	)
	mock.Interface(&dbDAO).Method("UpdateDataset").Apply(
		func(ctx *mocker.IContext, dataset *model.Dataset) error {
			if dataset.DatasetName != datasetName {
				t.Fatalf("DatasetName = %q, want %q", dataset.DatasetName, datasetName)
			}
			if dataset.Freqs != freqs {
				t.Fatalf("Freqs = %q, want %q", dataset.Freqs, freqs)
			}
			if dataset.Comment != comment {
				t.Fatalf("Comment = %q, want %q", dataset.Comment, comment)
			}
			if dataset.ObjectTableID == "" || dataset.DataTableID == "" {
				t.Fatalf("table IDs should not be empty: %+v", dataset)
			}
			return nil
		},
	)

	servicer := &MetaServicerImpl{dbDAO: dbDAO}
	rsp, err := servicer.UpdateDataSet(context.Background(), &pb.UpdateDataSetReq{
		ProjId:      12,
		DatasetId:   1206,
		DatasetName: &datasetName,
		Freqs:       &freqs,
		Comment:     &comment,
	})
	if err != nil {
		t.Fatalf("UpdateDataSet returned error: %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.EnumErrorCode_SUCCESS {
		t.Fatalf("UpdateDataSet code = %v, want %v, msg=%s", rsp.GetRetInfo().GetCode(), pb.EnumErrorCode_SUCCESS, rsp.GetRetInfo().GetMsg())
	}
}
