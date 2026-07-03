package rpc

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
)

func TestCountSyncResultsSeparatesTimeout(t *testing.T) {
	results := []*pb.InvokeSyncResult{
		{Status: pb.InvocationStatus_INVOCATION_STATUS_SUCCESS},
		{Status: pb.InvocationStatus_INVOCATION_STATUS_FAILED, ErrorMessage: "timeout: context deadline exceeded"},
		{Status: pb.InvocationStatus_INVOCATION_STATUS_FAILED, ErrorMessage: "invoke failed"},
	}
	success, failed, timeout := countSyncResults(results)
	if success != 1 || failed != 1 || timeout != 1 {
		t.Fatalf("success=%d failed=%d timeout=%d", success, failed, timeout)
	}
}

func TestTimeoutSyncResultMarksDeadlineExceeded(t *testing.T) {
	result := timeoutSyncResult("req-1", context.DeadlineExceeded)
	if !isTimeoutSyncResult(result) {
		t.Fatalf("expected timeout result, got %q", result.GetErrorMessage())
	}
}
