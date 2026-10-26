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
		Factors:        []*storagepb.FactorPeriodState{{FactorId: "momentum", MemberKey: "momentum", MemberKind: "fixed", Status: "complete", SourceHash: "hash-1", DefinitionHash: "def-1"}},
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
		{name: "complete without enabled factors", mutate: func(v *storagepb.FactorPeriodComputed) { v.Factors = nil }},
		{name: "complete with empty factors", mutate: func(v *storagepb.FactorPeriodComputed) { v.Factors = []*storagepb.FactorPeriodState{} }},
		{name: "empty factors degraded without failures", mutate: func(v *storagepb.FactorPeriodComputed) {
			v.Factors = nil
			v.Status = "degraded"
		}, wantError: true},
		{name: "empty factors complete with failures", mutate: func(v *storagepb.FactorPeriodComputed) {
			v.Factors = nil
			v.FailedSubjects = []string{"ETH"}
		}, wantError: true},
		{name: "empty factors degraded with failures", mutate: func(v *storagepb.FactorPeriodComputed) {
			v.Factors = nil
			v.Status = "degraded"
			v.FailedSubjects = []string{"ETH"}
		}},
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
		{name: "missing definition hash", mutate: func(v *storagepb.FactorPeriodComputed) { v.Factors[0].DefinitionHash = "" }, wantError: true},
		{name: "padded definition hash", mutate: func(v *storagepb.FactorPeriodComputed) { v.Factors[0].DefinitionHash = " def-1" }, wantError: true},
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
		{name: "gap subject within failed subjects", mutate: func(v *storagepb.FactorPeriodComputed) {
			v.Status = "degraded"
			v.Factors[0].Status = "degraded"
			v.Factors[0].FailedSubjects = []string{"ETH"}
			v.Factors[0].GapSubjects = []string{"ETH"}
		}},
		{name: "gap subject outside failed subjects", mutate: func(v *storagepb.FactorPeriodComputed) {
			v.Status = "degraded"
			v.Factors[0].Status = "degraded"
			v.Factors[0].FailedSubjects = []string{"ETH"}
			v.Factors[0].GapSubjects = []string{"BTC"}
		}, wantError: true},
		{name: "duplicate gap subject", mutate: func(v *storagepb.FactorPeriodComputed) {
			v.Status = "degraded"
			v.Factors[0].Status = "degraded"
			v.Factors[0].FailedSubjects = []string{"ETH"}
			v.Factors[0].GapSubjects = []string{"ETH", "ETH"}
		}, wantError: true},
		{name: "unknown factor failed subject", mutate: func(v *storagepb.FactorPeriodComputed) {
			v.Status = "degraded"
			v.Factors[0].Status = "degraded"
			v.Factors[0].FailedSubjects = []string{"unknown"}
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
		{FactorId: "momentum", MemberKey: "momentum", MemberKind: "fixed", Status: "complete", SourceHash: "hash-1", DefinitionHash: "def-1"},
		{FactorId: "rank", MemberKey: "rank", MemberKind: "fixed", Status: "skipped", SourceHash: "hash-2", DefinitionHash: "def-2"},
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

func TestRegistryExcludesRetiredMergeEvent(t *testing.T) {
	registry, err := DefaultRegistry()
	require.NoError(t, err)
	_, ok := registry.Lookup("event.storage."+"merge.period.completed", 1)
	require.False(t, ok)
}

func TestDecodeViewDataReadyEmptyFactors(t *testing.T) {
	registry, err := DefaultRegistry()
	require.NoError(t, err)
	payload := validViewDataReady(timestamppb.Now())
	payload.CompletionKind = FactorPeriodComputed.Name()
	payload.Factors = nil
	encoded, err := registry.Encode(ViewDataReady, payload, validationOptions("ready-event", "space", "view-1"))
	require.NoError(t, err)
	raw, err := proto.Marshal(encoded.Message)
	require.NoError(t, err)
	_, decoded, err := DecodeViewDataReady(registry, raw, encoded.Subject, "ready-event")
	require.NoError(t, err)
	require.Empty(t, decoded.Factors)
	require.Equal(t, "complete", decoded.Status)
}

func TestValidateViewDataReadyFactorFailureUniverse(t *testing.T) {
	registry, err := DefaultRegistry()
	require.NoError(t, err)
	for _, subject := range []string{"ETH", "unknown"} {
		t.Run(subject, func(t *testing.T) {
			payload := validViewDataReady(timestamppb.Now())
			payload.CompletionKind = FactorPeriodComputed.Name()
			payload.Status = "degraded"
			payload.FailedScopeRef = "failed:momentum"
			payload.UniverseSubjectIds = []string{"BTC", "ETH"}
			payload.Factors = []*storagepb.FactorPeriodState{{
				FactorId: "momentum", MemberKey: "momentum", MemberKind: "fixed", Status: "degraded", SourceHash: "hash-1", DefinitionHash: "def-1", FailedSubjects: []string{subject},
			}}
			_, err := registry.Encode(ViewDataReady, payload, validationOptions("ready-event", "space", "view-1"))
			if subject == "unknown" {
				require.ErrorContains(t, err, "not in universe_subject_ids")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// 成员身份是 member_key：同一因子可以用不同参数出现多次；key 重复、缺少 key 或类型未知都拒绝。
func TestFactorStatesKeyedByMemberKey(t *testing.T) {
	registry, err := DefaultRegistry()
	require.NoError(t, err)
	encode := func(states ...*storagepb.FactorPeriodState) error {
		payload := validViewDataReady(timestamppb.Now())
		payload.CompletionKind = FactorPeriodComputed.Name()
		payload.Factors = states
		_, err := registry.Encode(ViewDataReady, payload, validationOptions("ready-event", "space", "view-1"))
		return err
	}
	state := func(key, kind string) *storagepb.FactorPeriodState {
		return &storagepb.FactorPeriodState{FactorId: "boll", MemberKey: key, MemberKind: kind, Status: "complete", SourceHash: "h", DefinitionHash: "d"}
	}
	require.NoError(t, encode(state("boll__n_20", "fixed"), state("boll__var", "var")))
	require.ErrorContains(t, encode(state("boll__var", "var"), state("boll__var", "var")), "duplicated")
	require.ErrorContains(t, encode(state("", "fixed")), "identity is invalid")
	require.ErrorContains(t, encode(state("boll__var", "guessed")), "member_kind")
}
