package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestPostCollectorMarketUsesCollectorRPCPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/trpc.moox.collector.CollectMgr/GetMarketStatus" || r.Method != http.MethodPost {
			t.Fatalf("request=%s %s", r.Method, r.URL.Path)
		}
		raw, _ := protojson.Marshal(&pb.GetMarketStatusRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Module: &pb.MarketModule{MarketId: "stock_cn"}})
		_, _ = w.Write(raw)
	}))
	defer server.Close()
	var rsp pb.GetMarketStatusRsp
	if err := postCollectorMarket(context.Background(), server.URL, "GetMarketStatus", &pb.GetMarketStatusReq{MarketId: "stock_cn"}, &rsp); err != nil {
		t.Fatal(err)
	}
	if rsp.GetModule().GetMarketId() != "stock_cn" {
		t.Fatalf("rsp=%+v", rsp)
	}
}

func TestPostCollectorMarketRequiresControlURL(t *testing.T) {
	if err := postCollectorMarket(context.Background(), "", "GetMarketStatus", &pb.GetMarketStatusReq{}, &pb.GetMarketStatusRsp{}); err == nil {
		t.Fatal("empty control URL accepted")
	}
}
