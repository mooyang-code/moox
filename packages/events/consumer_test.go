package events

import (
	"testing"
)

func TestConsumerEventFilterCoversTheWholeFamily(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	cfg := ConsumerConfig{Name: "c", Event: ViewDataReady}
	filter, err := consumerEventFilter(registry, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if filter != "moox.event.storage.view.data.ready.v2.>" {
		t.Fatalf("filter = %q", filter)
	}
	transport := jetstreamConsumerConfig(cfg, filter)
	if transport.Stream != "MOOX_STORAGE" || transport.FilterSubject != filter || len(transport.FilterSubjects) != 0 {
		t.Fatalf("transport = %+v", transport)
	}
	if _, err := consumerEventFilter(registry, ConsumerConfig{}); err == nil {
		t.Fatal("没有 Event 的消费者配置应被拒绝")
	}
}
