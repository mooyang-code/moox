package bootstrap

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/archive/internal/service"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"google.golang.org/protobuf/proto"
)

type fakeInvoker struct {
	rsp  *storagepb.GetDatasetRsp
	err  error
	last *storagepb.GetDatasetReq
	svc  string
	meth string
}

func (f *fakeInvoker) Invoke(_ context.Context, svc, method string, req, rsp any) error {
	f.svc, f.meth = svc, method
	f.last = req.(*storagepb.GetDatasetReq)
	if f.err != nil {
		return f.err
	}
	proto.Merge(rsp.(*storagepb.GetDatasetRsp), f.rsp)
	return nil
}

func TestGatewayDatasetsMapsStorageDatasets(t *testing.T) {
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "s3cret-s3cret-s3cret-s3cret-s3cret")
	inv := &fakeInvoker{rsp: &storagepb.GetDatasetRsp{
		RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
		Dataset: &storagepb.Dataset{SpaceId: "crypto", DatasetId: "ds", Name: "K线", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1m", Status: "active", DataNodeId: "node-0", Retention: "168h"},
	}}
	ds, err := newGatewayDatasets(inv).GetDataset(context.Background(), "crypto", "ds")
	if err != nil {
		t.Fatal(err)
	}
	if ds.Freq != "1m" || ds.DataKind != "time_series" || ds.Status != "active" || ds.DataNodeID != "node-0" || ds.Retention != "168h" || ds.Name != "K线" {
		t.Fatalf("dataset = %+v", ds)
	}
	if inv.svc != "trpc.moox.storage.Metadata" || inv.meth != "GetDataset" || inv.last.GetSpaceId() != "crypto" || inv.last.GetDatasetId() != "ds" {
		t.Fatalf("call = %s/%s %v", inv.svc, inv.meth, inv.last)
	}
	auth := inv.last.GetAuthInfo()
	if auth.GetAppId() != "archive" || auth.GetAppKey() == "" || auth.GetOperator() != "archive-mgr" {
		t.Fatalf("auth = %v", auth)
	}
}

func TestGatewayDatasetsReportsMissingDatasetsAndTransportErrors(t *testing.T) {
	missing := &fakeInvoker{rsp: &storagepb.GetDatasetRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_DATASET_NOT_FOUND, Msg: "no"}}}
	if _, err := newGatewayDatasets(missing).GetDataset(context.Background(), "crypto", "x"); !errors.Is(err, service.ErrDatasetNotFound) {
		t.Fatalf("err = %v", err)
	}
	denied := &fakeInvoker{rsp: &storagepb.GetDatasetRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_NO_PERMISSION, Msg: "denied"}}}
	if _, err := newGatewayDatasets(denied).GetDataset(context.Background(), "crypto", "x"); err == nil || errors.Is(err, service.ErrDatasetNotFound) {
		t.Fatalf("err = %v", err)
	}
	broken := &fakeInvoker{err: errors.New("EOF")}
	if _, err := newGatewayDatasets(broken).GetDataset(context.Background(), "crypto", "x"); err == nil {
		t.Fatal("传输错误必须返回")
	}
	none := &fakeInvoker{rsp: &storagepb.GetDatasetRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}}}
	if _, err := newGatewayDatasets(none).GetDataset(context.Background(), "crypto", "x"); err == nil {
		t.Fatal("成功但没有数据集必须报错")
	}
	var unset *gatewayDatasets
	if _, err := unset.GetDataset(context.Background(), "crypto", "x"); err == nil {
		t.Fatal("没有网关客户端必须报错")
	}
}
