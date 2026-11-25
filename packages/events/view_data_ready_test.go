package events

import (
	"testing"

	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestViewDataReadyContract(t *testing.T) {
	registry, err := DefaultRegistry()
	require.NoError(t, err)

	t.Run("old events are not registered", func(t *testing.T) {
		for _, name := range []string{
			"event.storage.view.source_subject.ready",
			"event.storage.view.source_period.ready",
			"event.storage.view.factor_period.ready",
			"event.storage.dataset.period.collected",
		} {
			_, ok := registry.Lookup(name, 1)
			require.False(t, ok, "old event %s must not remain registered", name)
		}
	})

	t.Run("required replacement events are registered", func(t *testing.T) {
		for _, name := range []string{
			"event.storage.dataset.period.completed",
		} {
			_, ok := registry.Lookup(name, 1)
			require.True(t, ok, "required event %s must be registered", name)
		}
		_, ok := registry.Lookup("event.storage.view.data.ready", 2)
		require.True(t, ok, "ViewDataReady v2 must be registered")
		for _, name := range []string{"event.storage.collector.period.completed", "event.storage.dataset.factor_period.computed"} {
			_, ok := registry.Lookup(name, 1)
			require.False(t, ok, "%s 已被 DatasetPeriodCompleted 取代", name)
		}
	})

	now := timestamppb.Now()
	validReady := &storagepb.ViewDataReady{
		CompletionEventId: "collector-completed-1",
		DatasetId:         "dataset_kline_1m",
		Status:            "complete",
		Frequency:         "1m",
		PeriodTime:        1786032000,
		ReadyAt:           now,
	}

	t.Run("missing identity fields are rejected", func(t *testing.T) {
		cases := []struct {
			name   string
			mutate func(*storagepb.ViewDataReady)
		}{
			{name: "completion_event_id", mutate: func(v *storagepb.ViewDataReady) { v.CompletionEventId = "" }},
			{name: "detail_type", mutate: func(v *storagepb.ViewDataReady) { v.Detail = []byte("x") }},
			{name: "dataset_id", mutate: func(v *storagepb.ViewDataReady) { v.DatasetId = "" }},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				invalid := proto.Clone(validReady).(*storagepb.ViewDataReady)
				tc.mutate(invalid)
				_, err := registry.Encode(ViewDataReady, invalid, validationOptions("ready-1", "space", "dataset_kline_1m"))
				require.Error(t, err)
			})
		}
	})

	t.Run("repeated encode keeps a stable event id", func(t *testing.T) {
		opts := validationOptions("ready-stable-1", "space", "dataset_kline_1m")
		first, err := registry.Encode(ViewDataReady, proto.Clone(validReady), opts)
		require.NoError(t, err)
		second, err := registry.Encode(ViewDataReady, proto.Clone(validReady), opts)
		require.NoError(t, err)
		require.Equal(t, "ready-stable-1", first.Message.GetEventId())
		require.Equal(t, first.Message.GetEventId(), second.Message.GetEventId())
	})

	t.Run("failed_subjects rules", func(t *testing.T) {
		withFailed := func(status string, universe, failed []string) *storagepb.ViewDataReady {
			ready := proto.Clone(validReady).(*storagepb.ViewDataReady)
			ready.Status, ready.UniverseSubjectIds, ready.FailedSubjects = status, universe, failed
			return ready
		}
		_, err := registry.Encode(ViewDataReady, withFailed("degraded", []string{"BTC-USDT"}, nil), validationOptions("ready-2", "space", "dataset_kline_1m"))
		require.NoError(t, err, "degraded 可以不带具体的失败标的")
		_, err = registry.Encode(ViewDataReady, withFailed("degraded", []string{"BTC-USDT", "ETH-USDT"}, []string{"ETH-USDT"}), validationOptions("ready-3", "space", "dataset_kline_1m"))
		require.NoError(t, err)

		_, err = registry.Encode(ViewDataReady, withFailed("complete", []string{"ETH-USDT"}, []string{"ETH-USDT"}), validationOptions("ready-4", "space", "dataset_kline_1m"))
		require.ErrorContains(t, err, "complete status has failed_subjects")
		_, err = registry.Encode(ViewDataReady, withFailed("degraded", []string{"BTC-USDT"}, []string{"ETH-USDT"}), validationOptions("ready-5", "space", "dataset_kline_1m"))
		require.ErrorContains(t, err, "not in universe_subject_ids")
		_, err = registry.Encode(ViewDataReady, withFailed("degraded", []string{"ETH-USDT"}, []string{"ETH-USDT", "ETH-USDT"}), validationOptions("ready-6", "space", "dataset_kline_1m"))
		require.ErrorContains(t, err, "duplicated")
	})
}
