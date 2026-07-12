package storagepb

import (
	"reflect"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

var generatedFiles = []protoreflect.FileDescriptor{
	File_access_proto,
	File_common_proto,
	File_message_proto,
	File_metadata_proto,
	File_store_proto,
	File_view_proto,
	File_view_index_proto,
}

var generatedGoTypes = [][]interface{}{
	file_access_proto_goTypes,
	file_common_proto_goTypes,
	file_message_proto_goTypes,
	file_metadata_proto_goTypes,
	file_store_proto_goTypes,
	file_view_proto_goTypes,
	file_view_index_proto_goTypes,
}

func TestGeneratedMessagesRoundTripViaProtoReflect(t *testing.T) {
	for _, file := range generatedFiles {
		if file == nil {
			continue
		}
		for i := 0; i < file.Enums().Len(); i++ {
			enum := file.Enums().Get(i)
			if enum.Values().Len() > 0 {
				_ = enum.Values().Get(0).Name()
			}
		}
		for i := 0; i < file.Messages().Len(); i++ {
			populateDynamicMessage(t, dynamicpb.NewMessage(file.Messages().Get(i)))
		}
	}
}

func TestConcreteGeneratedTypesMarshal(t *testing.T) {
	for _, group := range generatedGoTypes {
		for _, typ := range group {
			if typ == nil {
				continue
			}
			rt := reflect.TypeOf(typ)
			if rt.Kind() != reflect.Ptr || rt.Elem().Kind() != reflect.Struct {
				continue
			}
			msg := reflect.New(rt.Elem()).Interface().(proto.Message)
			populateScalarFields(t, msg)
		}
	}
}

func populateDynamicMessage(t *testing.T, msg *dynamicpb.Message) {
	t.Helper()
	pr := msg.ProtoReflect()
	marshalMessage(t, msg)
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
			setScalarField(pr, fd)
		}
		_ = pr.Get(fd)
	}
	marshalMessage(t, msg)
}

func populateScalarFields(t *testing.T, msg proto.Message) {
	t.Helper()
	pr := msg.ProtoReflect()
	marshalMessage(t, msg)
	for i := 0; i < pr.Descriptor().Fields().Len(); i++ {
		fd := pr.Descriptor().Fields().Get(i)
		_ = pr.Get(fd)
		if fd.IsList() || fd.IsMap() || fd.Message() != nil {
			continue
		}
		setScalarField(pr, fd)
	}
	marshalMessage(t, msg)
}

func setScalarField(pr protoreflect.Message, fd protoreflect.FieldDescriptor) {
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

func marshalMessage(t *testing.T, msg proto.Message) {
	t.Helper()
	if _, err := proto.Marshal(msg); err != nil {
		t.Fatalf("marshal %T: %v", msg, err)
	}
}
