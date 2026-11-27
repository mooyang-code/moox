package adminpb

import (
	"reflect"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestSecretDoesNotImplementMasking(t *testing.T) {
	if _, ok := any(&Secret{}).(interface{ Masking() }); ok {
		t.Fatal("*Secret must not implement Masking(); secret redaction happens in SecretMgr RPC")
	}
	if _, ok := reflect.TypeOf(&Secret{}).MethodByName("Masking"); ok {
		t.Fatal("*Secret must not expose a Masking method")
	}
	if _, ok := reflect.TypeOf(&SecretMaterial{}).MethodByName("Masking"); ok {
		t.Fatal("*SecretMaterial must not expose a Masking method")
	}
}

func TestSecretMaterialUsesContiguousWireTags(t *testing.T) {
	fields := (&SecretMaterial{}).ProtoReflect().Descriptor().Fields()
	want := map[string]int32{
		"secret_id": 1, "name": 2, "description": 3, "category": 4,
		"provider": 5, "secret_type": 6, "key_id": 7, "secret_value": 8,
		"extra_config": 9, "status": 10,
	}
	for name, number := range want {
		field := fields.ByName(protoreflect.Name(name))
		if field == nil || int32(field.Number()) != number {
			t.Fatalf("field %s wire tag = %v, want %d", name, field, number)
		}
	}
}
