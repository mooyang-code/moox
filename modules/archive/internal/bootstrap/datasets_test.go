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
	listRsps  map[uint32]*storagepb.ListDatasetsRsp
	spaceRsp  *storagepb.GetSpaceRsp
	err       error
	last      *storagepb.GetDatasetReq
	listReqs  []*storagepb.ListDatasetsReq
	lastSpace *storagepb.GetSpaceReq
	svc       string
	meth      string
}

func (f *fakeInvoker) Invoke(_ context.Context, svc, method string, req, rsp any) error {
	f.svc, f.meth = svc, method
	var listPage uint32
	switch r := req.(type) {
	case *storagepb.GetDatasetReq:
		f.last = r
	case *storagepb.ListDatasetsReq:
		f.listReqs = append(f.listReqs, r)
		listPage = r.GetPage().GetPage()
	case *storagepb.GetSpaceReq:
		f.lastSpace = r
	}
	if f.err != nil {
		return f.err
	}
	switch out := rsp.(type) {
	case *storagepb.GetDatasetRsp:
		proto.Merge(out, f.rsp)
	case *storagepb.ListDatasetsRsp:
		if pageRsp := f.listRsps[listPage]; pageRsp != nil {
			proto.Merge(out, pageRsp)
		}
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

func TestGatewayDatasetsListsDatasetsAcrossPages(t *testing.T) {
	inv := &fakeInvoker{listRsps: map[uint32]*storagepb.ListDatasetsRsp{
		1: {
			RetInfo:    &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
			Datasets:   []*storagepb.Dataset{{SpaceId: "crypto", DatasetId: "ds_1m", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1m", Status: "active", Retention: "168h"}},
			PageResult: &commonpb.PageResult{Page: 1, Size: 200, HasMore: true},
		},
		2: {
			RetInfo:    &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
			Datasets:   []*storagepb.Dataset{{SpaceId: "stockcn", DatasetId: "cn_5m", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Freq: "5m", Status: "active", Retention: "720h"}},
			PageResult: &commonpb.PageResult{Page: 2, Size: 200, HasMore: false},
		},
	}}
	got, err := newGatewayDatasets(inv).ListDatasets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].DatasetID != "ds_1m" || got[1].DatasetID != "cn_5m" || len(inv.listReqs) != 2 {
		t.Fatalf("datasets = %+v reqs=%d", got, len(inv.listReqs))
	}
	if inv.svc != "trpc.moox.storage.Metadata" || inv.meth != "ListDatasets" {
		t.Fatalf("call = %s/%s", inv.svc, inv.meth)
	}

	denied := &fakeInvoker{listRsps: map[uint32]*storagepb.ListDatasetsRsp{
		1: {RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_NO_PERMISSION, Msg: "denied"}},
	}}
	if _, err := newGatewayDatasets(denied).ListDatasets(context.Background()); err == nil {
		t.Fatal("返回非成功码必须报错")
	}

	broken := &fakeInvoker{err: errors.New("EOF")}
	if _, err := newGatewayDatasets(broken).ListDatasets(context.Background()); err == nil {
		t.Fatal("传输错误必须报错")
	}

	var unset *gatewayDatasets
	if _, err := unset.ListDatasets(context.Background()); err == nil {
		t.Fatal("没有网关客户端必须报错")
	}
}
