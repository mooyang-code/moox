package serverless

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/tencentyun/scf-go-lib/functioncontext"
)

func TestHandleKeepaliveRunsPollWithServiceGatewayTarget(t *testing.T) {
	oldReport := reportHeartbeatAfterProbe
	oldPoll := pollJobItemsAfterHeartbeat
	t.Cleanup(func() {
		reportHeartbeatAfterProbe = oldReport
		pollJobItemsAfterHeartbeat = oldPoll
	})

	heartbeats := 0
	polls := 0
	reportHeartbeatAfterProbe = func(ctx context.Context) error {
		heartbeats++
		return nil
	}
	pollJobItemsAfterHeartbeat = func(ctx context.Context) error {
		polls++
		return nil
	}

	ctx := functioncontext.NewContext(context.Background(), &functioncontext.FunctionContext{
		RequestID:              "test-request",
		FunctionName:           "moox-collector-ap-guangzhou-0",
		FunctionVersion:        "$LATEST",
		Namespace:              "default",
		TencentcloudRegion:     "ap-guangzhou",
		InvokedFunctionUnique:  "moox-collector-ap-guangzhou-0",
		TencentcloudAppID:      "test-app",
		TencentcloudUin:        "test-uin",
	})
	rsp, err := NewCloudFunctionHandler().handleKeepalive(ctx, model.CloudFunctionEvent{
		Action:               model.EventActionKeepalive,
		Source:               "collector_schedule",
		ServiceGatewayTarget: "http://127.0.0.1:11000",
	})
	if err != nil {
		t.Fatalf("handleKeepalive() error = %v", err)
	}
	if rsp == nil || !rsp.Success {
		t.Fatalf("handleKeepalive() response = %#v, want success", rsp)
	}
	if heartbeats != 1 {
		t.Fatalf("heartbeats = %d, want 1", heartbeats)
	}
	if polls != 1 {
		t.Fatalf("polls = %d, want 1", polls)
	}
}
