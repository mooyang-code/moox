package events

import (
	"strings"
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

func TestSpaceConsumerFilterOnlyIncludesOneSpace(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	got, err := spaceConsumerFilter(registry, SpaceConsumerConfig{
		ConsumerConfig: ConsumerConfig{Event: ViewDataReady},
		SpaceID:        "crypto",
	})
	if err != nil {
		t.Fatal(err)
	}
	want, err := registry.SpacePattern(ViewDataReady, "crypto")
	if err != nil {
		t.Fatal(err)
	}
	if got != want || !strings.HasSuffix(got, ".>") {
		t.Fatalf("filter = %q, want %q", got, want)
	}
}

func TestSpaceConsumerFilterRejectsEmptySpaceID(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	_, err = spaceConsumerFilter(registry, SpaceConsumerConfig{ConsumerConfig: ConsumerConfig{Event: ViewDataReady}})
	if err == nil || !strings.Contains(err.Error(), "space_id") {
		t.Fatalf("error = %v, want space_id validation", err)
	}
}
