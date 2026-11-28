package events

import (
	"slices"
	"testing"
)

func TestConsumerEventFilterCoversTheWholeFamily(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	cfg := ConsumerConfig{Consumer: StrategyViewReadyConsumer.WithName("c")}
	filters, err := consumerEventFilters(registry, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(filters, []string{"moox.event.storage.view.data.ready.v2.>"}) {
		t.Fatalf("filters = %q", filters)
	}
	transport := jetstreamConsumerConfig(cfg, filters)
	if transport.Stream != "MOOX_STORAGE" || transport.Durable != "c" || transport.FilterSubject != filters[0] || len(transport.FilterSubjects) != 0 {
		t.Fatalf("transport = %+v", transport)
	}
	// WithName 传入空串保持原名，不改动原 DurableConsumer。
	if StrategyViewReadyConsumer.WithName("").Name() != StrategyViewReadyConsumer.Name() {
		t.Fatalf("empty WithName changed name")
	}
	// 多事件消费者使用 FilterSubjects。
	multiCfg := ConsumerConfig{Consumer: MonitorObservabilityConsumer}
	multiFilters, err := consumerEventFilters(registry, multiCfg)
	if err != nil {
		t.Fatal(err)
	}
	multiTransport := jetstreamConsumerConfig(multiCfg, multiFilters)
	if multiTransport.Stream != StreamObservability || multiTransport.FilterSubject != "" || len(multiTransport.FilterSubjects) != 2 {
		t.Fatalf("multiTransport = %+v", multiTransport)
	}
	if _, err := consumerEventFilters(registry, ConsumerConfig{}); err == nil {
		t.Fatal("缺少 DurableConsumer 的消费者配置应被拒绝")
	}
	if _, err := consumerEventFilters(registry, ConsumerConfig{Consumer: declareConsumer("only_name")}); err == nil {
		t.Fatal("缺少事件的消费者配置应被拒绝")
	}
}
