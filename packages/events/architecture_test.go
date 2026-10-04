package events

import (
	"reflect"
	"testing"

	"github.com/mooyang-code/moox/packages/events/eventpb"
)

func TestEventContractArchitecture(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := (&eventpb.EventMessage{}).ProtoReflect().Descriptor().Fields().Len(); got != 7 {
		t.Fatalf("EventMessage fields = %d, want 7", got)
	}
	typ := reflect.TypeOf(Event{})
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).PkgPath == "" {
			t.Fatalf("Event field %s must remain private", typ.Field(i).Name)
		}
	}
}
