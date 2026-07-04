package rpc

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
)

func TestPollJobItemsRequiresSupportedProtocolVersion(t *testing.T) {
	svc := &Service{}
	for _, protocolVersion := range []string{"", "unknown"} {
		rsp, err := svc.PollJobItems(context.Background(), &pb.PollJobItemsReq{
			SpaceId:           "space-a",
			NodeId:            "node-a",
			SupportedJobTypes: []string{"collect.kline"},
			ProtocolVersion:   protocolVersion,
		})
		if err != nil {
			t.Fatalf("PollJobItems() transport error = %v", err)
		}
		if rsp.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM {
			t.Fatalf("protocol_version %q ret code = %v, want INVALID_PARAM", protocolVersion, rsp.GetRetInfo().GetCode())
		}
	}
}
