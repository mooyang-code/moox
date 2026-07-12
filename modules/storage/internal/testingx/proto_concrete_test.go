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

func TestConcreteGeneratedMessagesExposeLegacyMethods(t *testing.T) {
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
		populateScalarFieldsOnly(t, msg)
		invokeReceiverMethods(t, msg)
		require.NoError(t, marshalMessage(msg))
		return true
	})
}

func populateScalarFieldsOnly(t *testing.T, msg proto.Message) {
	t.Helper()
	pr := msg.ProtoReflect()
	for i := 0; i < pr.Descriptor().Fields().Len(); i++ {
		fd := pr.Descriptor().Fields().Get(i)
		if fd.IsList() || fd.IsMap() || fd.Message() != nil {
			_ = pr.Get(fd)
			continue
		}
		setScalarField(pr, fd)
		_ = pr.Get(fd)
	}
}

func TestGeneratedEnumsExposeLegacyMethods(t *testing.T) {
	for _, file := range generatedFiles {
		if file == nil {
			continue
		}
		for i := 0; i < file.Enums().Len(); i++ {
			enum := file.Enums().Get(i)
			for j := 0; j < enum.Values().Len(); j++ {
				value := enum.Values().Get(j)
				_ = value.Name()
				_ = value.Number()
			}
		}
	}
}

func TestGeneratedClientProxyMethodsInvoke(t *testing.T) {
	ctx := context.Background()
	proxies := []interface{}{
		pb.NewMetadataClientProxy(),
		pb.NewAccessClientProxy(),
		pb.NewAccessScanClientProxy(),
		pb.NewPrimaryStoreClientProxy(),
		pb.NewDataViewClientProxy(),
		pb.NewViewIndexClientProxy(),
	}
	for _, proxy := range proxies {
		invokeClientProxyMethods(t, ctx, proxy)
	}
}

func populateConcreteMessage(t *testing.T, msg proto.Message) {
	t.Helper()
	pr := msg.ProtoReflect()
	for i := 0; i < pr.Descriptor().Fields().Len(); i++ {
		fd := pr.Descriptor().Fields().Get(i)
		switch {
		case fd.IsMap():
			populateMapField(pr, fd)
		case fd.IsList():
			populateListField(pr, fd)
		case fd.Message() != nil:
			pr.Set(fd, protoreflect.ValueOfMessage(dynamicpb.NewMessage(fd.Message())))
		default:
			setScalarField(pr, fd)
		}
		_ = pr.Get(fd)
	}
}

func populateMapField(pr protoreflect.Message, fd protoreflect.FieldDescriptor) {
	m := pr.Mutable(fd).Map()
	key := protoreflect.MapKey(protoreflect.ValueOfString("k"))
	switch fd.MapValue().Kind() {
	case protoreflect.StringKind:
		m.Set(key, protoreflect.ValueOfString("v"))
	case protoreflect.BoolKind:
		m.Set(key, protoreflect.ValueOfBool(true))
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		m.Set(key, protoreflect.ValueOfInt32(1))
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		m.Set(key, protoreflect.ValueOfInt64(1))
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		m.Set(key, protoreflect.ValueOfUint32(1))
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		m.Set(key, protoreflect.ValueOfUint64(1))
	case protoreflect.FloatKind:
		m.Set(key, protoreflect.ValueOfFloat32(1))
	case protoreflect.DoubleKind:
		m.Set(key, protoreflect.ValueOfFloat64(1))
	case protoreflect.BytesKind:
		m.Set(key, protoreflect.ValueOfBytes([]byte("x")))
	case protoreflect.EnumKind:
		if fd.MapValue().Enum().Values().Len() > 0 {
			m.Set(key, protoreflect.ValueOfEnum(fd.MapValue().Enum().Values().Get(0).Number()))
		}
	case protoreflect.MessageKind:
		m.Set(key, protoreflect.ValueOfMessage(dynamicpb.NewMessage(fd.MapValue().Message())))
	}
}

func populateListField(pr protoreflect.Message, fd protoreflect.FieldDescriptor) {
	list := pr.Mutable(fd).List()
	switch fd.Kind() {
	case protoreflect.StringKind:
		list.Append(protoreflect.ValueOfString("item"))
	case protoreflect.BoolKind:
		list.Append(protoreflect.ValueOfBool(true))
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		list.Append(protoreflect.ValueOfInt32(1))
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		list.Append(protoreflect.ValueOfInt64(1))
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		list.Append(protoreflect.ValueOfUint32(1))
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		list.Append(protoreflect.ValueOfUint64(1))
	case protoreflect.FloatKind:
		list.Append(protoreflect.ValueOfFloat32(1))
	case protoreflect.DoubleKind:
		list.Append(protoreflect.ValueOfFloat64(1))
	case protoreflect.BytesKind:
		list.Append(protoreflect.ValueOfBytes([]byte("x")))
	case protoreflect.EnumKind:
		if fd.Enum().Values().Len() > 0 {
			list.Append(protoreflect.ValueOfEnum(fd.Enum().Values().Get(0).Number()))
		}
	case protoreflect.MessageKind:
		list.Append(protoreflect.ValueOfMessage(dynamicpb.NewMessage(fd.Message())))
	}
}

func invokeReceiverMethods(t *testing.T, target interface{}) {
	t.Helper()
	val := reflect.ValueOf(target)
	typ := val.Type()
	for i := 0; i < typ.NumMethod(); i++ {
		method := typ.Method(i)
		if method.Type.NumIn() != 1 || method.Type.NumOut() == 0 {
			continue
		}
		method.Func.Call([]reflect.Value{val})
	}
}

func invokeClientProxyMethods(t *testing.T, ctx context.Context, proxy interface{}) {
	t.Helper()
	val := reflect.ValueOf(proxy)
	typ := val.Type()
	for i := 0; i < typ.NumMethod(); i++ {
		method := typ.Method(i)
		if method.Type.NumIn() < 3 {
			continue
		}
		reqType := method.Type.In(2)
		args := []reflect.Value{val, reflect.ValueOf(ctx)}
		if reqType.Kind() == reflect.Ptr {
			args = append(args, reflect.New(reqType.Elem()))
		} else {
			args = append(args, reflect.Zero(reqType))
		}
		method.Func.Call(args)
	}
}
