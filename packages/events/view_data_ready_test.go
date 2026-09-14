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
			"event.storage.collector.period.completed",
			"event.storage.merge.period.completed",
			"event.storage.view.data.ready",
		} {
			_, ok := registry.Lookup(name, 1)
			require.True(t, ok, "required event %s must be registered", name)
		}
	})

	now := timestamppb.Now()
	validReady := &storagepb.ViewDataReady{
		ViewId:             "view-1",
		ViewConfigId:       "view-config-1",
		CompletionEventId:  "merge-completed-1",
		CompletionKind:     MergePeriodCompleted.Name(),
		DatasetId:          "mdataset_kline_1m",
		Status:             "complete",
		VisibleScope:       "universe:crypto:usdt",
		Frequency:          "1m",
		PeriodTime:         1786032000,
		CommittedPositions: []*storagepb.CommittedPosition{{NodeId: "node-a", StoreId: "store-a", Sequence: 12}},
		ReadyAt:            now,
	}

	t.Run("missing identity fields are rejected", func(t *testing.T) {
		cases := []struct {
			name   string
			mutate func(*storagepb.ViewDataReady)
		}{
			{name: "completion_event_id", mutate: func(v *storagepb.ViewDataReady) { v.CompletionEventId = "" }},
			{name: "completion_kind", mutate: func(v *storagepb.ViewDataReady) { v.CompletionKind = "" }},
			{name: "view_id", mutate: func(v *storagepb.ViewDataReady) { v.ViewId = "" }},
			{name: "dataset_id", mutate: func(v *storagepb.ViewDataReady) { v.DatasetId = "" }},
			{name: "committed_positions", mutate: func(v *storagepb.ViewDataReady) {
				v.CommittedPositions = []*storagepb.CommittedPosition{{NodeId: "node-a", StoreId: "store-a", Sequence: 0}}
			}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				invalid := proto.Clone(validReady).(*storagepb.ViewDataReady)
				tc.mutate(invalid)
				_, err := registry.Encode(ViewDataReady, invalid, validationOptions("ready-1", "space", "view-1"))
				require.Error(t, err)
			})
		}
	})

	t.Run("repeated encode keeps a stable event id", func(t *testing.T) {
		opts := validationOptions("ready-stable-1", "space", "view-1")
		first, err := registry.Encode(ViewDataReady, proto.Clone(validReady), opts)
		require.NoError(t, err)
		second, err := registry.Encode(ViewDataReady, proto.Clone(validReady), opts)
		require.NoError(t, err)
		require.Equal(t, "ready-stable-1", first.Message.GetEventId())
		require.Equal(t, first.Message.GetEventId(), second.Message.GetEventId())
	})

	t.Run("degraded cannot be encoded as complete", func(t *testing.T) {
		invalid := proto.Clone(validReady).(*storagepb.ViewDataReady)
		invalid.Status = "complete"
		invalid.FailedScopeRef = "missing:ETH-USDT"
		_, err := registry.Encode(ViewDataReady, invalid, validationOptions("ready-2", "space", "view-1"))
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed_scope_ref")
	})
}
