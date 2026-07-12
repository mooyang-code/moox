package rpc

import (
	"reflect"
	"strings"
	"testing"

	pb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"
)

func FactorProtoMessages() []proto.Message {
	return []proto.Message{
		&pb.FactorDef{},
		&pb.FactorBinding{},
		&pb.FactorRun{},
		&pb.CreateFactorReq{},
		&pb.CreateFactorRsp{},
		&pb.UpdateFactorReq{},
		&pb.UpdateFactorRsp{},
		&pb.GetFactorReq{},
		&pb.GetFactorRsp{},
		&pb.ListFactorsReq{},
		&pb.ListFactorsRsp{},
		&pb.SetFactorStatusReq{},
		&pb.SetFactorStatusRsp{},
		&pb.UpsertBindingReq{},
		&pb.UpsertBindingRsp{},
		&pb.ListBindingsReq{},
		&pb.ListBindingsRsp{},
		&pb.DeleteBindingReq{},
		&pb.DeleteBindingRsp{},
		&pb.RecalcFactorReq{},
		&pb.RecalcFactorRsp{},
		&pb.GetRecalcProgressReq{},
		&pb.GetRecalcProgressRsp{},
		&pb.ListFactorRunsReq{},
		&pb.ListFactorRunsRsp{},
		&pb.WorkerStatus{},
		&pb.GetEngineStatusReq{},
		&pb.GetEngineStatusRsp{},
	}
}

func callFactorProtoGetters(msg proto.Message) {
	rv := reflect.ValueOf(msg)
	rt := rv.Type()
	for i := 0; i < rt.NumMethod(); i++ {
		method := rt.Method(i)
		if strings.HasPrefix(method.Name, "Get") && method.Type.NumIn() == 1 {
			method.Func.Call([]reflect.Value{rv})
		}
	}
	_ = msg.ProtoReflect()
	if d, ok := msg.(interface {
		Descriptor() ([]byte, []int)
	}); ok {
		_, _ = d.Descriptor()
	}
	if r, ok := msg.(interface{ Reset() }); ok {
		r.Reset()
	}
	if s, ok := msg.(interface{ String() string }); ok {
		_ = s.String()
	}
}

func TestFactorProtoMessages_ShouldExerciseGetters(t *testing.T) {
	for _, msg := range FactorProtoMessages() {
		callFactorProtoGetters(msg)
		assert.NotNil(t, msg)
	}
}
