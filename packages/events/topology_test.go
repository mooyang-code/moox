package events

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 拓扑声明自洽：每个事件属于一个声明过的 Stream、恰有一个账号发布；消费者名不重复、只属于一个账号、
// 事件同属一个 Stream；工作队列 Stream 至多一个消费者。
func TestTopologyIsConsistent(t *testing.T) {
	registry, err := DefaultRegistry()
	require.NoError(t, err)

	streams := map[string]StreamSpec{}
	for _, stream := range Streams() {
		require.NotContains(t, streams, stream.Name)
		require.Regexp(t, `^MOOX_[A-Z]+$`, stream.Name)
		require.Positive(t, stream.MaxAge, stream.Name)
		require.Positive(t, stream.MaxBytes, stream.Name)
		require.NotEmpty(t, stream.Subjects, "%s 没有事件", stream.Name)
		streams[stream.Name] = stream
	}

	publishers := map[string]string{}
	consumers := map[string]string{}
	usernames, files, keys := map[string]bool{}, map[string]bool{}, map[string]bool{}
	workQueueConsumers := map[string]int{}
	for _, role := range Roles {
		require.False(t, usernames[role.Username], role.Username)
		require.False(t, files[role.File], role.File)
		require.False(t, keys[role.SecretKey], role.SecretKey)
		usernames[role.Username], files[role.File], keys[role.SecretKey] = true, true, true
		require.Regexp(t, `^[a-z0-9-]+$`, role.Username)
		require.Regexp(t, `^[a-z0-9-]+$`, role.File)
		require.Regexp(t, `^eventbus_[a-z0-9_]+$`, role.SecretKey)
		for _, event := range role.Publishes {
			key := eventKey(event)
			require.NotContains(t, publishers, key, "%s 有两个发布账号", key)
			publishers[key] = role.Username
		}
		for _, consumer := range role.Consumers {
			require.NotEmpty(t, consumer.Events(), consumer.name)
			require.NotContains(t, consumers, consumer.name, "消费者 %s 声明了两次", consumer.name)
			consumers[consumer.name] = role.Username
			require.Regexp(t, `^[a-z0-9_]+$`, strings.ReplaceAll(consumer.name, "%s", "x"))
			require.Equal(t, consumer.perSpace, strings.Contains(consumer.name, "%s"), consumer.name)
			for _, event := range consumer.Events() {
				require.Equal(t, consumer.Stream(), event.Stream(), "消费者 %s 的事件不在同一个 Stream", consumer.name)
			}
			stream, ok := streams[consumer.Stream()]
			require.True(t, ok, consumer.Stream())
			if stream.WorkQueue {
				workQueueConsumers[stream.Name]++
			}
		}
	}
	for name, count := range workQueueConsumers {
		require.Equal(t, 1, count, "工作队列 %s 只能有一个消费者", name)
	}
	for _, event := range registry.Events() {
		_, ok := streams[event.Stream()]
		require.True(t, ok, "%s 的 Stream %s 没有声明", event.Name(), event.Stream())
		require.Contains(t, publishers, eventKey(event), "%s 没有发布账号", event.Name())
		require.Contains(t, streams[event.Stream()].Subjects, eventFamily(event))
	}
}

func TestCallbackDispatcherConsumers(t *testing.T) {
	registry, err := DefaultRegistry()
	require.NoError(t, err)
	role, ok := RoleByUsername("callback-dispatcher-eventbus")
	require.True(t, ok)
	require.Len(t, role.Consumers, len(CallbackDispatcherEvents))
	for i, event := range CallbackDispatcherEvents {
		_, ok := registry.Lookup(event.Name(), event.Version())
		require.True(t, ok, event.Name())
		// MOOX_TRADE 是工作队列，不能开放订阅。
		require.NotEqual(t, StreamTrade, event.Stream())
		require.Equal(t, CallbackDispatcherConsumerName(event), role.Consumers[i].Name())
	}
	require.Equal(t, "callback_dispatcher_collector_instrument_delist_scheduled_v1", CallbackDispatcherConsumerName(InstrumentDelistScheduled))
	require.Equal(t, "callback_dispatcher_storage_view_data_ready_v2", CallbackDispatcherConsumerName(ViewDataReady))
}

func TestPerSpaceConsumerNames(t *testing.T) {
	require.Equal(t, "collector_resample_period_crypto_v1", CollectorResamplePeriodConsumer.NameFor("crypto"))
	require.Equal(t, "collector_resample_backfilled_stockhk_v1", CollectorResampleBackfilledConsumer.NameFor("stockhk"))
	require.Panics(t, func() { _ = CollectorResamplePeriodConsumer.Name() })
	require.Equal(t, "factor_collector_period_v1", FactorPeriodConsumer.NameFor("crypto"))
}
