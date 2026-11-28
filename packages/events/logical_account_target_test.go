package events

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestLogicalAccountTargetWeightRequiresModernIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*eventpb.LogicalAccountTargetWeightRequested)
	}{
		{name: "target", mutate: func(value *eventpb.LogicalAccountTargetWeightRequested) { value.TargetId = "" }},
		{name: "logical account", mutate: func(value *eventpb.LogicalAccountTargetWeightRequested) { value.LogicalAccountId = "" }},
		{name: "instance", mutate: func(value *eventpb.LogicalAccountTargetWeightRequested) { value.InstanceId = "" }},
		{name: "session", mutate: func(value *eventpb.LogicalAccountTargetWeightRequested) { value.SessionId = "" }},
		{name: "strategy", mutate: func(value *eventpb.LogicalAccountTargetWeightRequested) { value.StrategyId = "" }},
		{name: "session started at missing", mutate: func(value *eventpb.LogicalAccountTargetWeightRequested) { value.SessionStartedAt = 0 }},
		{name: "session started at negative", mutate: func(value *eventpb.LogicalAccountTargetWeightRequested) { value.SessionStartedAt = -1 }},
		{name: "bar end", mutate: func(value *eventpb.LogicalAccountTargetWeightRequested) { value.BarEndTime = 0 }},
		{name: "valid until", mutate: func(value *eventpb.LogicalAccountTargetWeightRequested) { value.ValidUntil = 0 }},
		{name: "market missing", mutate: func(value *eventpb.LogicalAccountTargetWeightRequested) { value.Targets[0].MarketType = "" }},
		{name: "market unknown", mutate: func(value *eventpb.LogicalAccountTargetWeightRequested) { value.Targets[0].MarketType = "FUTURES" }},
		{name: "market uppercase", mutate: func(value *eventpb.LogicalAccountTargetWeightRequested) { value.Targets[0].MarketType = "SPOT" }},
		{name: "duplicate instrument and market", mutate: func(value *eventpb.LogicalAccountTargetWeightRequested) {
			value.Targets = append(value.Targets, &eventpb.InstrumentWeightTarget{InstrumentId: "BTC-USDT", TargetWeight: "0.1", MarketType: "spot"})
		}},
	}
	registry, err := DefaultRegistry()
	require.NoError(t, err)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := validLogicalAccountTargetWeight()
			test.mutate(payload)
			_, err := registry.Encode(
				LogicalAccountTargetWeightRequested,
				payload,
				validationOptions("target-1", "space", "logical-1"),
			)
			require.Error(t, err)
		})
	}
}

func TestLogicalAccountTargetWeightRejectsLegacyRunnerAndSequenceFallback(t *testing.T) {
	registry, err := DefaultRegistry()
	require.NoError(t, err)
	// Target and account identity alone cannot authorize automatic execution.
	payload := &eventpb.LogicalAccountTargetWeightRequested{
		TargetId:         "target-1",
		LogicalAccountId: "logical-1",
		Targets: []*eventpb.InstrumentWeightTarget{{
			InstrumentId: "BTC-USDT-SPOT",
			TargetWeight: "1",
		}},
	}
	_, err = registry.Encode(
		LogicalAccountTargetWeightRequested,
		payload,
		validationOptions("target-1", "space", "logical-1"),
	)
	require.Error(t, err)
}

func TestLegacyQuantityTargetIsNotRegistered(t *testing.T) {
	registry, err := DefaultRegistry()
	require.NoError(t, err)

	message, err := proto.Marshal(&eventpb.EventMessage{
		EventId:      "target-1",
		EventName:    "event.trade.target.requested",
		EventVersion: 1,
		SpaceId:      "space",
		SubjectId:    "logical-1",
		OccurredAt:   time.Unix(1_700_000_000, 0).UTC().UnixMilli(),
		Payload:      []byte("unregistered target payload"),
	})
	require.NoError(t, err)

	_, _, err = DecodeRaw(registry, message, "moox.event.trade.target.requested.v1.onygcy3f.nrxwo2ldmfwc2mi", "target-1", ContentType)
	require.EqualError(t, err, "event event.trade.target.requested@1 is not registered")
}

func TestLogicalAccountTargetWeightSubjectUsesLogicalAccountID(t *testing.T) {
	registry, err := DefaultRegistry()
	require.NoError(t, err)
	encoded, err := registry.Encode(
		LogicalAccountTargetWeightRequested,
		validLogicalAccountTargetWeight(),
		validationOptions("target-1", "space", "logical-1"),
	)
	require.NoError(t, err)
	want, err := registry.RenderSubject(
		LogicalAccountTargetWeightRequested,
		"space",
		"logical-1",
	)
	require.NoError(t, err)
	require.Equal(t, want, encoded.Subject)
}

func validLogicalAccountTargetWeight() *eventpb.LogicalAccountTargetWeightRequested {
	now := time.Unix(1_700_000_000, 0).UTC()
	bar := now.UnixMilli()
	return &eventpb.LogicalAccountTargetWeightRequested{
		TargetId:         "target-1",
		InstanceId:       "instance-1",
		SessionId:        "session-1",
		SessionStartedAt: now.Add(-time.Hour).UnixMilli(),
		StrategyId:       "strategy-1",
		LogicalAccountId: "logical-1",
		BarEndTime:       bar,
		ValidUntil:       now.Add(time.Hour).UnixMilli(),
		Targets: []*eventpb.InstrumentWeightTarget{{
			InstrumentId: "BTC-USDT",
			TargetWeight: "1.25",
			MarketType:   "spot",
		}},
	}
}

func TestLogicalAccountTargetWeightAllowsSameInstrumentInSpotAndSwap(t *testing.T) {
	registry, err := DefaultRegistry()
	require.NoError(t, err)
	payload := validLogicalAccountTargetWeight()
	payload.Targets = append(payload.Targets, &eventpb.InstrumentWeightTarget{
		InstrumentId: "BTC-USDT", TargetWeight: "-0.5", MarketType: "swap",
	})
	_, err = registry.Encode(
		LogicalAccountTargetWeightRequested,
		payload,
		validationOptions("target-1", "space", "logical-1"),
	)
	require.NoError(t, err)
}
