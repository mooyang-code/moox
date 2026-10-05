package rpc

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/factorwire"
	"github.com/stretchr/testify/require"
)

func TestConvertFactorDefRoundTrip(t *testing.T) {
	createdAt := time.Date(2026, time.October, 4, 10, 11, 12, 0, time.UTC)
	updatedAt := createdAt.Add(time.Minute)
	want := domain.FactorDef{
		FactorID:             "rolling_mean",
		Name:                 "Rolling mean",
		FactorType:           domain.FactorTypeTimeSeries,
		SourceCode:           "def calculate(frame, context): return frame",
		SourceHash:           domain.SourceHash("def calculate(frame, context): return frame"),
		InputColumns:         []string{"close", "open"},
		Outputs:              []string{"mean_close"},
		ParamsJSON:           `{"window":3}`,
		LookbackPeriods:      3,
		AllowPartialUniverse: true,
		CreatedAt:            createdAt,
		UpdatedAt:            updatedAt,
	}

	got, err := factorwire.DefFromPB(factorwire.DefToPB(want))

	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestConvertMemberRoundTrip(t *testing.T) {
	createdAt := time.Date(2026, time.October, 4, 10, 11, 12, 0, time.UTC)
	def := domain.FactorDef{
		FactorID: "rolling_mean", Name: "Rolling mean", FactorType: domain.FactorTypeCrossSection,
		SourceCode: "code", SourceHash: domain.SourceHash("code"), InputColumns: []string{"close"},
		Outputs: []string{"mean_close"}, ParamsJSON: "{}", LookbackPeriods: 3, AllowPartialUniverse: true,
		CreatedAt: createdAt, UpdatedAt: createdAt,
	}
	member := domain.SetMember{
		FactorSetMember: domain.FactorSetMember{
			SetID: "fset_a", FactorID: "rolling_mean", Status: domain.MemberStatusEnabled,
			CreatedAt: createdAt, UpdatedAt: createdAt.Add(time.Minute),
		},
		Factor: def,
	}

	full := memberToPB(member, true)
	require.Equal(t, "fset_a", full.GetSetId())
	require.Equal(t, "rolling_mean", full.GetFactorId())
	require.Equal(t, domain.MemberStatusEnabled, full.GetStatus())
	require.Equal(t, "2026-10-04T10:12:12Z", full.GetUpdatedAt())
	require.Equal(t, "code", full.GetFactor().GetSourceCode())
	got, err := factorwire.DefFromPB(full.GetFactor())
	require.NoError(t, err)
	require.Equal(t, def, got)

	listed := memberToPB(member, false)
	require.Empty(t, listed.GetFactor().GetSourceCode())
	require.Equal(t, domain.SourceHash("code"), listed.GetFactor().GetSourceHash())
}

func TestConvertFactorSetRoundTrip(t *testing.T) {
	createdAt := time.Date(2026, time.October, 4, 10, 11, 12, 0, time.UTC)
	want := domain.FactorSet{
		SetID:           "fset_binance_kline_1m",
		SpaceID:         "crypto",
		SourceDatasetID: "dataset_binance_kline_1m",
		Freq:            "1m",
		SubjectMode:     domain.SubjectModeInclude,
		Subjects:        []string{"BTCUSDT", "ETHUSDT"},
		ResultDatasetID: "dataset_factor_binance_kline_1m",
		Status:          domain.SetStatusEnabled,
		CreatedAt:       createdAt,
		UpdatedAt:       createdAt.Add(time.Minute),
	}

	got, err := factorwire.SetFromPB(factorwire.SetToPB(want))

	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestConvertRecalcJobRoundTrip(t *testing.T) {
	start := time.Date(2026, time.October, 4, 10, 0, 0, 0, time.UTC)
	want := RecalcJob{
		JobID:        "request-1",
		RequestID:    "request-1",
		SetID:        "fset_binance_kline_1m",
		FactorIDs:    []string{"rolling_mean"},
		Subjects:     []string{"BTCUSDT"},
		StartTime:    start,
		EndTime:      start.Add(time.Hour),
		Status:       "accepted",
		ProgressTime: start.Add(time.Minute),
		CreatedAt:    start,
		UpdatedAt:    start.Add(time.Minute),
	}

	got, err := recalcJobFromPB(recalcJobToPB(want))

	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestFactorDefPBDoesNotExposeSourcePath(t *testing.T) {
	got := factorwire.DefToPB(domain.FactorDef{
		FactorID: "rolling_mean",
	})

	require.Nil(t, got.ProtoReflect().Descriptor().Fields().ByName("source_path"))
	require.Equal(t, "rolling_mean", got.GetFactorId())
}
