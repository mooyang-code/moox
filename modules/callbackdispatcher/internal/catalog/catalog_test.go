package catalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"gopkg.in/yaml.v3"
)

// 目录里的每个事件都必须在注册表里声明过，而且所属的流不是工作队列（再挂消费者会和原消费者抢消息）。
func TestCatalogEventsAreRegisteredAndNotOnWorkQueueStreams(t *testing.T) {
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)

	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "eventbus", "config", "app.yaml"))
	require.NoError(t, err)
	var topology struct {
		Streams []struct {
			Name      string `yaml:"name"`
			Retention string `yaml:"retention"`
		} `yaml:"streams"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &topology))
	retention := map[string]string{}
	for _, stream := range topology.Streams {
		retention[stream.Name] = stream.Retention
	}

	require.NotEmpty(t, Subscribable())
	for _, entry := range Subscribable() {
		_, ok := registry.Lookup(entry.Event.Name(), entry.Event.Version())
		require.True(t, ok, entry.Name())
		require.NotEmpty(t, retention[entry.Event.Stream()], "%s 所属的流 %s 不在 EventBus 配置里", entry.Name(), entry.Event.Stream())
		require.NotEqual(t, "work_queue", retention[entry.Event.Stream()], "%s 所属的流是工作队列，不能开放订阅", entry.Name())
		require.NotEmpty(t, entry.Title)
		require.NotEmpty(t, entry.Fields)
		require.NotEmpty(t, entry.Sample)
	}
}

// 目录和凭据生成用的 durable 表必须是同一批事件，否则缺权限的 durable 创建不出来。
func TestCatalogMatchesDispatcherDurableTable(t *testing.T) {
	want := map[string]bool{}
	for _, event := range events.CallbackDispatcherEvents {
		want[event.Name()] = true
	}
	got := map[string]bool{}
	for _, entry := range Subscribable() {
		got[entry.Name()] = true
	}
	require.Equal(t, want, got)
}

func TestScopeValue(t *testing.T) {
	delist, ok := Lookup(events.InstrumentDelistScheduled.Name())
	require.True(t, ok)
	message := &eventpb.EventMessage{SpaceId: "mooxsys", SubjectId: "binance:SWAP:FOOUSDT"}
	for key, want := range map[string]string{FieldExchange: "binance", FieldMarket: "SWAP", FieldExchangeSymbol: "FOOUSDT", FieldSpaceID: "mooxsys", FieldSubject: "binance:SWAP:FOOUSDT"} {
		got, ok := delist.ScopeValue(message, key)
		require.True(t, ok, key)
		require.Equal(t, want, got, key)
	}
	_, ok = delist.ScopeValue(&eventpb.EventMessage{SubjectId: "broken"}, FieldExchange)
	require.False(t, ok)
	require.False(t, delist.HasField(FieldSubject))
	require.True(t, delist.HasField(FieldExchange))

	period, _ := Lookup(events.DatasetPeriodCompleted.Name())
	require.True(t, period.HasField(FieldSubject))
	require.False(t, period.HasField(FieldExchange))
	_, ok = Lookup("event.unknown")
	require.False(t, ok)
}

// 字段说明覆盖 payload 的每个 proto 字段（不多不少），说明不为空；示例能按真实推送的写法（protojson、proto 字段名）解码。
func TestPayloadFieldsAndSampleMatchProto(t *testing.T) {
	for _, entry := range Subscribable() {
		fields := entry.Event.NewPayload().ProtoReflect().Descriptor().Fields()
		protoNames := map[string]bool{}
		for i := 0; i < fields.Len(); i++ {
			protoNames[string(fields.Get(i).Name())] = true
		}
		described := map[string]bool{}
		for _, field := range entry.PayloadFields {
			require.NotEmpty(t, field.Title, "%s.%s", entry.Name(), field.Key)
			described[field.Key] = true
		}
		require.Equal(t, protoNames, described, "%s 的字段说明要覆盖全部 proto 字段", entry.Name())

		for key := range entry.Sample {
			require.True(t, protoNames[key], "%s 的示例字段 %s 要用 proto 字段名", entry.Name(), key)
		}
		raw, err := json.Marshal(entry.Sample)
		require.NoError(t, err)
		require.NoError(t, protojson.Unmarshal(raw, entry.Event.NewPayload()), "%s 的示例要能按 protojson 解码", entry.Name())
	}
}
