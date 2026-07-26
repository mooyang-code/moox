package adminpb

import (
	"context"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"trpc.group/trpc-go/trpc-filter/masking"
)

func TestMaskingFilterRedactsNormalSecretResponses(t *testing.T) {
	filter := masking.ServerFilter()
	rsp, err := filter(context.Background(), nil, func(context.Context, interface{}) (interface{}, error) {
		return &GetSecretRsp{Secret: &Secret{SecretValue: "plain-sensitive-value"}}, nil
	})
	if err != nil {
		t.Fatalf("filter error = %v", err)
	}
	got := rsp.(*GetSecretRsp).GetSecret().GetSecretValue()
	if got == "plain-sensitive-value" || got == "" {
		t.Fatalf("secret was not safely masked: %q", got)
	}
}

func TestMaskingFilterPreservesExplicitRevealResponse(t *testing.T) {
	filter := masking.ServerFilter()
	rsp, err := filter(context.Background(), nil, func(context.Context, interface{}) (interface{}, error) {
		return &RevealSecretRsp{Secret: &RevealedSecret{SecretValue: "plain-sensitive-value"}}, nil
	})
	if err != nil {
		t.Fatalf("filter error = %v", err)
	}
	if got := rsp.(*RevealSecretRsp).GetSecret().GetSecretValue(); got != "plain-sensitive-value" {
		t.Fatalf("reveal response was modified: %q", got)
	}
}

func TestRevealedSecretUsesContiguousWireTags(t *testing.T) {
	fields := (&RevealedSecret{}).ProtoReflect().Descriptor().Fields()
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
