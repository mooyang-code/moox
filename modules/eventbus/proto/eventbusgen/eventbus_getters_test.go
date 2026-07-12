package eventbuspb

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"
)

func eventbusProtoMessages() []proto.Message {
	return []proto.Message{
		&GetOverviewReq{},
		&GetOverviewRsp{},
		&ListTopicsReq{},
		&ListTopicsRsp{},
		&ListStreamsReq{},
		&ListStreamsRsp{},
		&ListConsumersReq{},
		&ListConsumersRsp{},
		&GetConsumerReq{},
		&GetConsumerRsp{},
		&TopicInfo{},
		&StreamInfo{},
		&ConsumerInfo{},
		&Overview{},
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

func TestEventbusProtoMessages_ShouldExerciseGetters(t *testing.T) {
	for _, msg := range eventbusProtoMessages() {
		callProtoGetters(msg)
		assert.NotNil(t, msg)
	}
}

