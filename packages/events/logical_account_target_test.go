package events

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestLogicalAccountTargetWeightRequiresModernIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*tradeeventpb.LogicalAccountTargetWeightRequested)
	}{
		{name: "target", mutate: func(value *tradeeventpb.LogicalAccountTargetWeightRequested) { value.TargetId = "" }},
		{name: "logical account", mutate: func(value *tradeeventpb.LogicalAccountTargetWeightRequested) { value.LogicalAccountId = "" }},
		{name: "instance", mutate: func(value *tradeeventpb.LogicalAccountTargetWeightRequested) { value.InstanceId = "" }},
		{name: "session", mutate: func(value *tradeeventpb.LogicalAccountTargetWeightRequested) { value.SessionId = "" }},
		{name: "strategy", mutate: func(value *tradeeventpb.LogicalAccountTargetWeightRequested) { value.StrategyId = "" }},
		{name: "bar end", mutate: func(value *tradeeventpb.LogicalAccountTargetWeightRequested) { value.BarEndTime = nil }},
		{name: "effective at", mutate: func(value *tradeeventpb.LogicalAccountTargetWeightRequested) { value.EffectiveAt = nil }},
		{name: "valid until", mutate: func(value *tradeeventpb.LogicalAccountTargetWeightRequested) { value.ValidUntil = nil }},
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
	// This was accepted by the previous validator and then authorized by the
	// Trade consumer through runner_id/command_sequence. It must no longer be
	// a publishable automatic-execution event.
	payload := &tradeeventpb.LogicalAccountTargetWeightRequested{
		TargetId:         "target-1",
		RunnerId:         "runner-1",
		LogicalAccountId: "logical-1",
		CommandSequence:  1,
		Targets: []*tradeeventpb.InstrumentWeightTarget{{
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

	payload, err := proto.Marshal(&tradeeventpb.LogicalAccountTargetRequested{
		TargetId:         "target-1",
		RunnerId:         "runner-1",
		LogicalAccountId: "logical-1",
		CommandSequence:  1,
	})
	require.NoError(t, err)
	message, err := proto.Marshal(&eventpb.EventMessage{
		EventId:      "target-1",
		EventName:    "event.trade.target.requested",
		EventVersion: 1,
		SpaceId:      "space",
		SubjectId:    "logical-1",
		OccurredAt:   timestamppb.New(time.Unix(1_700_000_000, 0).UTC()),
		Payload:      payload,
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

func validLogicalAccountTargetWeight() *tradeeventpb.LogicalAccountTargetWeightRequested {
	now := time.Unix(1_700_000_000, 0).UTC()
	bar := timestamppb.New(now)
	return &tradeeventpb.LogicalAccountTargetWeightRequested{
		TargetId:         "target-1",
		InstanceId:       "instance-1",
		SessionId:        "session-1",
		StrategyId:       "strategy-1",
		LogicalAccountId: "logical-1",
		BarEndTime:       bar,
		EffectiveAt:      bar,
		ValidUntil:       timestamppb.New(now.Add(time.Hour)),
		Targets: []*tradeeventpb.InstrumentWeightTarget{{
			InstrumentId: "BTC-USDT-SPOT",
			TargetWeight: "1.25",
		}},
	}
}
