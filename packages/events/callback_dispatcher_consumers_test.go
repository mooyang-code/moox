package events

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCallbackDispatcherConsumers(t *testing.T) {
	registry, err := DefaultRegistry()
	require.NoError(t, err)
	names := map[string]bool{}
	for _, event := range CallbackDispatcherEvents {
		_, ok := registry.Lookup(event.Name(), event.Version())
		require.True(t, ok, event.Name())
		// MOOX_TRADE 是工作队列，不能开放订阅。
		require.NotEqual(t, "MOOX_TRADE", event.Stream())
		name := CallbackDispatcherConsumerName(event)
		require.Regexp(t, `^[a-z0-9_]+$`, name)
		require.False(t, names[name], name)
		names[name] = true
	}
	require.Equal(t, "callback_dispatcher_collector_instrument_delist_scheduled_v1", CallbackDispatcherConsumerName(InstrumentDelistScheduled))
	require.Equal(t, "callback_dispatcher_storage_view_data_ready_v2", CallbackDispatcherConsumerName(ViewDataReady))
}
