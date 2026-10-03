package events

import (
	"testing"

	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func factorPeriodStatePayload() *storagepb.FactorPeriodComputed {
	return &storagepb.FactorPeriodComputed{
		DatasetId: "result", SourceDatasetId: "source", Frequency: "1m", PeriodTime: 1786032000,
		Status: "complete", UniverseSubjectIds: []string{"BTC", "ETH"},
		Factors:        []*storagepb.FactorPeriodState{{FactorId: "momentum", Status: "complete", SourceHash: "hash-1"}},
		TriggerEventId: "collector-event", ComputedAt: timestamppb.Now(),
	}
}

func TestValidateFactorPeriodComputedRequiresFactorStates(t *testing.T) {
	registry, err := DefaultRegistry()
	require.NoError(t, err)
	tests := []struct {
		name      string
		mutate    func(*storagepb.FactorPeriodComputed)
		wantError bool
	}{
		{name: "complete"},
		{name: "missing factors", mutate: func(v *storagepb.FactorPeriodComputed) { v.Factors = nil }, wantError: true},
		{name: "nil factor", mutate: func(v *storagepb.FactorPeriodComputed) { v.Factors[0] = nil }, wantError: true},
		{name: "missing factor id", mutate: func(v *storagepb.FactorPeriodComputed) { v.Factors[0].FactorId = "" }, wantError: true},
		{name: "missing source dataset", mutate: func(v *storagepb.FactorPeriodComputed) { v.SourceDatasetId = "" }, wantError: true},
		{name: "invalid aggregate status", mutate: func(v *storagepb.FactorPeriodComputed) { v.Status = "skipped" }, wantError: true},
		{name: "invalid factor status", mutate: func(v *storagepb.FactorPeriodComputed) { v.Factors[0].Status = "failed" }, wantError: true},
		{name: "empty factor status", mutate: func(v *storagepb.FactorPeriodComputed) { v.Factors[0].Status = "" }, wantError: true},
		{name: "skipped factor", mutate: func(v *storagepb.FactorPeriodComputed) {
			v.Status = "degraded"
			v.Factors[0].Status = "skipped"
		}},
		{name: "degraded factor", mutate: func(v *storagepb.FactorPeriodComputed) {
			v.Status = "degraded"
			v.Factors[0].Status = "degraded"
			v.Factors[0].FailedSubjects = []string{"ETH"}
		}},
		{name: "upstream failure", mutate: func(v *storagepb.FactorPeriodComputed) {
			v.Status = "degraded"
			v.FailedSubjects = []string{"ETH"}
		}},
		{name: "duplicate factor", mutate: func(v *storagepb.FactorPeriodComputed) {
			v.Factors = append(v.Factors, proto.Clone(v.Factors[0]).(*storagepb.FactorPeriodState))
		}, wantError: true},
		{name: "missing source hash", mutate: func(v *storagepb.FactorPeriodComputed) { v.Factors[0].SourceHash = "" }, wantError: true},
		{name: "complete aggregate with failed subjects", mutate: func(v *storagepb.FactorPeriodComputed) { v.FailedSubjects = []string{"ETH"} }, wantError: true},
		{name: "complete aggregate with skipped factor", mutate: func(v *storagepb.FactorPeriodComputed) { v.Factors[0].Status = "skipped" }, wantError: true},
		{name: "complete factor with failed subjects", mutate: func(v *storagepb.FactorPeriodComputed) {
			v.Status = "degraded"
			v.Factors[0].FailedSubjects = []string{"ETH"}
		}, wantError: true},
		{name: "unknown failed subject", mutate: func(v *storagepb.FactorPeriodComputed) {
			v.Status = "degraded"
			v.FailedSubjects = []string{"unknown"}
		}, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := factorPeriodStatePayload()
			if tt.mutate != nil {
				tt.mutate(payload)
			}
			_, err := registry.Encode(FactorPeriodComputed, payload, validationOptions("factor-event", "space", "result"))
			if tt.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestDecodeViewDataReadyFactorStates(t *testing.T) {
	registry, err := DefaultRegistry()
	require.NoError(t, err)
	payload := validViewDataReady(timestamppb.Now())
	payload.CompletionKind = FactorPeriodComputed.Name()
	payload.Factors = []*storagepb.FactorPeriodState{
		{FactorId: "momentum", Status: "complete", SourceHash: "hash-1"},
		{FactorId: "rank", Status: "skipped", SourceHash: "hash-2"},
	}
	payload.Status = "degraded"
	payload.FailedScopeRef = "failed:rank"
	encoded, err := registry.Encode(ViewDataReady, payload, validationOptions("ready-event", "space", "view-1"))
	require.NoError(t, err)
	raw, err := proto.Marshal(encoded.Message)
	require.NoError(t, err)
	_, decoded, err := DecodeViewDataReady(registry, raw, encoded.Subject, "ready-event")
	require.NoError(t, err)
	require.Len(t, decoded.Factors, len(payload.Factors))
	for i, factor := range payload.Factors {
		require.True(t, proto.Equal(factor, decoded.Factors[i]), "factor %d changed during decode", i)
	}

	for _, kind := range []string{"event.storage.merge.period.completed", "factor_period.computed"} {
		payload.CompletionKind = kind
		_, err := registry.Encode(ViewDataReady, payload, validationOptions("ready-event", "space", "view-1"))
		require.Error(t, err)
	}
}

func TestRegistryHasNoMergePeriodCompleted(t *testing.T) {
	registry, err := DefaultRegistry()
	require.NoError(t, err)
	_, ok := registry.Lookup("event.storage.merge.period.completed", 1)
	require.False(t, ok)
}
