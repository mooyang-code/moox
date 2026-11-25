package command

import (
	"bytes"
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// historySeedGateway 按方法返回固定的 Collector 应答，并记下最近一次调用。
type historySeedGateway struct {
	method string
	req    any
	status *collectorpb.GetCollectorStatusRsp
	skip   *collectorpb.SkipHistorySeedRsp
	list   *collectorpb.ListHistorySeedsRsp
}

func (g *historySeedGateway) Invoke(_ context.Context, _, method string, req, rsp any) error {
	g.method, g.req = method, req
	switch out := rsp.(type) {
	case *collectorpb.GetCollectorStatusRsp:
		out.RetInfo, out.HistorySeeds = g.status.RetInfo, g.status.HistorySeeds
	case *collectorpb.SkipHistorySeedRsp:
		out.RetInfo, out.Skipped = g.skip.RetInfo, g.skip.Skipped
	case *collectorpb.ListHistorySeedsRsp:
		out.RetInfo, out.Seeds = g.list.RetInfo, g.list.Seeds
	}
	return nil
}
func (g *historySeedGateway) Close() error { return nil }
func (g *historySeedGateway) Forward(context.Context, string, string, int, []byte) ([]byte, error) {
	return nil, nil
}

func useHistorySeedGateway(t *testing.T, gateway *historySeedGateway) {
	t.Helper()
	previous := openCommandGateway
	openCommandGateway = func(context.Context, string, *topology.Config) (commandGateway, error) { return gateway, nil }
	t.Cleanup(func() { openCommandGateway = previous })
}

var okRet = &collectorpb.RetInfo{Code: collectorpb.ErrorCode_SUCCESS}

// collector status 输出每个数据集的历史补齐：状态、进度、预计剩余（只有补齐中才有）、失败分块与处理提示。
func TestCollectorStatusShowsHistorySeeds(t *testing.T) {
	gateway := &historySeedGateway{status: &collectorpb.GetCollectorStatusRsp{RetInfo: okRet, HistorySeeds: []*collectorpb.HistorySeed{
		{DatasetId: "kline_5m", Frequency: "5m", Status: "running", TotalChunks: 40, DoneChunks: 10, EtaSeconds: 1800},
		{DatasetId: "kline_1h", Frequency: "1h", Status: "done", TotalChunks: 8, DoneChunks: 8, FailedChunks: 2},
	}}}
	useHistorySeedGateway(t, gateway)
	var out bytes.Buffer
	require.NoError(t, runCollectorStatus(context.Background(), &out, klineRangeFlags{SpaceID: "stockus"}))
	text := out.String()
	assert.Contains(t, text, `"history_seeding": 1`)
	assert.Contains(t, text, `"progress": "10/40"`)
	assert.Contains(t, text, `"eta_seconds": 1800`)
	assert.Contains(t, text, `"failed_chunks": 2`)
	assert.Contains(t, text, "collector kline backfill")
	assert.NotContains(t, text, `"eta_seconds": 0`, "已结束的补齐不显示预计剩余")
}

// history-seed skip 带空间与数据集调用 SkipHistorySeed，输出放行个数；缺空间时报错。
func TestHistorySeedSkipCallsCollector(t *testing.T) {
	gateway := &historySeedGateway{skip: &collectorpb.SkipHistorySeedRsp{RetInfo: okRet, Skipped: 2}}
	useHistorySeedGateway(t, gateway)
	var out bytes.Buffer
	require.NoError(t, runHistorySeedSkip(context.Background(), &out, klineRangeFlags{SpaceID: "stockus", DatasetID: " kline_5m "}))
	assert.Equal(t, "SkipHistorySeed", gateway.method)
	req := gateway.req.(*collectorpb.SkipHistorySeedReq)
	assert.Equal(t, "stockus", req.GetSpaceId())
	assert.Equal(t, "kline_5m", req.GetDatasetId())
	assert.Contains(t, out.String(), `"skipped": 2`)
	assert.ErrorContains(t, runHistorySeedSkip(context.Background(), &out, klineRangeFlags{}), "--space-id")

	gateway.skip = &collectorpb.SkipHistorySeedRsp{RetInfo: &collectorpb.RetInfo{Code: collectorpb.ErrorCode_INNER_ERR, Msg: "失败"}}
	assert.ErrorContains(t, runHistorySeedSkip(context.Background(), &out, klineRangeFlags{SpaceID: "stockus"}), "失败")
}

// 命令解析：history-seed skip 挂在 collector 下，--space-id 与 --dataset-id 可用。
func TestHistorySeedSkipCommandRegistered(t *testing.T) {
	mountCommands(rootCmd)
	cmd, _, err := rootCmd.Find([]string{"collector", "history-seed", "skip"})
	require.NoError(t, err)
	assert.Equal(t, "skip", cmd.Name())
	assert.NotNil(t, cmd.Flags().Lookup("space-id"))
	assert.NotNil(t, cmd.Flags().Lookup("dataset-id"))
}

// setup start 结束时：空间有补齐中的数据集才提示；取不到补齐状态不影响启动。
func TestSetupStartNotesHistorySeeding(t *testing.T) {
	run := func(client *seedingStartClient) setupStartSummary {
		summary, err := runSetupStart(context.Background(), setupDeps{}, client, testSetupSnapshot(t), startRepository(t), "crypto", 1)
		require.NoError(t, err)
		return summary
	}
	summary := run(&seedingStartClient{fakeStartClient: startClient(), running: 2})
	assert.Contains(t, summary.Notes[len(summary.Notes)-1], "历史正在自动补，补齐前不算因子、不下单")
	assert.Contains(t, summary.Notes[len(summary.Notes)-1], "collector status --space-id crypto")
	for _, client := range []*seedingStartClient{{fakeStartClient: startClient()}, {fakeStartClient: startClient(), running: 2, err: assert.AnError}} {
		for _, note := range run(client).Notes {
			assert.NotContains(t, note, "历史正在自动补")
		}
	}
}

type seedingStartClient struct {
	*fakeStartClient
	running int
	err     error
}

func (c *seedingStartClient) HistorySeeding(context.Context, string) (int, error) {
	return c.running, c.err
}

// 线上实现按空间只取补齐中的数据集。
func TestGatewayStartClientHistorySeeding(t *testing.T) {
	gateway := &historySeedGateway{list: &collectorpb.ListHistorySeedsRsp{RetInfo: okRet, Seeds: []*collectorpb.HistorySeed{{DatasetId: "a"}, {DatasetId: "b"}}}}
	count, err := gatewayStartClient{gateway}.HistorySeeding(context.Background(), "crypto")
	require.NoError(t, err)
	assert.Equal(t, 2, count)
	req := gateway.req.(*collectorpb.ListHistorySeedsReq)
	assert.Equal(t, "crypto", req.GetSpaceId())
	assert.True(t, req.GetOnlyRunning())
}
