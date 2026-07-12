package factorpb

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"
)

func factorProtoMessages() []proto.Message {
	return []proto.Message{
		&FactorDef{},
		&FactorBinding{},
		&FactorRun{},
		&CreateFactorReq{},
		&CreateFactorRsp{},
		&UpdateFactorReq{},
		&UpdateFactorRsp{},
		&GetFactorReq{},
		&GetFactorRsp{},
		&ListFactorsReq{},
		&ListFactorsRsp{},
		&SetFactorStatusReq{},
		&SetFactorStatusRsp{},
		&UpsertBindingReq{},
		&UpsertBindingRsp{},
		&ListBindingsReq{},
		&ListBindingsRsp{},
		&DeleteBindingReq{},
		&DeleteBindingRsp{},
		&RecalcFactorReq{},
		&RecalcFactorRsp{},
		&GetRecalcProgressReq{},
		&GetRecalcProgressRsp{},
		&ListFactorRunsReq{},
		&ListFactorRunsRsp{},
		&WorkerStatus{},
		&GetEngineStatusReq{},
		&GetEngineStatusRsp{},
	}
}

func callProtoGetters(msg proto.Message) {
	rv := reflect.ValueOf(msg)
	rt := rv.Type()
	for i := 0; i < rt.NumMethod(); i++ {
		method := rt.Method(i)
		if strings.HasPrefix(method.Name, "Get") && method.Type.NumIn() == 1 {
			method.Func.Call([]reflect.Value{rv})
		}
	}
	_ = msg.ProtoReflect()
	if r, ok := msg.(interface{ Reset() }); ok {
		r.Reset()
	}
	if s, ok := msg.(interface{ String() string }); ok {
		_ = s.String()
	}
}

func TestFactorProtoMessages_ShouldExerciseGetters(t *testing.T) {
	for _, msg := range factorProtoMessages() {
		callProtoGetters(msg)
		assert.NotNil(t, msg)
	}
}

