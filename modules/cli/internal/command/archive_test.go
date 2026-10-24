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

func TestArchiveCreateSendsTheScheduleAndPrintsProtoFieldNames(t *testing.T) {
	g := &archiveFakeGateway{rsp: &archivepb.CreateArchiveTaskRsp{RetInfo: ok(), Task: &archivepb.ArchiveTask{SpaceId: "crypto", DatasetId: "ds", IntervalSeconds: 3600, NextRunAt: "2026-10-17T01:34:43Z"}}}
	useArchiveGateway(t, g)
	var out bytes.Buffer
	err := runArchiveTasksCreate(context.Background(), &out, archiveFlags{SpaceID: "crypto", DatasetID: "ds", Cron: "43 34 * * * *", TZ: "Asia/Shanghai", BackfillFrom: "2026-10-15T00:00:00Z", Paused: true})
	require.NoError(t, err)
	assert.Equal(t, "trpc.moox.archive.ArchiveMgr", g.service)
	assert.Equal(t, "CreateArchiveTask", g.method)
	req := g.req.(*archivepb.CreateArchiveTaskReq)
	assert.Equal(t, "43 34 * * * *", req.GetScheduleCron())
	assert.Equal(t, "Asia/Shanghai", req.GetScheduleTz())
	assert.Equal(t, "2026-10-15T00:00:00Z", req.GetBackfillFrom())
	assert.True(t, req.GetPaused())
	assert.True(t, g.closed)
	assert.Contains(t, out.String(), `"interval_seconds": 3600`)
	assert.Contains(t, out.String(), `"next_run_at": "2026-10-17T01:34:43Z"`)
}

func TestArchiveCommandsValidateInputsBeforeCallingTheGateway(t *testing.T) {
	g := &archiveFakeGateway{}
	useArchiveGateway(t, g)
	ctx := context.Background()
	var out bytes.Buffer
	for name, err := range map[string]error{
		"create without dataset":  runArchiveTasksCreate(ctx, &out, archiveFlags{SpaceID: "crypto", Cron: "x"}),
		"create without cron":     runArchiveTasksCreate(ctx, &out, archiveFlags{SpaceID: "crypto", DatasetID: "ds"}),
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
			return runArchiveTasksPreview(ctx, b, archiveFlags{SpaceID: "crypto", DatasetID: "ds", Cron: "0 0 * * * *"})
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

func TestArchiveSurfacesManagerErrors(t *testing.T) {
	g := &archiveFakeGateway{rsp: &archivepb.CreateArchiveTaskRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_CONFLICT, Msg: "数据集已有归档任务"}}}
	useArchiveGateway(t, g)
	var out bytes.Buffer
	err := runArchiveTasksCreate(context.Background(), &out, archiveFlags{SpaceID: "crypto", DatasetID: "ds", Cron: "0 0 * * * *"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "数据集已有归档任务")
	assert.Empty(t, out.String())
}

func TestArchiveAndSyncDeploymentsCommandsAreRegistered(t *testing.T) {
	var found []string
	for _, c := range archiveCmd.Commands() {
		found = append(found, c.Name())
	}
	assert.ElementsMatch(t, []string{"overview", "tasks", "runs", "partitions"}, found)
	setup := newSetupCommand(defaultSetupDeps())
	sync, _, err := setup.Find([]string{"sync-deployments"})
	require.NoError(t, err)
	assert.Equal(t, "sync-deployments", sync.Name())
	require.NotNil(t, sync.Flags().Lookup("host"))
	require.NotNil(t, sync.Flags().Lookup("file"))
}
