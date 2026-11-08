package bootstrap

import (
	"context"
	"strings"
	"testing"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"google.golang.org/protobuf/proto"
)

type fakeCollectorGateway struct {
	service, method string
	rsp             *collectorpb.GetCollectorStatusRsp
}

func (f *fakeCollectorGateway) Invoke(_ context.Context, service, method string, req, rsp any) error {
	f.service, f.method = service, method
	if req.(*collectorpb.GetCollectorStatusReq).GetSpaceId() != "crypto" {
		panic("unexpected space")
	}
	proto.Merge(rsp.(proto.Message), f.rsp)
	return nil
}

func TestGatewayCollectorReadsFunctionFrequencies(t *testing.T) {
	gw := &fakeCollectorGateway{rsp: &collectorpb.GetCollectorStatusRsp{
		RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
		Functions: []*collectorpb.CollectorFunctionStatus{
			{Datasets: []string{"dataset_a|1m", "dataset_b|1h"}, State: "applied"},
			{Datasets: []string{"dataset_c|5m", "坏条目", "dataset_d|"}, State: "retiring"},
		},
	}}
	freqs, err := newGatewayCollector(gw).SpaceFrequencies(context.Background(), "crypto")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(freqs, ",") != "1m,1h,5m" || gw.service != "trpc.moox.collector.CollectMgr" || gw.method != "GetCollectorStatus" {
		t.Fatalf("freqs=%v call=%s.%s", freqs, gw.service, gw.method)
	}
	gw.rsp = &collectorpb.GetCollectorStatusRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: "boom"}}
	if _, err := newGatewayCollector(gw).SpaceFrequencies(context.Background(), "crypto"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
}
