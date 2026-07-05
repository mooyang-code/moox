package rpc

import (
	"testing"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
)

func TestCloudNodeProtoContractIncludesQueueDirectives(t *testing.T) {
	if pb.JobItemStatus_JOB_ITEM_STATUS_ENQUEUE_FAILED.Number() == 0 {
		t.Fatalf("JOB_ITEM_STATUS_ENQUEUE_FAILED must be non-zero")
	}
	if pb.JobItemReportStatus_JOB_ITEM_REPORT_STATUS_CANCELED.Number() == 0 {
		t.Fatalf("JOB_ITEM_REPORT_STATUS_CANCELED must be non-zero")
	}
	rsp := &pb.ReportHeartbeatRsp{Directives: []*pb.ControlDirective{{
		Type:      pb.ControlDirectiveType_CONTROL_DIRECTIVE_CANCEL,
		JobItemId: "ji-1",
		AttemptNo: 1,
		Reason:    "test",
	}}}
	if len(rsp.GetDirectives()) != 1 || rsp.GetDirectives()[0].GetType() != pb.ControlDirectiveType_CONTROL_DIRECTIVE_CANCEL {
		t.Fatalf("heartbeat directives not available: %+v", rsp)
	}
}
