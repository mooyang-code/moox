package command

import (
	"bytes"
	"context"
	"testing"

	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type archiveFakeGateway struct {
	service, method string
	req             proto.Message
	rsp             proto.Message
	closed          bool
}

func (g *archiveFakeGateway) Invoke(_ context.Context, service, method string, req, rsp any) error {
	g.service, g.method, g.req = service, method, req.(proto.Message)
	if g.rsp != nil {
		proto.Merge(rsp.(proto.Message), g.rsp)
	}
	return nil
}
func (g *archiveFakeGateway) Close() error { g.closed = true; return nil }
func (g *archiveFakeGateway) Forward(context.Context, string, string, int, []byte) ([]byte, error) {
	return nil, nil
}

func useArchiveGateway(t *testing.T, g *archiveFakeGateway) {
	t.Helper()
	previous := openCommandGateway
	openCommandGateway = func(context.Context, string, *setupconfig.Snapshot) (commandGateway, error) { return g, nil }
	t.Cleanup(func() { openCommandGateway = previous })
}

func ok() *commonpb.RetInfo { return &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS} }

func TestArchiveCreateSendsThePullIntervalAndPrintsProtoFieldNames(t *testing.T) {
	g := &archiveFakeGateway{rsp: &archivepb.CreateArchiveTaskRsp{RetInfo: ok(), Task: &archivepb.ArchiveTask{SpaceId: "crypto", DatasetId: "ds", IntervalSeconds: 3600, PullEveryBars: 5, NextPullAt: "2026-10-17T01:05:25Z"}}}
	useArchiveGateway(t, g)
	var out bytes.Buffer
	err := runArchiveTasksCreate(context.Background(), &out, archiveFlags{SpaceID: "crypto", DatasetID: "ds", PullEvery: 5, BackfillFrom: "2026-10-15T00:00:00Z", Paused: true})
	require.NoError(t, err)
	assert.Equal(t, "trpc.moox.archive.ArchiveMgr", g.service)
	assert.Equal(t, "CreateArchiveTask", g.method)
	req := g.req.(*archivepb.CreateArchiveTaskReq)
	assert.Equal(t, uint32(5), req.GetPullEveryBars())
	assert.Equal(t, "2026-10-15T00:00:00Z", req.GetBackfillFrom())
	assert.True(t, req.GetPaused())
	assert.True(t, g.closed)
	assert.Contains(t, out.String(), `"interval_seconds": 3600`)
	assert.Contains(t, out.String(), `"pull_every_bars": 5`)
	assert.Contains(t, out.String(), `"next_pull_at": "2026-10-17T01:05:25Z"`)
}

func TestArchiveCreateAndPrepareSendTheDirNameAndPrintTheSuggestion(t *testing.T) {
	g := &archiveFakeGateway{rsp: &archivepb.CreateArchiveTaskRsp{RetInfo: ok(), Task: &archivepb.ArchiveTask{SpaceId: "crypto", DatasetId: "ds", Purpose: "collect", DirName: "binance-perp-kline-1m"}}}
	useArchiveGateway(t, g)
	var out bytes.Buffer
	require.NoError(t, runArchiveTasksCreate(context.Background(), &out, archiveFlags{SpaceID: "crypto", DatasetID: "ds", DirName: "binance-perp-kline-1m"}))
	assert.Equal(t, "binance-perp-kline-1m", g.req.(*archivepb.CreateArchiveTaskReq).GetDirName())
	assert.Contains(t, out.String(), `"dir_name": "binance-perp-kline-1m"`)

	prep := &archiveFakeGateway{rsp: &archivepb.PrepareArchiveTaskRsp{RetInfo: ok(), Purpose: "collect", SuggestedDirName: "binance-swap-kline-1m", DirNameConflict: true}}
	useArchiveGateway(t, prep)
	out.Reset()
	require.NoError(t, runArchiveTasksPrepare(context.Background(), &out, archiveFlags{SpaceID: "crypto", DatasetID: "ds", DirName: "binance-perp-kline-1m"}))
	assert.Equal(t, "binance-perp-kline-1m", prep.req.(*archivepb.PrepareArchiveTaskReq).GetDirName())
	assert.Contains(t, out.String(), `"suggested_dir_name": "binance-swap-kline-1m"`)
	assert.Contains(t, out.String(), `"dir_name_conflict": true`)
	assert.Contains(t, out.String(), `"purpose": "collect"`)
}

func TestArchivePreviewAndRunsListSendTheNewFields(t *testing.T) {
	g := &archiveFakeGateway{rsp: &archivepb.PreviewArchiveScheduleRsp{RetInfo: ok(), NextRuns: []string{"2026-10-17T01:04:25Z"}, NextReviews: []string{"2026-10-17T01:34:25Z"}}}
	useArchiveGateway(t, g)
	var out bytes.Buffer
	require.NoError(t, runArchiveTasksPreview(context.Background(), &out, archiveFlags{SpaceID: "crypto", DatasetID: "ds", PullEvery: 3}))
	assert.Equal(t, uint32(3), g.req.(*archivepb.PreviewArchiveScheduleReq).GetPullEveryBars())
	assert.Contains(t, out.String(), `"next_reviews": [`)

	runs := &archiveFakeGateway{rsp: &archivepb.ListArchiveRunsRsp{RetInfo: ok()}}
	useArchiveGateway(t, runs)
	require.NoError(t, runArchiveRunsList(context.Background(), &out, archiveFlags{IncludeTicks: true}))
	assert.True(t, runs.req.(*archivepb.ListArchiveRunsReq).GetIncludeTicks())
}

func TestArchiveCommandsValidateInputsBeforeCallingTheGateway(t *testing.T) {
	g := &archiveFakeGateway{}
	useArchiveGateway(t, g)
	ctx := context.Background()
	var out bytes.Buffer
	for name, err := range map[string]error{
		"create without dataset":  runArchiveTasksCreate(ctx, &out, archiveFlags{SpaceID: "crypto"}),
		"delete without yes":      runArchiveTasksDelete(ctx, &out, archiveFlags{SpaceID: "crypto", DatasetID: "ds"}),
		"backfill without range":  runArchiveTasksBackfill(ctx, &out, archiveFlags{SpaceID: "crypto", DatasetID: "ds"}),
		"run get without id":      runArchiveRunsGet(ctx, &out, archiveFlags{}),
		"run cancel without id":   runArchiveRunsCancel(ctx, &out, archiveFlags{}),
		"partition list no space": runArchivePartitionsList(ctx, &out, archiveFlags{}),
		"partition get no key":    runArchivePartitionsGet(ctx, &out, archiveFlags{SpaceID: "crypto", DatasetID: "ds"}),
		"prepare without dataset": runArchiveTasksPrepare(ctx, &out, archiveFlags{SpaceID: "crypto"}),
	} {
		require.Error(t, err, name)
	}
	assert.Empty(t, g.method, "参数不全时不应调用网关")
}

func TestArchiveCommandsMapToTheRightMethods(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		method string
		rsp    proto.Message
		run    func(*bytes.Buffer) error
	}{
		{"GetArchiveOverview", &archivepb.GetArchiveOverviewRsp{RetInfo: ok()}, func(b *bytes.Buffer) error { return runArchiveOverview(ctx, b, archiveFlags{}) }},
		{"ListArchiveTasks", &archivepb.ListArchiveTasksRsp{RetInfo: ok()}, func(b *bytes.Buffer) error { return runArchiveTasksList(ctx, b, archiveFlags{SpaceID: "crypto"}) }},
		{"PrepareArchiveTask", &archivepb.PrepareArchiveTaskRsp{RetInfo: ok()}, func(b *bytes.Buffer) error {
			return runArchiveTasksPrepare(ctx, b, archiveFlags{SpaceID: "crypto", DatasetID: "ds"})
		}},
		{"PreviewArchiveSchedule", &archivepb.PreviewArchiveScheduleRsp{RetInfo: ok()}, func(b *bytes.Buffer) error {
			return runArchiveTasksPreview(ctx, b, archiveFlags{SpaceID: "crypto", DatasetID: "ds"})
		}},
		{"UpdateArchiveTask", &archivepb.UpdateArchiveTaskRsp{RetInfo: ok()}, func(b *bytes.Buffer) error {
			return runArchiveTasksUpdate(ctx, b, archiveFlags{SpaceID: "crypto", DatasetID: "ds", Status: "paused"})
		}},
		{"DeleteArchiveTask", &archivepb.DeleteArchiveTaskRsp{RetInfo: ok()}, func(b *bytes.Buffer) error {
			return runArchiveTasksDelete(ctx, b, archiveFlags{SpaceID: "crypto", DatasetID: "ds", Yes: true})
		}},
		{"RunArchiveTaskNow", &archivepb.RunArchiveTaskNowRsp{RetInfo: ok()}, func(b *bytes.Buffer) error {
			return runArchiveTasksRun(ctx, b, archiveFlags{SpaceID: "crypto", DatasetID: "ds"})
		}},
		{"BackfillArchiveRange", &archivepb.BackfillArchiveRangeRsp{RetInfo: ok()}, func(b *bytes.Buffer) error {
			return runArchiveTasksBackfill(ctx, b, archiveFlags{SpaceID: "crypto", DatasetID: "ds", From: "2026-10-16T00:00:00Z", To: "2026-10-17T00:00:00Z"})
		}},
		{"ListArchiveRuns", &archivepb.ListArchiveRunsRsp{RetInfo: ok()}, func(b *bytes.Buffer) error { return runArchiveRunsList(ctx, b, archiveFlags{Status: "queued"}) }},
		{"GetArchiveRun", &archivepb.GetArchiveRunRsp{RetInfo: ok()}, func(b *bytes.Buffer) error { return runArchiveRunsGet(ctx, b, archiveFlags{RunID: "run_1"}) }},
		{"CancelArchiveRun", &archivepb.CancelArchiveRunRsp{RetInfo: ok()}, func(b *bytes.Buffer) error { return runArchiveRunsCancel(ctx, b, archiveFlags{RunID: "run_1"}) }},
		{"ListArchivePartitions", &archivepb.ListArchivePartitionsRsp{RetInfo: ok()}, func(b *bytes.Buffer) error {
			return runArchivePartitionsList(ctx, b, archiveFlags{SpaceID: "crypto", FromMonth: "2026-10"})
		}},
		{"GetArchivePartition", &archivepb.GetArchivePartitionRsp{RetInfo: ok()}, func(b *bytes.Buffer) error {
			return runArchivePartitionsGet(ctx, b, archiveFlags{SpaceID: "crypto", DatasetID: "ds", PartitionKey: "2026-10-16"})
		}},
	} {
		g := &archiveFakeGateway{rsp: tc.rsp}
		useArchiveGateway(t, g)
		var out bytes.Buffer
		require.NoError(t, tc.run(&out), tc.method)
		assert.Equal(t, tc.method, g.method)
		assert.Equal(t, "trpc.moox.archive.ArchiveMgr", g.service)
	}
}

// archive overview 的输出带上当前避让规则（含依据与计算时间）。
func TestArchiveOverviewPrintsAvoidance(t *testing.T) {
	g := &archiveFakeGateway{rsp: &archivepb.GetArchiveOverviewRsp{RetInfo: ok(), Avoidance: &archivepb.AvoidanceRules{
		EveryMinutes: 1, BeforeSeconds: 5, AfterSeconds: 25, HourQuietFrom: "58:00", HourQuietUntil: "04:00",
		Basis: "crypto：1m、1h；stockcn：5m（每分钟触发，有 1h 及以上，整点避让）", ComputedAt: "2026-11-08T10:00:00Z",
	}}}
	useArchiveGateway(t, g)
	var out bytes.Buffer
	require.NoError(t, runArchiveOverview(context.Background(), &out, archiveFlags{}))
	for _, want := range []string{`"avoidance"`, `"every_minutes": 1`, `"hour_quiet_from": "58:00"`, `"basis": "crypto：1m、1h；stockcn：5m`, `"computed_at": "2026-11-08T10:00:00Z"`} {
		assert.Contains(t, out.String(), want)
	}
}

func TestArchiveSurfacesManagerErrors(t *testing.T) {
	g := &archiveFakeGateway{rsp: &archivepb.CreateArchiveTaskRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_CONFLICT, Msg: "数据集已有归档任务"}}}
	useArchiveGateway(t, g)
	var out bytes.Buffer
	err := runArchiveTasksCreate(context.Background(), &out, archiveFlags{SpaceID: "crypto", DatasetID: "ds"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "数据集已有归档任务")
	assert.Empty(t, out.String())
}

func TestArchiveAndSyncDeploymentsCommandsAreRegistered(t *testing.T) {
	var found []string
	for _, c := range archiveCmd.Commands() {
		found = append(found, c.Name())
	}
	assert.ElementsMatch(t, []string{"overview", "tasks", "runs", "partitions", "worker", "import"}, found)
	setup := newSetupCommand(defaultSetupDeps())
	sync, _, err := setup.Find([]string{"sync-deployments"})
	require.NoError(t, err)
	assert.Equal(t, "sync-deployments", sync.Name())
	require.NotNil(t, sync.Flags().Lookup("host"))
	require.NotNil(t, sync.Flags().Lookup("file"))
}

func TestArchiveWorkerRetireRequiresWorkerAndConfirmation(t *testing.T) {
	g := &archiveFakeGateway{rsp: &archivepb.RetireArchiveWorkerRsp{RetInfo: ok(), Retired: true}}
	useArchiveGateway(t, g)
	var out bytes.Buffer
	require.ErrorContains(t, runArchiveWorkerRetire(context.Background(), &out, archiveFlags{Yes: true}), "--worker")
	require.ErrorContains(t, runArchiveWorkerRetire(context.Background(), &out, archiveFlags{WorkerID: "factor-old"}), "--yes")
	require.NoError(t, runArchiveWorkerRetire(context.Background(), &out, archiveFlags{WorkerID: " archive-worker@old ", Yes: true}))
	assert.Equal(t, "RetireArchiveWorker", g.method)
	assert.Contains(t, out.String(), `"retired": true`)
}
