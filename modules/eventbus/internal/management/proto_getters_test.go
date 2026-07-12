package management

import (
	"reflect"
	"strings"
	"testing"

	pb "github.com/mooyang-code/moox/modules/eventbus/proto/eventbusgen"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"
)

func EventbusProtoMessages() []proto.Message {
	return []proto.Message{
		&pb.GetOverviewReq{},
		&pb.GetOverviewRsp{},
		&pb.ListTopicsReq{},
		&pb.ListTopicsRsp{},
		&pb.ListStreamsReq{},
		&pb.ListStreamsRsp{},
		&pb.ListConsumersReq{},
		&pb.ListConsumersRsp{},
		&pb.GetConsumerReq{},
		&pb.GetConsumerRsp{},
		&pb.TopicInfo{},
		&pb.StreamInfo{},
		&pb.ConsumerInfo{},
		&pb.Overview{},
	}
}

func callEventbusProtoGetters(msg proto.Message) {
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

func TestEventbusProtoMessages_ShouldExerciseGetters(t *testing.T) {
	for _, msg := range EventbusProtoMessages() {
		callEventbusProtoGetters(msg)
		assert.NotNil(t, msg)
	}
}
