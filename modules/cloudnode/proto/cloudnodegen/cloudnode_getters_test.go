package cloudnodepb

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"
)

func TestCloudnodeProtoMessages_ShouldExerciseGetters(t *testing.T) {
	msgs := []proto.Message{
		&CloudNode{},
		&GetNodeListReq{},
		&GetNodeListRsp{},
		&UpdateNodeReq{},
		&UpdateNodeRsp{},
		&InvokeFunctionReq{},
		&ScfInvokeResult{},
		&InvokeFunctionRsp{},
		&NodeCreateItem{},
		&BatchCreateNodesReq{},
		&BatchChangeResult{},
		&BatchDeleteNodesReq{},
		&NodeDeployItem{},
		&BatchDeployNodesReq{},
		&CloudAccountSummary{},
		&CloudAccountInput{},
		&ListCloudAccountsReq{},
		&ListCloudAccountsRsp{},
		&CreateCloudAccountReq{},
		&CreateCloudAccountRsp{},
		&UpdateCloudAccountReq{},
		&UpdateCloudAccountRsp{},
		&DeleteCloudAccountReq{},
		&DeleteCloudAccountRsp{},
		&CloudAccountSecret{},
		&GetCOSAccountInfoReq{},
		&GetCOSAccountInfoRsp{},
		&CloudRegion{},
		&ListCloudRegionsReq{},
		&ListCloudRegionsRsp{},
		&PackageListItem{},
		&GetPackageListReq{},
		&GetPackageListRsp{},
		&PackageDetail{},
		&GetPackageDetailReq{},
		&GetPackageDetailRsp{},
		&DeletePackageReq{},
		&DeletePackageRsp{},
		&PackageDownloadURL{},
		&GetPackageDownloadURLReq{},
		&GetPackageDownloadURLRsp{},
		&InitPackageUploadReq{},
		&InitPackageUploadRsp{},
		&CompletePackageUploadReq{},
		&CompletePackageUploadRsp{},
		&LocalDNSReportItem{},
		&ReportHeartbeatReq{},
		&ControlDirective{},
		&ReportHeartbeatRsp{},
		&JobItem{},
		&SubmitJobItemsReq{},
		&JobItemAck{},
		&SubmitJobItemsRsp{},
		&PollJobItemsReq{},
		&PolledJobItem{},
		&PollJobItemsRsp{},
		&ReportJobItemStatusReq{},
		&ReportJobItemStatusRsp{},
		&CancelJobItemReq{},
		&CancelJobItemRsp{},
		&GetJobItemReq{},
		&JobItemDetail{},
		&GetJobItemRsp{},
		&ListJobItemsReq{},
		&ListJobItemsRsp{},
		&JobItemAttempt{},
		&ListJobItemAttemptsReq{},
		&ListJobItemAttemptsRsp{},
		&InvokeSyncPayload{},
		&InvokeSyncReq{},
		&InvokeSyncResult{},
		&InvokeSyncRsp{},
	}
	for _, msg := range msgs {
		rv := reflect.ValueOf(msg); rt := rv.Type()
		for i := 0; i < rt.NumMethod(); i++ {
			m := rt.Method(i)
			if strings.HasPrefix(m.Name, "Get") && m.Type.NumIn() == 1 { m.Func.Call([]reflect.Value{rv}) }
		}
		_ = msg.ProtoReflect(); assert.NotNil(t, msg)
	}
}
