package command

import (
	"context"
	"testing"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// historySeedGateway 给 gatewayStartClient.HistorySeeding 的单测用。
type historySeedGateway struct {
	method string
	req    any
	list   *collectorpb.ListHistorySeedsRsp
}

func (g *historySeedGateway) Invoke(_ context.Context, _, method string, req, rsp any) error {
	g.method, g.req = method, req
	if out, ok := rsp.(*collectorpb.ListHistorySeedsRsp); ok {
		out.RetInfo, out.Seeds = g.list.RetInfo, g.list.Seeds
	}
	return nil
}
func (g *historySeedGateway) Close() error { return nil }
func (g *historySeedGateway) Forward(context.Context, string, string, int, []byte) ([]byte, error) {
	return nil, nil
}

var okRet = &collectorpb.RetInfo{Code: collectorpb.ErrorCode_SUCCESS}

// collector status 与 history-seed list/skip 均经规格表直接返回接口字段。
func TestCollectorStatusAndHistorySeedSpecs(t *testing.T) {
	gw := &rpcFakeGateway{responses: map[string]string{
		"GetCollectorStatus": `{"ret_info":{"code":0},"level":"ok","history_seeds":[{"dataset_id":"kline_5m","frequency":"5m","status":"running","total_chunks":40,"done_chunks":10,"eta_seconds":1800}]}`,
		"ListHistorySeeds":   `{"ret_info":{"code":0},"seeds":[{"space_id":"stockus","dataset_id":"kline_5m","status":"running"}]}`,
		"SkipHistorySeed":    `{"ret_info":{"code":0},"skipped":2}`,
	}}
	useRPCGateway(t, gw)

	out, _, err := runSpecCommand(t, specByPath(t, "collector", "status"), "--space-id", "stockus")
	require.NoError(t, err)
	assert.Contains(t, out, `"eta_seconds": 1800`)
	assert.JSONEq(t, `{"space_id":"stockus"}`, string(gw.bodies["GetCollectorStatus"]))

	out, _, err = runSpecCommand(t, specByPath(t, "collector", "history-seed", "list"), "--only-running")
	require.NoError(t, err)
	assert.Contains(t, out, `"kline_5m"`)
	assert.JSONEq(t, `{"only_running":true}`, string(gw.bodies["ListHistorySeeds"]))

	out, _, err = runSpecCommand(t, specByPath(t, "collector", "history-seed", "skip"), "--space-id", "stockus", "--dataset-id", "kline_5m")
	require.NoError(t, err)
	assert.Contains(t, out, `"skipped": 2`)
	assert.JSONEq(t, `{"space_id":"stockus","dataset_id":"kline_5m"}`, string(gw.bodies["SkipHistorySeed"]))
}

// 命令解析：history-seed list 与 skip 挂在 collector 下，参数齐备。
func TestHistorySeedCommandsRegistered(t *testing.T) {
	mountCommands(rootCmd)
	for _, sub := range []string{"list", "skip"} {
		cmd, rest, err := rootCmd.Find([]string{"collector", "history-seed", sub})
		require.NoError(t, err, sub)
		require.Empty(t, rest, sub)
		assert.Equal(t, sub, cmd.Name())
		assert.NotNil(t, cmd.Flags().Lookup("space-id"))
	}
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
