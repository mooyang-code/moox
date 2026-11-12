package primarystore

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type factorDatasetAdminSnapshot struct{ dataset *pb.Dataset }

func (s factorDatasetAdminSnapshot) GetDataset(string, string) (*pb.Dataset, bool) {
	return s.dataset, s.dataset != nil
}

func (factorDatasetAdminSnapshot) ListDatasetColumns(string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return nil, nil, nil
}

func newFactorDatasetAdminService(t *testing.T, store Store, appID string) *Service {
	t.Helper()
	dataset := &pb.Dataset{
		SpaceId: "crypto", DatasetId: "dataset_factor_prices_1m", Status: "active",
		Attributes: map[string]string{"dataset_role": "factor_result"},
	}
	service, err := New(Options{
		Store:    store,
		Snapshot: func() metadata.RequestSnapshot { return factorDatasetAdminSnapshot{dataset: dataset} },
		Authorizer: func(auth *pb.AuthInfo) error {
			if auth.GetAppId() != appID {
				return errors.New("unexpected caller")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestDeleteDatasetRowsAllowsFactorResultOwner(t *testing.T) {
	store := newHookStore(t)
	deleted := 0
	store.deleteRows = func(_ context.Context, spaceID, datasetID string) (uint64, error) {
		deleted++
		if spaceID != "crypto" || datasetID != "dataset_factor_prices_1m" {
			t.Errorf("deleted unexpected dataset %s/%s", spaceID, datasetID)
		}
		return 1, nil
	}
	service := newFactorDatasetAdminService(t, store, "factor")
	rsp, err := service.DeleteDatasetRows(context.Background(), &pb.PrimaryDeleteDatasetRowsReq{
		AuthInfo: &pb.AuthInfo{AppId: "factor"}, SpaceId: "crypto", DatasetId: "dataset_factor_prices_1m",
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || rsp.GetDeletedRanges() != 1 {
		t.Fatalf("response=%v err=%v", rsp, err)
	}
	if deleted != 1 {
		t.Fatalf("store delete count = %d, want 1", deleted)
	}
}

// 删除墓碑已去掉，RestoreDatasetRows 只校验调用方并返回成功。
func TestRestoreDatasetRowsIsANoOp(t *testing.T) {
	service := newFactorDatasetAdminService(t, newHookStore(t), "factor")
	rsp, err := service.RestoreDatasetRows(context.Background(), &pb.PrimaryRestoreDatasetRowsReq{
		AuthInfo: &pb.AuthInfo{AppId: "factor"}, SpaceId: "crypto", DatasetId: "dataset_factor_prices_1m",
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("response=%v err=%v", rsp, err)
	}
	denied, err := service.RestoreDatasetRows(context.Background(), &pb.PrimaryRestoreDatasetRowsReq{
		AuthInfo: &pb.AuthInfo{AppId: "other"}, SpaceId: "crypto", DatasetId: "dataset_factor_prices_1m",
	})
	if err != nil || denied.GetRetInfo().GetCode() != pb.ErrorCode_NO_PERMISSION {
		t.Fatalf("denied response=%v err=%v", denied, err)
	}
}
