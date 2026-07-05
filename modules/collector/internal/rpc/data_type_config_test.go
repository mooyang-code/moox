package rpc

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
)

func TestGetDataTypeConfigWithFieldsNormalizesDataType(t *testing.T) {
	svc := &Service{}

	rsp, err := svc.GetDataTypeConfigWithFields(context.Background(), &pb.GetDataTypeConfigWithFieldsReq{
		DataType: " KLINE ",
	})
	if err != nil {
		t.Fatalf("GetDataTypeConfigWithFields() error = %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("ret code = %v, want SUCCESS, msg=%s", rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	}
	if rsp.GetDetail().GetConfig().GetDataType() != "kline" {
		t.Fatalf("data_type = %q, want kline", rsp.GetDetail().GetConfig().GetDataType())
	}
	if len(rsp.GetDetail().GetFields()) != 3 {
		t.Fatalf("len(fields) = %d, want 3", len(rsp.GetDetail().GetFields()))
	}
}
