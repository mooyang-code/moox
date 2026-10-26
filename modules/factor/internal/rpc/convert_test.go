package rpc

import (
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/factorwire"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/stretchr/testify/require"
)

func TestConvertFactorDefRoundTrip(t *testing.T) {
	createdAt := time.Date(2026, time.October, 4, 10, 11, 12, 0, time.UTC)
	updatedAt := createdAt.Add(time.Minute)
	want := domain.MustDefinition("rolling_mean", "Rolling mean", "params:\n  window: {type: int, min: 2, max: 3, default: 3}\noutputs:\n  mean_close: cs_rank(ts_mean(close - open, window))\n")
	want.AllowPartialUniverse, want.CreatedAt, want.UpdatedAt = true, createdAt, updatedAt

	pb := factorwire.DefToPB(want)
	require.Equal(t, domain.FactorTypeCrossSection, pb.GetFactorType(), "派生字段只读地带给调用方")
	require.Equal(t, []string{"close", "open"}, pb.GetInputColumns())
	require.Equal(t, int32(3), pb.GetLookbackPeriods())
	require.Equal(t, domain.DefinitionHash(want), pb.GetDefinitionHash())
	require.Equal(t, []*factorpb.FactorParamDecl{{Name: "window", Type: "int", Min: 2, Max: 3, DefaultValue: 3}}, pb.GetParams())
	got, err := factorwire.DefFromPB(pb)

	require.NoError(t, err)
	requireSameDefinition(t, want, got)
}

// 派生字段不从请求读取：调用方伪造的类型、输入列、回看会被编译结果覆盖。
func TestDefFromPBIgnoresClientDerivedFields(t *testing.T) {
	pb := factorwire.DefToPB(domain.MustDefinition("f", "F", "outputs:\n  f: ts_mean(close, 5)\n"))
	pb.FactorType, pb.InputColumns, pb.LookbackPeriods, pb.Outputs = "cross_section", []string{"volume"}, 99, []string{"x"}
	got, err := factorwire.DefFromPB(pb)
	require.NoError(t, err)
	require.Equal(t, domain.FactorTypeTimeSeries, got.FactorType)
	require.Equal(t, []string{"close"}, got.InputColumns)
	require.Equal(t, []string{"f"}, got.Outputs)
	require.Equal(t, 5, got.LookbackPeriods)
}

// 接收方编译不了时保留发送方的派生字段，组装才能清掉这些输出列上的旧值。
func TestDefFromPBKeepsSenderDerivedFieldsWhenUncompilable(t *testing.T) {
	pb := &factorpb.FactorDef{FactorId: "f", Name: "F", Spec: "outputs:\n  f: future_op(close)\n",
		FactorType: "timeseries", InputColumns: []string{"close"}, Outputs: []string{"f"}, LookbackPeriods: 7}
	got, err := factorwire.DefFromPB(pb)
	require.NoError(t, err)
	require.Nil(t, got.Compiled())
	require.Equal(t, []string{"f"}, got.Outputs)
	require.Equal(t, 7, got.LookbackPeriods)
	require.Equal(t, domain.FactorTypeTimeSeries, got.FactorType)
}

// requireSameDefinition 比较定义的全部导出字段（编译结果是内部指针，不参与比较）。
func requireSameDefinition(t *testing.T, want, got domain.FactorDef) {
	t.Helper()
	require.Equal(t, want.FactorID, got.FactorID)
	require.Equal(t, want.Name, got.Name)
	require.Equal(t, want.Spec, got.Spec)
	require.Equal(t, want.SpecHash, got.SpecHash)
	require.Equal(t, want.AllowPartialUniverse, got.AllowPartialUniverse)
	require.True(t, want.CreatedAt.Equal(got.CreatedAt) && want.UpdatedAt.Equal(got.UpdatedAt))
	require.Equal(t, want.FactorType, got.FactorType)
	require.Equal(t, want.InputColumns, got.InputColumns)
	require.Equal(t, want.Outputs, got.Outputs)
	require.Equal(t, want.LookbackPeriods, got.LookbackPeriods)
	require.NotNil(t, got.Compiled())
}

func TestConvertMemberRoundTrip(t *testing.T) {
	createdAt := time.Date(2026, time.October, 4, 10, 11, 12, 0, time.UTC)
	def := domain.MustDefinition("rolling_mean", "Rolling mean", "outputs:\n  mean_close: cs_rank(ts_mean(close, 3))\n")
	def.AllowPartialUniverse, def.CreatedAt, def.UpdatedAt = true, createdAt, createdAt
	member := domain.TaskMember{
		FactorTaskMember: domain.FactorTaskMember{
			TaskID: "ftask_a", FactorID: "rolling_mean", Status: domain.MemberStatusEnabled,
			CreatedAt: createdAt, UpdatedAt: createdAt.Add(time.Minute),
		},
		Factor: def,
	}

	full := memberToPB(member, true)
	require.Equal(t, "ftask_a", full.GetTaskId())
	require.Equal(t, "rolling_mean", full.GetFactorId())
	require.Equal(t, domain.MemberStatusEnabled, full.GetStatus())
	require.Equal(t, "2026-10-04T10:12:12Z", full.GetUpdatedAt())
	require.Equal(t, def.Spec, full.GetFactor().GetSpec())
	got, err := factorwire.DefFromPB(full.GetFactor())
	require.NoError(t, err)
	requireSameDefinition(t, def, got)

	listed := memberToPB(member, false)
	require.Empty(t, listed.GetFactor().GetSpec())
	require.Equal(t, def.SpecHash, listed.GetFactor().GetSpecHash())
	require.Equal(t, []string{"mean_close"}, listed.GetFactor().GetOutputs(), "列表里仍带派生字段")
}

func TestConvertFactorTaskRoundTrip(t *testing.T) {
	createdAt := time.Date(2026, time.October, 4, 10, 11, 12, 0, time.UTC)
	want := domain.FactorTask{
		TaskID:          "ftask_binance_kline_1m",
		SpaceID:         "crypto",
		SourceDatasetID: "dataset_binance_kline_1m",
		Freq:            "1m",

		ResultDatasetID: "dataset_factor_binance_kline_1m",
		Status:          domain.TaskStatusEnabled,
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
		TaskID:       "ftask_binance_kline_1m",
		Members:      []string{"rolling_mean"},
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

func recalcJobFromPB(pb *factorpb.RecalcJob) (RecalcJob, error) {
	if pb == nil {
		return RecalcJob{}, fmt.Errorf("recalc job is required")
	}
	var job RecalcJob
	job.JobID = pb.GetJobId()
	job.RequestID = pb.GetRequestId()
	job.TaskID = pb.GetTaskId()
	job.Members = factorwire.CloneStrings(pb.GetMembers())
	job.Subjects = factorwire.CloneStrings(pb.GetSubjects())
	job.Status = pb.GetStatus()
	job.Error = pb.GetError()
	job.EngineID = pb.GetEngineId()
	timeFields := []struct {
		name   string
		value  string
		target *time.Time
	}{
		{name: "start_time", value: pb.GetStartTime(), target: &job.StartTime},
		{name: "end_time", value: pb.GetEndTime(), target: &job.EndTime},
		{name: "progress_time", value: pb.GetProgressTime(), target: &job.ProgressTime},
		{name: "created_at", value: pb.GetCreatedAt(), target: &job.CreatedAt},
		{name: "updated_at", value: pb.GetUpdatedAt(), target: &job.UpdatedAt},
	}
	for _, field := range timeFields {
		parsed, err := factorwire.ParseTime(field.value)
		if err != nil {
			return RecalcJob{}, fmt.Errorf("%s: %w", field.name, err)
		}
		*field.target = parsed
	}
	return job, nil
}
