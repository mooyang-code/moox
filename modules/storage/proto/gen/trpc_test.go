package storagepb

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestConcreteMessagesExposeLegacyProtoMethods(t *testing.T) {
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
			if resetter, ok := msg.(interface{ Reset() }); ok {
				resetter.Reset()
			}
			if stringer, ok := msg.(fmt.Stringer); ok {
				_ = stringer.String()
			}
			_ = msg.ProtoReflect().Descriptor()
			_, err := proto.Marshal(msg)
			require.NoError(t, err)
		}
	}
}

func TestUnimplementedServiceMethodsReturnError(t *testing.T) {
	services := []interface{}{
		&UnimplementedMetadata{},
		&UnimplementedAccess{},
		&UnimplementedAccessScan{},
		&UnimplementedPrimaryStore{},
		&UnimplementedDataView{},
		&UnimplementedViewIndex{},
	}
	for _, svc := range services {
		t.Run(reflect.TypeOf(svc).Elem().Name(), func(t *testing.T) {
			callAllRPCMethods(t, svc)
		})
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
