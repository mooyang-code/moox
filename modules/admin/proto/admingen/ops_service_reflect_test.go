package mooxpb

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestOpsProtoMessagesViaReflect_ShouldMarshal(t *testing.T) {
	if File_ops_service_proto == nil {
		t.Skip("ops proto file descriptor unavailable")
	}
	for i := 0; i < File_ops_service_proto.Messages().Len(); i++ {
		populateOpsDynamicMessage(t, dynamicpb.NewMessage(File_ops_service_proto.Messages().Get(i)))
	}
	for i := 0; i < File_ops_service_proto.Enums().Len(); i++ {
		enum := File_ops_service_proto.Enums().Get(i)
		if enum.Values().Len() > 0 {
			_ = enum.Values().Get(0).Name()
		}
	}
}

func populateOpsDynamicMessage(t *testing.T, msg *dynamicpb.Message) {
	t.Helper()
	pr := msg.ProtoReflect()
	_, err := proto.Marshal(msg)
	require.NoError(t, err)
	for i := 0; i < pr.Descriptor().Fields().Len(); i++ {
		fd := pr.Descriptor().Fields().Get(i)
		switch {
		case fd.IsMap() && fd.MapKey().Kind() == protoreflect.StringKind:
			pr.Mutable(fd).Map().Set(
				protoreflect.MapKey(protoreflect.ValueOfString("k")),
				protoreflect.ValueOfString("v"),
			)
		case fd.IsList() && fd.Kind() == protoreflect.StringKind:
			pr.Mutable(fd).List().Append(protoreflect.ValueOfString("item"))
		case fd.Message() != nil && !fd.IsList() && !fd.IsMap():
			pr.Set(fd, protoreflect.ValueOfMessage(dynamicpb.NewMessage(fd.Message())))
		default:
			setOpsScalarField(pr, fd)
		}
		_ = pr.Get(fd)
	}
	_, err = proto.Marshal(msg)
	require.NoError(t, err)
}

func setOpsScalarField(pr protoreflect.Message, fd protoreflect.FieldDescriptor) {
	switch fd.Kind() {
	case protoreflect.StringKind:
		pr.Set(fd, protoreflect.ValueOfString("test"))
	case protoreflect.BoolKind:
		pr.Set(fd, protoreflect.ValueOfBool(true))
	case protoreflect.EnumKind:
		if fd.Enum().Values().Len() > 0 {
			pr.Set(fd, protoreflect.ValueOfEnum(fd.Enum().Values().Get(0).Number()))
		}
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		pr.Set(fd, protoreflect.ValueOfInt32(1))
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		pr.Set(fd, protoreflect.ValueOfInt64(1))
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		pr.Set(fd, protoreflect.ValueOfUint32(1))
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		pr.Set(fd, protoreflect.ValueOfUint64(1))
	case protoreflect.FloatKind:
		pr.Set(fd, protoreflect.ValueOfFloat32(1))
	case protoreflect.DoubleKind:
		pr.Set(fd, protoreflect.ValueOfFloat64(1))
	case protoreflect.BytesKind:
		pr.Set(fd, protoreflect.ValueOfBytes([]byte("x")))
	}
}

func TestOpsProtoConcreteTypes_ShouldExposeGetters(t *testing.T) {
	msgs := []proto.Message{
		&SSHHost{}, &ListHostsReq{}, &ListHostsRsp{Hosts: []*SSHHost{{}}},
		&CreateHostReq{Host: &SSHHost{}}, &CreateHostRsp{},
		&SessionInfo{}, &GetOnlineSessionsRsp{Sessions: []*SessionInfo{{}}},
		&SftpListRsp{Files: []*SftpFileItem{{}}},
	}
	for _, msg := range msgs {
		t.Run(reflect.TypeOf(msg).Elem().Name(), func(t *testing.T) {
			exerciseMessage(t, msg.(interface {
				Reset()
				String() string
				ProtoMessage()
			}))
			invokeGetters(t, msg)
		})
	}
}
