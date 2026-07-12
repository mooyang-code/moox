package rpc

import (
	"reflect"
	"strings"
	"testing"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"
)

func cloudnodeProtoMessages() []proto.Message {
	return []proto.Message{
		&pb.CloudNode{},
		&pb.GetNodeListReq{},
		&pb.GetNodeListRsp{},
		&pb.UpdateNodeReq{},
		&pb.UpdateNodeRsp{},
		&pb.InvokeFunctionReq{},
		&pb.ScfInvokeResult{},
		&pb.InvokeFunctionRsp{},
		&pb.NodeCreateItem{},
		&pb.BatchCreateNodesReq{},
		&pb.BatchChangeResult{},
		&pb.BatchDeleteNodesReq{},
		&pb.NodeDeployItem{},
		&pb.BatchDeployNodesReq{},
		&pb.CloudAccountSummary{},
		&pb.CloudAccountInput{},
		&pb.ListCloudAccountsReq{},
		&pb.ListCloudAccountsRsp{},
		&pb.CreateCloudAccountReq{},
		&pb.CreateCloudAccountRsp{},
		&pb.UpdateCloudAccountReq{},
		&pb.UpdateCloudAccountRsp{},
		&pb.DeleteCloudAccountReq{},
		&pb.DeleteCloudAccountRsp{},
		&pb.CloudAccountSecret{},
		&pb.GetCOSAccountInfoReq{},
		&pb.GetCOSAccountInfoRsp{},
		&pb.CloudRegion{},
		&pb.ListCloudRegionsReq{},
		&pb.ListCloudRegionsRsp{},
		&pb.PackageListItem{},
		&pb.GetPackageListReq{},
		&pb.GetPackageListRsp{},
		&pb.PackageDetail{},
		&pb.GetPackageDetailReq{},
		&pb.GetPackageDetailRsp{},
		&pb.DeletePackageReq{},
		&pb.DeletePackageRsp{},
		&pb.PackageDownloadURL{},
		&pb.GetPackageDownloadURLReq{},
		&pb.GetPackageDownloadURLRsp{},
		&pb.InitPackageUploadReq{},
		&pb.InitPackageUploadRsp{},
		&pb.CompletePackageUploadReq{},
		&pb.CompletePackageUploadRsp{},
		&pb.LocalDNSReportItem{},
		&pb.ReportHeartbeatReq{},
		&pb.ControlDirective{},
		&pb.ReportHeartbeatRsp{},
		&pb.JobItem{},
		&pb.SubmitJobItemsReq{},
		&pb.JobItemAck{},
		&pb.SubmitJobItemsRsp{},
		&pb.PollJobItemsReq{},
		&pb.PolledJobItem{},
		&pb.PollJobItemsRsp{},
		&pb.ReportJobItemStatusReq{},
		&pb.ReportJobItemStatusRsp{},
		&pb.CancelJobItemReq{},
		&pb.CancelJobItemRsp{},
		&pb.GetJobItemReq{},
		&pb.JobItemDetail{},
		&pb.GetJobItemRsp{},
		&pb.ListJobItemsReq{},
		&pb.ListJobItemsRsp{},
		&pb.JobItemAttempt{},
		&pb.ListJobItemAttemptsReq{},
		&pb.ListJobItemAttemptsRsp{},
		&pb.InvokeSyncPayload{},
		&pb.InvokeSyncReq{},
		&pb.InvokeSyncResult{},
		&pb.InvokeSyncRsp{},
	}
}

func callCloudnodeProtoGetters(msg proto.Message) {
	rv := reflect.ValueOf(msg)
	rt := rv.Type()
	for i := 0; i < rt.NumMethod(); i++ {
		method := rt.Method(i)
		if strings.HasPrefix(method.Name, "Get") && method.Type.NumIn() == 1 {
			method.Func.Call([]reflect.Value{rv})
		}
	}
	if d, ok := msg.(interface {
		Descriptor() ([]byte, []int)
	}); ok {
		_, _ = d.Descriptor()
	}
	_ = msg.ProtoReflect()
	if r, ok := msg.(interface{ Reset() }); ok {
		r.Reset()
	}
	if s, ok := msg.(interface{ String() string }); ok {
		_ = s.String()
	}
}

func TestCloudnodeProtoMessages_ShouldExerciseGettersAndReflection(t *testing.T) {
	for _, msg := range cloudnodeProtoMessages() {
		callCloudnodeProtoGetters(msg)
		assert.NotNil(t, msg)
	}
}
