package cloudnodepb

import "testing"

func TestNodeBatchProtoContract(t *testing.T) {
	_ = NodeBatchOperation_NODE_BATCH_OPERATION_CREATE_NODES
	_ = NodeBatchStatus_NODE_BATCH_STATUS_RUNNING
	_ = NodeBatchItemStatus_NODE_BATCH_ITEM_STATUS_FAILED
	_ = &SubmitNodeBatchRsp{}
	_ = &GetNodeBatchChangeReq{}
	_ = &GetNodeBatchChangeRsp{}
}
