package cloudfunctionpb

import "testing"

func TestFunctionJobProtoContract(t *testing.T) {
	_ = FunctionJobOperation_FUNCTION_JOB_OPERATION_CREATE_INSTANCES
	_ = FunctionJobStatus_FUNCTION_JOB_STATUS_RUNNING
	_ = FunctionJobItemStatus_FUNCTION_JOB_ITEM_STATUS_FAILED
	_ = &SubmitFunctionJobRsp{}
	_ = &GetFunctionJobReq{}
	_ = &GetFunctionJobRsp{}
}
