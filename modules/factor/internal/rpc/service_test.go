package rpc

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/catalog"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/server"
	"trpc.group/trpc-go/trpc-go/transport"
)

func TestCreateFactorWithoutTaskSucceeds(t *testing.T) {
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
	catalog := &catalogFake{err: errors.New("factor is enabled in set ftask_a; disable it before updating its definition")}
	svc := NewService(catalog, &recalcFake{})

	updated, err := svc.UpdateFactor(context.Background(), &factorpb.UpdateFactorReq{Factor: &factorpb.FactorDef{FactorId: "f"}})
	require.NoError(t, err)
	require.NotEqual(t, commonpb.ErrorCode_SUCCESS, updated.GetRetInfo().GetCode())
	require.Contains(t, updated.GetRetInfo().GetMsg(), "disable it")

	catalog.err = errors.New("factor \"f\" is still used by factor task \"ftask_a\"")
	deleted, err := svc.DeleteFactor(context.Background(), &factorpb.DeleteFactorReq{FactorId: "f"})
	require.NoError(t, err)
	require.NotEqual(t, commonpb.ErrorCode_SUCCESS, deleted.GetRetInfo().GetCode())
	require.Contains(t, deleted.GetRetInfo().GetMsg(), "still used")
}

func TestListFactorsWithoutTaskReturnsAllWithUsages(t *testing.T) {
	catalog := &catalogFake{infos: []domain.FactorInfo{
		{Factor: domain.FactorDef{FactorID: "a", Spec: "src-a", SpecHash: "h-a"}, Usages: []domain.FactorUsage{{TaskID: "s1", Status: "enabled"}, {TaskID: "s2", Status: "disabled"}}},
		{Factor: domain.FactorDef{FactorID: "b", Spec: "src-b", SpecHash: "h-b"}},
	}}
	svc := NewService(catalog, &recalcFake{})

	rsp, err := svc.ListFactors(context.Background(), &factorpb.ListFactorsReq{})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, "", catalog.listTaskID)
	require.Len(t, rsp.GetFactors(), 2)
	require.Equal(t, "a", rsp.GetFactors()[0].GetFactor().GetFactorId())
	require.Len(t, rsp.GetFactors()[0].GetUsages(), 2)
	require.Equal(t, "s2", rsp.GetFactors()[0].GetUsages()[1].GetTaskId())
	require.Empty(t, rsp.GetFactors()[1].GetUsages())
}

func TestListFactorsWithTaskFiltersByMemberStatus(t *testing.T) {
	catalog := &catalogFake{}
	svc := NewService(catalog, &recalcFake{})

	_, err := svc.ListFactors(context.Background(), &factorpb.ListFactorsReq{TaskId: "ftask_a", Status: "enabled"})
	require.NoError(t, err)
	require.Equal(t, "ftask_a", catalog.listTaskID)
	require.Equal(t, "enabled", catalog.listStatus)
}

func TestListFactorsOmitsSourceByDefault(t *testing.T) {
	catalog := &catalogFake{infos: []domain.FactorInfo{{Factor: domain.FactorDef{FactorID: "a", Spec: "src-a", SpecHash: "h-a"}}}}
	svc := NewService(catalog, &recalcFake{})

	rsp, err := svc.ListFactors(context.Background(), &factorpb.ListFactorsReq{})
	require.NoError(t, err)
	require.Empty(t, rsp.GetFactors()[0].GetFactor().GetSpec())
	require.Equal(t, "h-a", rsp.GetFactors()[0].GetFactor().GetSpecHash())
}

func TestListFactorsIncludeSpecWhenRequested(t *testing.T) {
	catalog := &catalogFake{infos: []domain.FactorInfo{{Factor: domain.FactorDef{FactorID: "a", Spec: "src-a"}}}}
	svc := NewService(catalog, &recalcFake{})

	rsp, err := svc.ListFactors(context.Background(), &factorpb.ListFactorsReq{IncludeSpec: true})
	require.NoError(t, err)
	require.Equal(t, "src-a", rsp.GetFactors()[0].GetFactor().GetSpec())
}

func TestGetFactorAlwaysIncludesSourceAndUsages(t *testing.T) {
	catalog := &catalogFake{infos: []domain.FactorInfo{{
		Factor: domain.FactorDef{FactorID: "a", Spec: "src-a", SpecHash: "h-a"},
		Usages: []domain.FactorUsage{{TaskID: "s1", Status: "enabled"}},
	}}}
	svc := NewService(catalog, &recalcFake{})

	rsp, err := svc.GetFactor(context.Background(), &factorpb.GetFactorReq{FactorId: "a"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, "src-a", rsp.GetFactor().GetSpec())
	require.Len(t, rsp.GetUsages(), 1)
	require.Equal(t, "enabled", rsp.GetUsages()[0].GetStatus())
}

func TestListFactorTasksMembersOmitSource(t *testing.T) {
	catalog := &catalogFake{
		set: domain.FactorTask{TaskID: "ftask_a", Status: domain.TaskStatusEnabled},
		members: []domain.TaskMember{{
			FactorTaskMember: domain.FactorTaskMember{TaskID: "ftask_a", MemberKey: "a", FactorID: "a", Status: domain.MemberStatusEnabled},
			Factor:           domain.FactorDef{FactorID: "a", Spec: "src-a", SpecHash: "h-a"},
		}},
		sets: []domain.FactorTask{{TaskID: "ftask_a", Status: domain.TaskStatusEnabled}},
	}
	svc := NewService(catalog, &recalcFake{})

	list, err := svc.ListFactorTasks(context.Background(), &factorpb.ListFactorTasksReq{})
	require.NoError(t, err)
	require.Len(t, list.GetFactorTasks()[0].GetMembers(), 1)
	member := list.GetFactorTasks()[0].GetMembers()[0]
	require.Equal(t, "enabled", member.GetStatus())
	require.Empty(t, member.GetFactor().GetSpec())
	require.Equal(t, "h-a", member.GetFactor().GetSpecHash())

	got, err := svc.GetFactorTask(context.Background(), &factorpb.GetFactorTaskReq{TaskId: "ftask_a"})
	require.NoError(t, err)
	require.Len(t, got.GetMembers(), 1)
	require.Empty(t, got.GetMembers()[0].GetFactor().GetSpec())
}

func TestAddFactorToTaskAndRemoveSurfaceCatalogErrors(t *testing.T) {
	catalog := &catalogFake{err: errors.New("factor task is not ready")}
	svc := NewService(catalog, &recalcFake{})

	added, err := svc.AddTaskMember(context.Background(), &factorpb.AddTaskMemberReq{TaskId: "ftask_a", FactorId: "a"})
	require.NoError(t, err)
	require.NotEqual(t, commonpb.ErrorCode_SUCCESS, added.GetRetInfo().GetCode())
	require.Contains(t, added.GetRetInfo().GetMsg(), "not ready")

	missing, err := svc.AddTaskMember(context.Background(), &factorpb.AddTaskMemberReq{TaskId: "ftask_a"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, missing.GetRetInfo().GetCode())

	catalog.err = errors.New("member is enabled; disable it before removal")
	removed, err := svc.RemoveTaskMember(context.Background(), &factorpb.RemoveTaskMemberReq{TaskId: "ftask_a", MemberKey: "a"})
	require.NoError(t, err)
	require.NotEqual(t, commonpb.ErrorCode_SUCCESS, removed.GetRetInfo().GetCode())

	catalog.err = nil
	ok, err := svc.AddTaskMember(context.Background(), &factorpb.AddTaskMemberReq{TaskId: "ftask_a", FactorId: "a"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, ok.GetRetInfo().GetCode())
	require.Equal(t, "ftask_a", ok.GetMember().GetTaskId())
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
			catalog := &catalogFake{set: domain.FactorTask{
				TaskID:          "ftask_binance_kline_1m",
				SpaceID:         "crypto",
				SourceDatasetID: "dataset_binance_kline_1m",
				Freq:            "1m",

				ResultDatasetID: "dataset_factor_binance_kline_1m",
				Status:          domain.TaskStatusEnabled,
			}}
			recalc := &recalcFake{}
			svc := NewService(catalog, recalc)

			rsp, err := svc.RecalcFactors(context.Background(), &factorpb.RecalcFactorsReq{
				TaskId:    catalog.set.TaskID,
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
	catalog := &catalogFake{set: domain.FactorTask{
		TaskID:          "ftask_binance_kline_1m",
		SpaceID:         "crypto",
		SourceDatasetID: "dataset_binance_kline_1m",
		Freq:            "1m",

		ResultDatasetID: "dataset_factor_binance_kline_1m",
		Status:          domain.TaskStatusEnabled,
	}}
	recalc := &recalcFake{}
	svc := NewService(catalog, recalc)

	rsp, err := svc.RecalcFactors(context.Background(), &factorpb.RecalcFactorsReq{
		TaskId:    catalog.set.TaskID,
		Members:   []string{"rolling_mean"},
		Subjects:  []string{"BTCUSDT"},
		StartTime: "2026-10-04T10:00:00+00:00",
		EndTime:   "2026-10-04T10:05:00Z",
		RequestId: "request-1",
	})

	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, 1, recalc.submitCalls)
	require.Equal(t, "ftask_binance_kline_1m", recalc.taskID)
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
		"CreateFactorTask", "SetFactorTaskStatus", "DeleteFactorTask", "GetFactorTask", "ListFactorTasks",
		"CreateFactor", "UpdateFactor", "DeleteFactor", "GetFactor", "ListFactors",
		"AddTaskMember", "RemoveTaskMember", "SetTaskMemberStatus", "BindFactorTask",
		"RecalcFactors", "ListRecalcJobs", "GetRecalcJob", "CancelRecalcJob", "GetStatus",
		"ValidateFactorSpec", "PreviewFactor", "ListFactorFunctions", "NotifyPeriodsFilled",
		"ListFactorCategories", "UpsertFactorCategory", "DeleteFactorCategory", "SetFactorCategory",
		"SubmitFactorParams", "GetFactorParams",
		"PublishUdf", "ListUdfs", "GetUdf", "ActivateUdf",
	}
	got := make([]string, 0, service.Methods().Len())
	for i := 0; i < service.Methods().Len(); i++ {
		got = append(got, string(service.Methods().Get(i).Name()))
	}
	require.ElementsMatch(t, want, got)
}

type catalogFake struct {
	set               domain.FactorTask
	sets              []domain.FactorTask
	members           []domain.TaskMember
	infos             []domain.FactorInfo
	createFactorCalls int
	listTaskID        string
	listStatus        string
	err               error
	writer            string
	submitTask        string
	submitPeriod      time.Time
	submitRequest     string
	submissions       []catalog.MemberSubmission
	paramStates       []domain.FactorTaskMember
	boundTask         string
	boundInstance     string
	addKind           string
}

func (f *catalogFake) CreateTask(_ context.Context, in domain.FactorTask) (domain.FactorTask, error) {
	f.set = in
	return in, nil
}
func (f *catalogFake) SetTaskStatus(_ context.Context, _, status string) (domain.FactorTask, error) {
	f.set.Status = status
	return f.set, nil
}
func (f *catalogFake) DeleteTask(context.Context, string, bool) error { return nil }
func (f *catalogFake) GetTask(context.Context, string) (domain.FactorTask, []domain.TaskMember, error) {
	if f.set.TaskID == "" {
		return domain.FactorTask{}, nil, errors.New("set not found")
	}
	return f.set, f.members, nil
}
func (f *catalogFake) ListTasks(context.Context) ([]domain.FactorTask, error) { return f.sets, nil }
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
func (f *catalogFake) ListFactors(_ context.Context, taskID, status string) ([]domain.FactorInfo, error) {
	f.listTaskID, f.listStatus = taskID, status
	return f.infos, nil
}
func (f *catalogFake) AddTaskMember(_ context.Context, taskID, factorID, kind string, _ map[string]float64) (domain.TaskMember, error) {
	if f.err != nil {
		return domain.TaskMember{}, f.err
	}
	f.addKind = kind
	key := factorID
	if kind == domain.MemberKindVar {
		key = factorID + "__var"
	}
	return domain.TaskMember{
		FactorTaskMember: domain.FactorTaskMember{TaskID: taskID, MemberKey: key, FactorID: factorID, Kind: kind, Status: domain.MemberStatusDisabled},
		Factor:           domain.FactorDef{FactorID: factorID},
	}, nil
}
func (f *catalogFake) RemoveTaskMember(context.Context, string, string) error { return f.err }
func (f *catalogFake) SetTaskMemberStatus(_ context.Context, taskID, factorID, status string) (domain.TaskMember, error) {
	return domain.TaskMember{
		FactorTaskMember: domain.FactorTaskMember{TaskID: taskID, MemberKey: factorID, FactorID: factorID, Status: status},
		Factor:           domain.FactorDef{FactorID: factorID},
	}, nil
}
func (f *catalogFake) ListCategories(context.Context) ([]domain.FactorCategory, error) {
	return nil, f.err
}
func (f *catalogFake) UpsertCategory(_ context.Context, category domain.FactorCategory) (domain.FactorCategory, error) {
	return category, f.err
}
func (f *catalogFake) DeleteCategory(context.Context, string) error { return f.err }
func (f *catalogFake) SetFactorCategory(_ context.Context, factorID, categoryID string) (domain.FactorDef, error) {
	return domain.FactorDef{FactorID: factorID, CategoryID: categoryID}, f.err
}
func (f *catalogFake) BindTask(_ context.Context, taskID, instanceID string) (domain.FactorTask, error) {
	f.boundTask, f.boundInstance = taskID, instanceID
	task := f.set
	task.TaskID, task.BoundInstanceID = taskID, instanceID
	return task, f.err
}
func (f *catalogFake) SubmitParams(_ context.Context, writer, taskID string, period time.Time, requestID string, submissions []catalog.MemberSubmission) ([]store.ParamSubmitOutcome, error) {
	f.writer, f.submitTask, f.submitPeriod, f.submitRequest, f.submissions = writer, taskID, period, requestID, submissions
	results := make([]store.ParamSubmitOutcome, 0, len(submissions))
	for _, submission := range submissions {
		results = append(results, store.ParamSubmitOutcome{MemberKey: submission.MemberKey, Result: store.SubmitAccepted})
	}
	return results, f.err
}
func (f *catalogFake) GetParams(context.Context, string) ([]domain.FactorTaskMember, error) {
	return f.paramStates, f.err
}

type recalcFake struct {
	submitCalls int
	jobs        []RecalcJob
	listTaskID  string
	listStatus  []string
	job         RecalcJob
	taskID      string
	factorIDs   []string
	subjects    []string
	requestID   string
	start       time.Time
	end         time.Time
}

func (f *recalcFake) Submit(_ context.Context, taskID string, factorIDs, subjects []string, requestID string, start, end time.Time) (RecalcJob, error) {
	f.submitCalls++
	f.taskID, f.factorIDs, f.subjects, f.requestID, f.start, f.end = taskID, factorIDs, subjects, requestID, start, end
	f.job = RecalcJob{JobID: requestID, RequestID: requestID, TaskID: taskID, Members: factorIDs, Subjects: subjects, StartTime: start, EndTime: end}
	return f.job, nil
}
func (f *recalcFake) List(_ context.Context, taskID string, statuses []string) ([]RecalcJob, error) {
	f.listTaskID, f.listStatus = taskID, statuses
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

func TestTaskTaskMemberStatusEnablesAndDisablesMembers(t *testing.T) {
	svc := NewService(&catalogFake{}, &recalcFake{})

	enabled, err := svc.SetTaskMemberStatus(context.Background(), &factorpb.SetTaskMemberStatusReq{TaskId: "ftask_a", MemberKey: "momentum", Status: domain.MemberStatusEnabled})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, enabled.GetRetInfo().GetCode())
	require.Equal(t, "enabled", enabled.GetMember().GetStatus())
	require.Equal(t, "ftask_a", enabled.GetMember().GetTaskId())

	disabled, err := svc.SetTaskMemberStatus(context.Background(), &factorpb.SetTaskMemberStatusReq{TaskId: "ftask_a", MemberKey: "momentum", Status: domain.MemberStatusDisabled})
	require.NoError(t, err)
	require.Equal(t, "disabled", disabled.GetMember().GetStatus())

	invalid, err := svc.SetTaskMemberStatus(context.Background(), &factorpb.SetTaskMemberStatusReq{TaskId: "ftask_a", MemberKey: "momentum", Status: "bogus"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, invalid.GetRetInfo().GetCode())
}

func TestListRecalcJobsFiltersByTaskAndPaginates(t *testing.T) {
	recalc := &recalcFake{jobs: []RecalcJob{{JobID: "job-3"}, {JobID: "job-2"}, {JobID: "job-1"}}}
	svc := NewService(&catalogFake{}, recalc)

	missing, err := svc.ListRecalcJobs(context.Background(), &factorpb.ListRecalcJobsReq{})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, missing.GetRetInfo().GetCode())

	rsp, err := svc.ListRecalcJobs(context.Background(), &factorpb.ListRecalcJobsReq{
		TaskId: "ftask_a", Statuses: []string{"running"}, Page: &commonpb.Page{Page: 1, Size: 2},
	})
	require.NoError(t, err)
	require.Equal(t, "ftask_a", recalc.listTaskID)
	require.Equal(t, []string{"running"}, recalc.listStatus)
	require.Len(t, rsp.GetJobs(), 2)
	require.EqualValues(t, 3, rsp.GetPageResult().GetTotal())
	require.True(t, rsp.GetPageResult().GetHasMore())
}

// 修改类接口受生命周期门禁约束：暂停、重置中时被拒绝；读接口不受影响。
func TestMutationGateRejectsWritesButNotReads(t *testing.T) {
	blocked := errors.New("因子管理已暂停")
	gate := func() error { return blocked }
	service := NewService(&catalogFake{}, &recalcFake{}, WithMutationGate(gate))
	ctx := context.Background()

	rsp, err := service.CreateFactorTask(ctx, &factorpb.CreateFactorTaskReq{FactorTask: &factorpb.FactorTask{TaskId: "s"}})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "已暂停")
	del, err := service.DeleteFactor(ctx, &factorpb.DeleteFactorReq{FactorId: "f"})
	require.NoError(t, err)
	require.Contains(t, del.GetRetInfo().GetMsg(), "已暂停")
	recalc, err := service.RecalcFactors(ctx, &factorpb.RecalcFactorsReq{})
	require.NoError(t, err)
	require.Contains(t, recalc.GetRetInfo().GetMsg(), "已暂停")
}
