package rpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
)

func TestCreateFactorRequiresSetID(t *testing.T) {
	catalog := &catalogFake{}
	svc := NewService(catalog, &recalcFake{})

	rsp, err := svc.CreateFactor(context.Background(), &factorpb.CreateFactorReq{
		Factor: &factorpb.FactorDef{FactorId: "rolling_mean", Name: "Rolling mean"},
	})

	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "set_id")
	require.Zero(t, catalog.createFactorCalls)
}

func TestRecalcFactorsValidatesTimeRange(t *testing.T) {
	start := "2026-10-04T10:00:00Z"
	end := "2026-10-04T10:02:00Z"
	unaligned := "2026-10-04T10:02:01Z"

	tests := []struct {
		name    string
		start   string
		end     string
		message string
	}{
		{name: "end before start", start: end, end: start, message: "end_time"},
		{name: "end equals start", start: start, end: start, message: "end_time"},
		{name: "unaligned end", start: start, end: unaligned, message: "aligned"},
		{name: "unaligned start", start: "2026-10-04T10:00:01Z", end: end, message: "aligned"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			catalog := &catalogFake{set: domain.FactorSet{
				SetID:           "fset_binance_kline_1m",
				SpaceID:         "crypto",
				SourceDatasetID: "dataset_binance_kline_1m",
				Freq:            "1m",
				SubjectMode:     domain.SubjectModeAll,
				ResultDatasetID: "dataset_factor_binance_kline_1m",
				Status:          domain.SetStatusEnabled,
			}}
			recalc := &recalcFake{}
			svc := NewService(catalog, recalc)

			rsp, err := svc.RecalcFactors(context.Background(), &factorpb.RecalcFactorsReq{
				SetId:     catalog.set.SetID,
				StartTime: tt.start,
				EndTime:   tt.end,
				RequestId: "request-1",
			})

			require.NoError(t, err)
			require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
			require.Contains(t, rsp.GetRetInfo().GetMsg(), tt.message)
			require.Zero(t, recalc.submitCalls)
		})
	}
}

func TestRecalcFactorsSubmitsAlignedRange(t *testing.T) {
	catalog := &catalogFake{set: domain.FactorSet{
		SetID:           "fset_binance_kline_1m",
		SpaceID:         "crypto",
		SourceDatasetID: "dataset_binance_kline_1m",
		Freq:            "1m",
		SubjectMode:     domain.SubjectModeAll,
		ResultDatasetID: "dataset_factor_binance_kline_1m",
		Status:          domain.SetStatusEnabled,
	}}
	recalc := &recalcFake{}
	svc := NewService(catalog, recalc)

	rsp, err := svc.RecalcFactors(context.Background(), &factorpb.RecalcFactorsReq{
		SetId:     catalog.set.SetID,
		FactorIds: []string{"rolling_mean"},
		Subjects:  []string{"BTCUSDT"},
		StartTime: "2026-10-04T10:00:00+00:00",
		EndTime:   "2026-10-04T10:05:00Z",
		RequestId: "request-1",
	})

	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, 1, recalc.submitCalls)
	require.Equal(t, "fset_binance_kline_1m", recalc.setID)
	require.Equal(t, []string{"rolling_mean"}, recalc.factorIDs)
	require.Equal(t, []string{"BTCUSDT"}, recalc.subjects)
	require.Equal(t, "request-1", recalc.requestID)
	require.Equal(t, time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC), recalc.start)
	require.Equal(t, time.Date(2026, 10, 4, 10, 5, 0, 0, time.UTC), recalc.end)
	require.Equal(t, "request-1", rsp.GetJob().GetJobId())
}

func TestFactorMgrExposesOnlyNewContract(t *testing.T) {
	service := factorpb.File_factor_proto.Services().ByName("FactorMgr")
	require.NotNil(t, service)

	want := []string{
		"CreateFactorSet", "UpdateFactorSet", "SetFactorSetStatus", "DeleteFactorSet", "GetFactorSet", "ListFactorSets",
		"CreateFactor", "UpdateFactor", "SetFactorStatus", "DeleteFactor", "GetFactor", "ListFactors",
		"RecalcFactors", "ListRecalcJobs", "GetRecalcJob", "CancelRecalcJob", "GetStatus",
	}
	got := make([]string, 0, service.Methods().Len())
	for i := 0; i < service.Methods().Len(); i++ {
		got = append(got, string(service.Methods().Get(i).Name()))
	}
	require.ElementsMatch(t, want, got)
}

type catalogFake struct {
	set               domain.FactorSet
	factors           []domain.FactorDef
	createFactorCalls int
}

func (f *catalogFake) CreateSet(_ context.Context, in domain.FactorSet) (domain.FactorSet, error) {
	f.set = in
	return in, nil
}
func (f *catalogFake) UpdateSetSubjects(_ context.Context, _, mode string, subjects []string) (domain.FactorSet, error) {
	f.set.SubjectMode, f.set.Subjects = mode, subjects
	return f.set, nil
}
func (f *catalogFake) SetSetStatus(_ context.Context, _, status string) (domain.FactorSet, error) {
	f.set.Status = status
	return f.set, nil
}
func (f *catalogFake) DeleteSet(context.Context, string, bool) error { return nil }
func (f *catalogFake) GetSet(context.Context, string) (domain.FactorSet, []domain.FactorDef, error) {
	if f.set.SetID == "" {
		return domain.FactorSet{}, nil, errors.New("set not found")
	}
	return f.set, f.factors, nil
}
func (f *catalogFake) ListSets(context.Context) ([]domain.FactorSet, error) { return nil, nil }
func (f *catalogFake) CreateFactor(_ context.Context, in domain.FactorDef) (domain.FactorDef, error) {
	f.createFactorCalls++
	return in, nil
}
func (f *catalogFake) UpdateFactor(_ context.Context, in domain.FactorDef) (domain.FactorDef, error) {
	return in, nil
}
func (f *catalogFake) SetFactorStatus(_ context.Context, factorID, status string) (domain.FactorDef, string, error) {
	if status == domain.FactorStatusEnabled {
		return domain.FactorDef{FactorID: factorID, Status: status}, "factor-enable-" + factorID, nil
	}
	return domain.FactorDef{FactorID: factorID, Status: status}, "", nil
}
func (f *catalogFake) DeleteFactor(context.Context, string) error { return nil }
func (f *catalogFake) GetFactor(context.Context, string) (domain.FactorDef, error) {
	return domain.FactorDef{}, errors.New("factor not found")
}
func (f *catalogFake) ListFactors(context.Context, string, string) ([]domain.FactorDef, error) {
	return f.factors, nil
}

type recalcFake struct {
	submitCalls int
	jobs        []RecalcJob
	listSetID   string
	listStatus  []string
	job         RecalcJob
	setID       string
	factorIDs   []string
	subjects    []string
	requestID   string
	start       time.Time
	end         time.Time
}

func (f *recalcFake) Submit(_ context.Context, setID string, factorIDs, subjects []string, requestID string, start, end time.Time) (RecalcJob, error) {
	f.submitCalls++
	f.setID, f.factorIDs, f.subjects, f.requestID, f.start, f.end = setID, factorIDs, subjects, requestID, start, end
	f.job = RecalcJob{JobID: requestID, RequestID: requestID, SetID: setID, FactorIDs: factorIDs, Subjects: subjects, StartTime: start, EndTime: end}
	return f.job, nil
}
func (f *recalcFake) List(_ context.Context, setID string, statuses []string) ([]RecalcJob, error) {
	f.listSetID, f.listStatus = setID, statuses
	return f.jobs, nil
}
func (f *recalcFake) Get(_ context.Context, jobID string) (RecalcJob, error) {
	if f.job.JobID == "" {
		return RecalcJob{JobID: jobID}, nil
	}
	return f.job, nil
}
func (f *recalcFake) Cancel(context.Context, string) (RecalcJob, error) {
	return f.job, nil
}

func TestSetFactorStatusReturnsBackfillJobOnEnable(t *testing.T) {
	svc := NewService(&catalogFake{}, &recalcFake{})

	enabled, err := svc.SetFactorStatus(context.Background(), &factorpb.SetFactorStatusReq{FactorId: "momentum", Status: domain.FactorStatusEnabled})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, enabled.GetRetInfo().GetCode())
	require.Equal(t, "factor-enable-momentum", enabled.GetBackfillJob().GetJobId())

	disabled, err := svc.SetFactorStatus(context.Background(), &factorpb.SetFactorStatusReq{FactorId: "momentum", Status: domain.FactorStatusDisabled})
	require.NoError(t, err)
	require.Nil(t, disabled.GetBackfillJob())
}

func TestListRecalcJobsFiltersBySetAndPaginates(t *testing.T) {
	recalc := &recalcFake{jobs: []RecalcJob{{JobID: "job-3"}, {JobID: "job-2"}, {JobID: "job-1"}}}
	svc := NewService(&catalogFake{}, recalc)

	missing, err := svc.ListRecalcJobs(context.Background(), &factorpb.ListRecalcJobsReq{})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, missing.GetRetInfo().GetCode())

	rsp, err := svc.ListRecalcJobs(context.Background(), &factorpb.ListRecalcJobsReq{
		SetId: "fset_a", Statuses: []string{"running"}, Page: &commonpb.Page{Page: 1, Size: 2},
	})
	require.NoError(t, err)
	require.Equal(t, "fset_a", recalc.listSetID)
	require.Equal(t, []string{"running"}, recalc.listStatus)
	require.Len(t, rsp.GetJobs(), 2)
	require.EqualValues(t, 3, rsp.GetPageResult().GetTotal())
	require.True(t, rsp.GetPageResult().GetHasMore())
}
