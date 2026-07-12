package testingx

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
)

var generatedFiles = []protoreflect.FileDescriptor{
	pb.File_access_proto,
	pb.File_common_proto,
	pb.File_message_proto,
	pb.File_metadata_proto,
	pb.File_store_proto,
	pb.File_view_proto,
	pb.File_view_index_proto,
}

func TestGeneratedProtoMessagesAreAccessible(t *testing.T) {
	for _, file := range generatedFiles {
		if file == nil {
			continue
		}
		for i := 0; i < file.Messages().Len(); i++ {
			populateDynamicMessage(t, dynamicpb.NewMessage(file.Messages().Get(i)))
		}
	}
	protoregistry.GlobalTypes.RangeMessages(func(mt protoreflect.MessageType) bool {
		if !strings.HasPrefix(string(mt.Descriptor().FullName()), "trpc.moox.storage.") {
			return true
		}
		msg := mt.New().Interface()
		if resetter, ok := msg.(interface{ Reset() }); ok {
			resetter.Reset()
		}
		if stringer, ok := msg.(fmt.Stringer); ok {
			_ = stringer.String()
		}
		_ = msg.ProtoReflect().Descriptor()
		populateScalarFields(t, msg)
		return true
	})
}

func TestUnimplementedStorageRPCsReturnError(t *testing.T) {
	services := []interface{}{
		&pb.UnimplementedMetadata{},
		&pb.UnimplementedAccess{},
		&pb.UnimplementedAccessScan{},
		&pb.UnimplementedPrimaryStore{},
		&pb.UnimplementedDataView{},
		&pb.UnimplementedViewIndex{},
	}
	for _, svc := range services {
		callAllRPCMethods(t, svc)
	}
}

func callAllRPCMethods(t *testing.T, svc interface{}) {
	t.Helper()
	typ := reflect.TypeOf(svc)
	for i := 0; i < typ.NumMethod(); i++ {
		method := typ.Method(i)
		if method.Type.NumIn() != 3 || method.Type.NumOut() != 2 {
			continue
		}
		reqType := method.Type.In(2)
		if reqType.Kind() != reflect.Ptr || reqType.Elem().Kind() != reflect.Struct {
			continue
		}
		req := reflect.New(reqType.Elem())
		out := method.Func.Call([]reflect.Value{reflect.ValueOf(svc), reflect.ValueOf(context.Background()), req})
		err, ok := out[1].Interface().(error)
		require.True(t, ok, "%s should return error", method.Name)
		require.Error(t, err, method.Name)
	}
}

func populateDynamicMessage(t *testing.T, msg *dynamicpb.Message) {
	t.Helper()
	pr := msg.ProtoReflect()
	require.NoError(t, marshalMessage(msg))
	for i := 0; i < pr.Descriptor().Fields().Len(); i++ {
		fd := pr.Descriptor().Fields().Get(i)
		switch {
		case fd.IsMap():
			// Skip map population; getters on empty maps are enough for coverage.
		case fd.IsList() && fd.Kind() == protoreflect.StringKind:
			pr.Mutable(fd).List().Append(protoreflect.ValueOfString("item"))
		case fd.Message() != nil && !fd.IsList() && !fd.IsMap():
			pr.Set(fd, protoreflect.ValueOfMessage(dynamicpb.NewMessage(fd.Message())))
		default:
			setScalarField(pr, fd)
		}
		_ = pr.Get(fd)
	}
	require.NoError(t, marshalMessage(msg))
}

func populateScalarFields(t *testing.T, msg proto.Message) {
	t.Helper()
	pr := msg.ProtoReflect()
	require.NoError(t, marshalMessage(msg))
	for i := 0; i < pr.Descriptor().Fields().Len(); i++ {
		fd := pr.Descriptor().Fields().Get(i)
		_ = pr.Get(fd)
		if fd.IsList() || fd.IsMap() || fd.Message() != nil {
			continue
		}
		setScalarField(pr, fd)
	}
	require.NoError(t, marshalMessage(msg))
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

func marshalMessage(msg proto.Message) error {
	_, err := proto.Marshal(msg)
	return err
}
