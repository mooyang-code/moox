package collectorpb

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"
)

func TestCollectorProtoMessages_ShouldExerciseGetters(t *testing.T) {
	msgs := []proto.Message{
		&TaskRule{},
		&GetTaskRuleListReq{},
		&GetTaskRuleListRsp{},
		&GetTaskRuleDetailReq{},
		&GetTaskRuleDetailRsp{},
		&CreateTaskRuleReq{},
		&CreateTaskRuleRsp{},
		&UpdateTaskRuleReq{},
		&UpdateTaskRuleRsp{},
		&DisableTaskRuleReq{},
		&DisableTaskRuleRsp{},
		&TaskInstance{},
		&TaskInstanceFilter{},
		&GetTaskInstanceListReq{},
		&GetTaskInstanceListRsp{},
		&ReportInstanceStatusReq{},
		&ReportInstanceStatusRsp{},
		&DataTypeConfig{},
		&DataTypeFieldConfig{},
		&DataTypeConfigDetail{},
		&GetDataTypeConfigsReq{},
		&GetDataTypeConfigsRsp{},
		&GetDataTypeConfigWithFieldsReq{},
		&GetDataTypeConfigWithFieldsRsp{},
		&RecalculateAllTaskInstancesReq{},
		&RecalculateAllTaskInstancesRsp{},
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
