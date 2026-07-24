package eventbuspb

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestEventBusManagementContract(t *testing.T) {
	service := EventBusMgrServer_ServiceDesc.ServiceName
	if service != "trpc.moox.eventbus.EventBusMgr" {
		t.Fatalf("service name = %q", service)
	}
	want := map[string]struct{}{
		"GetOverview": {}, "ListEvents": {}, "ListStreams": {}, "ListConsumers": {}, "GetConsumer": {},
	}
	for _, method := range EventBusMgrServer_ServiceDesc.Methods {
		name := method.Name[strings.LastIndex(method.Name, "/")+1:]
		if _, ok := want[name]; !ok {
			t.Fatalf("unexpected method %q", name)
		}
		delete(want, name)
	}
	if len(want) != 0 {
		t.Fatalf("missing methods: %#v", want)
	}
	for _, forbidden := range []string{"Publish", "Send", "Produce"} {
		for _, method := range EventBusMgrServer_ServiceDesc.Methods {
			if strings.HasSuffix(method.Name, "/"+forbidden) {
				t.Fatalf("forbidden method %q exists", forbidden)
			}
		}
	}
	if field := (&EventInfo{}).ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name("event_name")); field == nil {
		t.Fatal("EventInfo.event_name is missing")
	}
}
