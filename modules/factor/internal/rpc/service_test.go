package rpc

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

func TestCreateFactorWithoutSetSucceeds(t *testing.T) {
	catalog := &catalogFake{}
	svc := nativeFactorManager(t, NewService(catalog, &recalcFake{}))

	for i, serialization := range []int{codec.SerializationTypePB, codec.SerializationTypeJSON} {
		rsp, err := svc.CreateFactor(context.Background(), &factorpb.CreateFactorReq{
			Factor: &factorpb.FactorDef{FactorId: "rolling_mean", Name: "Rolling mean"},
		}, client.WithSerializationType(serialization))
		require.NoError(t, err)
		require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
		require.Equal(t, i+1, catalog.createFactorCalls)
		require.Equal(t, "rolling_mean", rsp.GetFactor().GetFactorId())
	}
}

func nativeFactorManager(t *testing.T, service *Service) factorpb.FactorMgrClientProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	rpc := server.New(server.WithListener(listener), server.WithAddress(listener.Addr().String()), server.WithNetwork("tcp"), server.WithProtocol("trpc"), server.WithTransport(transport.NewServerTransport()))
	factorpb.RegisterFactorMgrService(rpc, service)
	done := make(chan error, 1)
	go func() { done <- rpc.Serve() }()
	t.Cleanup(func() {
		require.NoError(t, rpc.Close(nil))
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("FactorMgr native listener did not stop")
		}
	})
	return factorpb.NewFactorMgrClientProxy(client.WithTarget("ip://"+listener.Addr().String()), client.WithNetwork("tcp"), client.WithProtocol("trpc"), client.WithTimeout(3*time.Second), client.WithTransport(transport.NewClientTransport()), client.WithDisableConnectionPool())
}

func TestCreateFactorRequiresFactorID(t *testing.T) {
	catalog := &catalogFake{}
	svc := NewService(catalog, &recalcFake{})
	rsp, err := svc.CreateFactor(context.Background(), &factorpb.CreateFactorReq{Factor: &factorpb.FactorDef{Name: "x"}})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Zero(t, catalog.createFactorCalls)
}

func TestUpdateFactorAndDeleteFactorSurfaceCatalogConflicts(t *testing.T) {
	catalog := &catalogFake{err: errors.New("factor is enabled in set fset_a; disable it before updating its definition")}
	svc := NewService(catalog, &recalcFake{})

	updated, err := svc.UpdateFactor(context.Background(), &factorpb.UpdateFactorReq{Factor: &factorpb.FactorDef{FactorId: "f"}})
	require.NoError(t, err)
	require.NotEqual(t, commonpb.ErrorCode_SUCCESS, updated.GetRetInfo().GetCode())
	require.Contains(t, updated.GetRetInfo().GetMsg(), "disable it")

	catalog.err = errors.New("factor \"f\" is still used by factor set \"fset_a\"")
	deleted, err := svc.DeleteFactor(context.Background(), &factorpb.DeleteFactorReq{FactorId: "f"})
	require.NoError(t, err)
	require.NotEqual(t, commonpb.ErrorCode_SUCCESS, deleted.GetRetInfo().GetCode())
	require.Contains(t, deleted.GetRetInfo().GetMsg(), "still used")
}

func TestListFactorsWithoutSetReturnsAllWithUsages(t *testing.T) {
	catalog := &catalogFake{infos: []domain.FactorInfo{
		{Factor: domain.FactorDef{FactorID: "a", SourceCode: "src-a", SourceHash: "h-a"}, Usages: []domain.FactorUsage{{SetID: "s1", Status: "enabled"}, {SetID: "s2", Status: "disabled"}}},
		{Factor: domain.FactorDef{FactorID: "b", SourceCode: "src-b", SourceHash: "h-b"}},
	}}
	svc := NewService(catalog, &recalcFake{})

	rsp, err := svc.ListFactors(context.Background(), &factorpb.ListFactorsReq{})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, "", catalog.listSetID)
	require.Len(t, rsp.GetFactors(), 2)
	require.Equal(t, "a", rsp.GetFactors()[0].GetFactor().GetFactorId())
	require.Len(t, rsp.GetFactors()[0].GetUsages(), 2)
	require.Equal(t, "s2", rsp.GetFactors()[0].GetUsages()[1].GetSetId())
	require.Empty(t, rsp.GetFactors()[1].GetUsages())
}

func TestListFactorsWithSetFiltersByMemberStatus(t *testing.T) {
	catalog := &catalogFake{}
	svc := NewService(catalog, &recalcFake{})

	_, err := svc.ListFactors(context.Background(), &factorpb.ListFactorsReq{SetId: "fset_a", Status: "enabled"})
	require.NoError(t, err)
	require.Equal(t, "fset_a", catalog.listSetID)
	require.Equal(t, "enabled", catalog.listStatus)
}

func TestListFactorsOmitsSourceByDefault(t *testing.T) {
	catalog := &catalogFake{infos: []domain.FactorInfo{{Factor: domain.FactorDef{FactorID: "a", SourceCode: "src-a", SourceHash: "h-a"}}}}
	svc := NewService(catalog, &recalcFake{})

	rsp, err := svc.ListFactors(context.Background(), &factorpb.ListFactorsReq{})
	require.NoError(t, err)
	require.Empty(t, rsp.GetFactors()[0].GetFactor().GetSourceCode())
	require.Equal(t, "h-a", rsp.GetFactors()[0].GetFactor().GetSourceHash())
}

func TestListFactorsIncludeSourceWhenRequested(t *testing.T) {
	catalog := &catalogFake{infos: []domain.FactorInfo{{Factor: domain.FactorDef{FactorID: "a", SourceCode: "src-a"}}}}
	svc := NewService(catalog, &recalcFake{})

	rsp, err := svc.ListFactors(context.Background(), &factorpb.ListFactorsReq{IncludeSource: true})
	require.NoError(t, err)
	require.Equal(t, "src-a", rsp.GetFactors()[0].GetFactor().GetSourceCode())
}

func TestGetFactorAlwaysIncludesSourceAndUsages(t *testing.T) {
	catalog := &catalogFake{infos: []domain.FactorInfo{{
		Factor: domain.FactorDef{FactorID: "a", SourceCode: "src-a", SourceHash: "h-a"},
		Usages: []domain.FactorUsage{{SetID: "s1", Status: "enabled"}},
	}}}
	svc := NewService(catalog, &recalcFake{})

	rsp, err := svc.GetFactor(context.Background(), &factorpb.GetFactorReq{FactorId: "a"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, "src-a", rsp.GetFactor().GetSourceCode())
	require.Len(t, rsp.GetUsages(), 1)
	require.Equal(t, "enabled", rsp.GetUsages()[0].GetStatus())
}

func TestListFactorSetsMembersOmitSource(t *testing.T) {
	catalog := &catalogFake{
		set: domain.FactorSet{SetID: "fset_a", Status: domain.SetStatusEnabled},
		members: []domain.SetMember{{
			FactorSetMember: domain.FactorSetMember{SetID: "fset_a", FactorID: "a", Status: domain.MemberStatusEnabled},
			Factor:          domain.FactorDef{FactorID: "a", SourceCode: "src-a", SourceHash: "h-a"},
		}},
		sets: []domain.FactorSet{{SetID: "fset_a", Status: domain.SetStatusEnabled}},
	}
	svc := NewService(catalog, &recalcFake{})

	list, err := svc.ListFactorSets(context.Background(), &factorpb.ListFactorSetsReq{})
	require.NoError(t, err)
	require.Len(t, list.GetFactorSets()[0].GetMembers(), 1)
	member := list.GetFactorSets()[0].GetMembers()[0]
	require.Equal(t, "enabled", member.GetStatus())
	require.Empty(t, member.GetFactor().GetSourceCode())
	require.Equal(t, "h-a", member.GetFactor().GetSourceHash())

	got, err := svc.GetFactorSet(context.Background(), &factorpb.GetFactorSetReq{SetId: "fset_a"})
	require.NoError(t, err)
	require.Len(t, got.GetMembers(), 1)
	require.Empty(t, got.GetMembers()[0].GetFactor().GetSourceCode())
}

func TestAddFactorToSetAndRemoveSurfaceCatalogErrors(t *testing.T) {
	catalog := &catalogFake{err: errors.New("factor set is not ready")}
	svc := NewService(catalog, &recalcFake{})

	added, err := svc.AddFactorToSet(context.Background(), &factorpb.AddFactorToSetReq{SetId: "fset_a", FactorId: "a"})
	require.NoError(t, err)
	require.NotEqual(t, commonpb.ErrorCode_SUCCESS, added.GetRetInfo().GetCode())
	require.Contains(t, added.GetRetInfo().GetMsg(), "not ready")

	missing, err := svc.AddFactorToSet(context.Background(), &factorpb.AddFactorToSetReq{SetId: "fset_a"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, missing.GetRetInfo().GetCode())

	catalog.err = errors.New("member is enabled; disable it before removal")
	removed, err := svc.RemoveFactorFromSet(context.Background(), &factorpb.RemoveFactorFromSetReq{SetId: "fset_a", FactorId: "a"})
	require.NoError(t, err)
	require.NotEqual(t, commonpb.ErrorCode_SUCCESS, removed.GetRetInfo().GetCode())

	catalog.err = nil
	ok, err := svc.AddFactorToSet(context.Background(), &factorpb.AddFactorToSetReq{SetId: "fset_a", FactorId: "a"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, ok.GetRetInfo().GetCode())
	require.Equal(t, "fset_a", ok.GetMember().GetSetId())
	require.Equal(t, "disabled", ok.GetMember().GetStatus())
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
		"CreateFactor", "UpdateFactor", "DeleteFactor", "GetFactor", "ListFactors",
		"AddFactorToSet", "RemoveFactorFromSet", "SetFactorMemberStatus",
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
	sets              []domain.FactorSet
	members           []domain.SetMember
	infos             []domain.FactorInfo
	createFactorCalls int
	listSetID         string
	listStatus        string
	err               error
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
func (f *catalogFake) GetSet(context.Context, string) (domain.FactorSet, []domain.SetMember, error) {
	if f.set.SetID == "" {
		return domain.FactorSet{}, nil, errors.New("set not found")
	}
	return f.set, f.members, nil
}
func (f *catalogFake) ListSets(context.Context) ([]domain.FactorSet, error) { return f.sets, nil }
func (f *catalogFake) CreateFactor(_ context.Context, in domain.FactorDef) (domain.FactorDef, error) {
	f.createFactorCalls++
	return in, f.err
}
func (f *catalogFake) UpdateFactor(_ context.Context, in domain.FactorDef) (domain.FactorDef, error) {
	return in, f.err
}
func (f *catalogFake) DeleteFactor(context.Context, string) error { return f.err }
func (f *catalogFake) GetFactor(context.Context, string) (domain.FactorInfo, error) {
	if len(f.infos) == 0 {
		return domain.FactorInfo{}, errors.New("factor not found")
	}
	return f.infos[0], nil
}
func (f *catalogFake) ListFactors(_ context.Context, setID, status string) ([]domain.FactorInfo, error) {
	f.listSetID, f.listStatus = setID, status
	return f.infos, nil
}
func (f *catalogFake) AddFactorToSet(_ context.Context, setID, factorID string) (domain.SetMember, error) {
	if f.err != nil {
		return domain.SetMember{}, f.err
	}
	return domain.SetMember{
		FactorSetMember: domain.FactorSetMember{SetID: setID, FactorID: factorID, Status: domain.MemberStatusDisabled},
		Factor:          domain.FactorDef{FactorID: factorID},
	}, nil
}
func (f *catalogFake) RemoveFactorFromSet(context.Context, string, string) error { return f.err }
func (f *catalogFake) SetFactorMemberStatus(_ context.Context, setID, factorID, status string) (domain.SetMember, string, error) {
	member := domain.SetMember{
		FactorSetMember: domain.FactorSetMember{SetID: setID, FactorID: factorID, Status: status},
		Factor:          domain.FactorDef{FactorID: factorID},
	}
	if status == domain.MemberStatusEnabled {
		return member, "factor-enable-" + factorID, nil
	}
	return member, "", nil
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

func TestSetFactorMemberStatusReturnsBackfillJobOnEnable(t *testing.T) {
	svc := NewService(&catalogFake{}, &recalcFake{})

	enabled, err := svc.SetFactorMemberStatus(context.Background(), &factorpb.SetFactorMemberStatusReq{SetId: "fset_a", FactorId: "momentum", Status: domain.MemberStatusEnabled})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, enabled.GetRetInfo().GetCode())
	require.Equal(t, "factor-enable-momentum", enabled.GetBackfillJob().GetJobId())
	require.Equal(t, "enabled", enabled.GetMember().GetStatus())
	require.Equal(t, "fset_a", enabled.GetMember().GetSetId())

	disabled, err := svc.SetFactorMemberStatus(context.Background(), &factorpb.SetFactorMemberStatusReq{SetId: "fset_a", FactorId: "momentum", Status: domain.MemberStatusDisabled})
	require.NoError(t, err)
	require.Nil(t, disabled.GetBackfillJob())

	invalid, err := svc.SetFactorMemberStatus(context.Background(), &factorpb.SetFactorMemberStatusReq{SetId: "fset_a", FactorId: "momentum", Status: "bogus"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, invalid.GetRetInfo().GetCode())
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
