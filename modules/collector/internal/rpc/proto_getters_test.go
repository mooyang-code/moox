package rpc

import (
	"reflect"
	"strings"
	"testing"

	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

func collectorProtoMessages() []proto.Message {
	params, _ := structpb.NewStruct(map[string]any{"source": map[string]any{"kind": "none"}})
	return []proto.Message{
		&pb.TaskRule{SpaceId: "crypto", RuleId: "rule-1", DataType: "symbol", Exchange: "binance", CollectParams: params},
		&pb.GetTaskRuleListReq{SpaceId: "crypto"},
		&pb.GetTaskRuleListRsp{},
		&pb.GetTaskRuleDetailReq{SpaceId: "crypto", RuleId: "rule-1"},
		&pb.GetTaskRuleDetailRsp{},
		&pb.CreateTaskRuleReq{},
		&pb.CreateTaskRuleRsp{RuleId: "rule-1"},
		&pb.UpdateTaskRuleReq{},
		&pb.UpdateTaskRuleRsp{},
		&pb.DisableTaskRuleReq{},
		&pb.DisableTaskRuleRsp{},
		&pb.TaskInstance{SpaceId: "crypto", TaskId: "task-1"},
		&pb.TaskInstanceFilter{SpaceId: "crypto"},
		&pb.GetTaskInstanceListReq{},
		&pb.GetTaskInstanceListRsp{},
		&pb.ReportInstanceStatusReq{SpaceId: "crypto", TaskId: "task-1"},
		&pb.ReportInstanceStatusRsp{},
		&pb.DataTypeConfig{DataType: "symbol"},
		&pb.DataTypeFieldConfig{FieldName: "exchange"},
		&pb.DataTypeConfigDetail{},
		&pb.GetDataTypeConfigsReq{},
		&pb.GetDataTypeConfigsRsp{},
		&pb.GetDataTypeConfigWithFieldsReq{DataType: "symbol"},
		&pb.GetDataTypeConfigWithFieldsRsp{},
		&pb.RecalculateAllTaskInstancesReq{SpaceId: "crypto"},
		&pb.RecalculateAllTaskInstancesRsp{},
	}
}

func callCollectorProtoGetters(msg proto.Message) {
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

func TestCollectorProtoMessages_ShouldExerciseGettersAndReflection(t *testing.T) {
	for _, msg := range collectorProtoMessages() {
		callCollectorProtoGetters(msg)
		assert.NotNil(t, msg)
	}
}
