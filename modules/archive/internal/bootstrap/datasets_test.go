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
	rsp       *storagepb.GetDatasetRsp
	spaceRsp  *storagepb.GetSpaceRsp
	err       error
	last      *storagepb.GetDatasetReq
	lastSpace *storagepb.GetSpaceReq
	svc       string
	meth      string
}

func (f *fakeInvoker) Invoke(_ context.Context, svc, method string, req, rsp any) error {
	f.svc, f.meth = svc, method
	switch r := req.(type) {
	case *storagepb.GetDatasetReq:
		f.last = r
	case *storagepb.GetSpaceReq:
		f.lastSpace = r
	}
	if f.err != nil {
		return f.err
	}
	switch out := rsp.(type) {
	case *storagepb.GetDatasetRsp:
		proto.Merge(out, f.rsp)
	case *storagepb.GetSpaceRsp:
		proto.Merge(out, f.spaceRsp)
	}
	return nil
}

func TestGatewayDatasetsMapsStorageDatasets(t *testing.T) {
	inv := &fakeInvoker{rsp: &storagepb.GetDatasetRsp{
		RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
		Dataset: &storagepb.Dataset{SpaceId: "crypto", DatasetId: "ds", Name: "K线", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1m", Status: "active", Retention: "168h"},
	}}
	ds, err := newGatewayDatasets(inv).GetDataset(context.Background(), "crypto", "ds")
	if err != nil {
		t.Fatal(err)
	}
	if ds.Freq != "1m" || ds.DataKind != "time_series" || ds.Status != "active" || ds.Retention != "168h" || ds.Name != "K线" {
		t.Fatalf("dataset = %+v", ds)
	}
	if inv.svc != "trpc.moox.storage.Metadata" || inv.meth != "GetDataset" || inv.last.GetSpaceId() != "crypto" || inv.last.GetDatasetId() != "ds" {
		t.Fatalf("call = %s/%s %v", inv.svc, inv.meth, inv.last)
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

func TestGatewayDatasetsReadsSpaceTimezone(t *testing.T) {
	inv := &fakeInvoker{spaceRsp: &storagepb.GetSpaceRsp{
		RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
		Space:   &storagepb.Space{SpaceId: "stockcn", Timezone: "Asia/Shanghai"},
	}}
	tz, err := newGatewayDatasets(inv).GetSpaceTimezone(context.Background(), "stockcn")
	if err != nil || tz != "Asia/Shanghai" {
		t.Fatalf("tz=%q err=%v", tz, err)
	}
	if inv.svc != "trpc.moox.storage.Metadata" || inv.meth != "GetSpace" || inv.lastSpace.GetSpaceId() != "stockcn" {
		t.Fatalf("call = %s/%s %v", inv.svc, inv.meth, inv.lastSpace)
	}

	emptyTZ := &fakeInvoker{spaceRsp: &storagepb.GetSpaceRsp{
		RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
		Space:   &storagepb.Space{SpaceId: "crypto"},
	}}
	if tz, err := newGatewayDatasets(emptyTZ).GetSpaceTimezone(context.Background(), "crypto"); err != nil || tz != "UTC" {
		t.Fatalf("空时区应默认 UTC: tz=%q err=%v", tz, err)
	}

	missing := &fakeInvoker{spaceRsp: &storagepb.GetSpaceRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SPACE_NOT_FOUND, Msg: "no"}}}
	if _, err := newGatewayDatasets(missing).GetSpaceTimezone(context.Background(), "ghost"); !errors.Is(err, service.ErrSpaceNotFound) {
		t.Fatalf("missing err = %v", err)
	}

	badTZ := &fakeInvoker{spaceRsp: &storagepb.GetSpaceRsp{
		RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
		Space:   &storagepb.Space{SpaceId: "mars", Timezone: "Mars/Base"},
	}}
	if _, err := newGatewayDatasets(badTZ).GetSpaceTimezone(context.Background(), "mars"); err == nil {
		t.Fatal("非法时区必须报错")
	}

	var unset *gatewayDatasets
	if _, err := unset.GetSpaceTimezone(context.Background(), "crypto"); err == nil {
		t.Fatal("没有网关客户端必须报错")
	}
}
