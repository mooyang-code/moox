package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
	"trpc.group/trpc-go/trpc-go/client"
)

type fakeKlineGateway struct {
	readReq   *pb.ReadTimeSeriesRowsReq
	readCtx   context.Context
	readRsp   *pb.ReadTimeSeriesRowsRsp
	readErr   error
	readWait  bool
	taskReqs  []*collectorpb.GetTaskListReq
	taskPages []*collectorpb.GetTaskListRsp
	taskErr   error
}

type klineTestCloser func() error

func (f klineTestCloser) Close() error { return f() }

func (f *fakeKlineGateway) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq, _ ...client.Option) (*pb.ReadTimeSeriesRowsRsp, error) {
	f.readCtx = ctx
	f.readReq = req
	if f.readWait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.readRsp, f.readErr
}

func (f *fakeKlineGateway) ListCollectionTasks(_ context.Context, req *collectorpb.GetTaskListReq) (*collectorpb.GetTaskListRsp, error) {
	f.taskReqs = append(f.taskReqs, req)
	if f.taskErr != nil {
		return nil, f.taskErr
	}
	page := int(req.GetPage().GetPage())
	if page <= 0 {
		page = 1
	}
	if page > len(f.taskPages) {
		return &collectorpb.GetTaskListRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}}, nil
	}
	return f.taskPages[page-1], nil
}

func makeTestKlineTask(spaceID, taskID, name, freq, datasetID string, tags []string, enabled bool) *collectorpb.CollectionTask {
	params, err := structpb.NewStruct(map[string]any{"frequency": freq})
	if err != nil {
		panic(err)
	}
	en := enabled
	return &collectorpb.CollectionTask{
		SpaceId:       spaceID,
		TaskId:        taskID,
		TaskName:      name,
		DataType:      "kline",
		TagIds:        tags,
		CollectParams: params,
		Enabled:       &en,
		Result:        &collectorpb.TaskResult{DatasetId: datasetID},
	}
}

func newTestDataKlineCommand(t *testing.T, gw *fakeKlineGateway) (*bytes.Buffer, *bytes.Buffer, *cobra.Command) {
	t.Helper()
	cmd := newDataKlineGetCmd(dataKlineDeps{
		open: func(context.Context, string) (klineGateway, io.Closer, error) {
			return gw, nil, nil
		},
	})
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	return stdout, stderr, cmd
}

func TestDataKlineOwnsGatewayUntilRequestFinishes(t *testing.T) {
	for _, rpcError := range []error{nil, errors.New("RPC failed")} {
		t.Run(fmt.Sprint(rpcError), func(t *testing.T) {
			closed := false
			gw := &fakeKlineGateway{readRsp: &pb.ReadTimeSeriesRowsRsp{RetInfo: &pb.RetInfo{}}, readErr: rpcError}
			cmd := newDataKlineGetCmd(dataKlineDeps{
				open: func(ctx context.Context, file string) (klineGateway, io.Closer, error) {
					require.Equal(t, "operator.toml", file)
					_, bounded := ctx.Deadline()
					require.True(t, bounded)
					return gw, klineTestCloser(func() error { closed = true; return nil }), nil
				},
			})
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs([]string{"--file", "operator.toml", "--space-id", "crypto", "--dataset-id", "dataset_binance_kline_1m", "--symbol", "BTC-USDT"})
			if rpcError == nil {
				require.NoError(t, cmd.Execute())
			} else {
				require.ErrorContains(t, cmd.Execute(), rpcError.Error())
			}
			require.True(t, closed)
		})
	}
}

func TestDataKlineDirectDatasetIDBuildsRequest(t *testing.T) {
	gw := &fakeKlineGateway{readRsp: &pb.ReadTimeSeriesRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}}
	stdout, _, cmd := newTestDataKlineCommand(t, gw)
	cmd.SetArgs([]string{
		"--space-id", "crypto",
		"--dataset-id", "dataset_binance_kline_1m",
		"--symbol", "BTC-USDT",
		"--from", "2026-08-28T00:00:00Z",
		"--to", "2026-08-28T01:00:00Z",
		"--limit", "5",
	})
	require.NoError(t, cmd.Execute())
	require.Empty(t, gw.taskReqs, "直接传 --dataset-id 时不需要查询 Collector 任务")
	require.NotNil(t, gw.readReq)
	assert.Equal(t, uint32(5), gw.readReq.GetPage().GetSize())
	assert.Equal(t, uint32(0), gw.readReq.GetLatestPerSeries(), "给了时间范围时按区间读取")
	assert.Equal(t, "2026-08-28T00:00:00Z", gw.readReq.GetTimeRange().GetStartTime())
	assert.Equal(t, []string{"open", "high", "low", "close", "volume"}, gw.readReq.GetColumnNames())
	assert.Equal(t, "crypto", gw.readReq.GetSpaceId())
	assert.Equal(t, "dataset_binance_kline_1m", gw.readReq.GetDatasetId())
	require.Len(t, gw.readReq.GetSelectors(), 1)
	selector := gw.readReq.GetSelectors()[0]
	assert.Equal(t, "BTC-USDT", selector.GetSubjectId())
	assert.Equal(t, "1m", selector.GetFreq())
	assert.Nil(t, selector.SeriesTag, "未传 --series-tag 时不限制序列标签")
	assert.Contains(t, stdout.String(), "ret_info")
	deadline, ok := gw.readCtx.Deadline()
	require.True(t, ok)
	assert.WithinDuration(t, time.Now().Add(15*time.Second), deadline, time.Second)
}

func TestDataKlineExplicitSeriesTagAndLatestQuery(t *testing.T) {
	gw := &fakeKlineGateway{readRsp: &pb.ReadTimeSeriesRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}}
	_, _, cmd := newTestDataKlineCommand(t, gw)
	cmd.SetArgs([]string{
		"--space-id", "stockcn",
		"--dataset-id", "dataset_stockcn_equity_kline_5m",
		"--symbol", "600519.SH",
		"--interval", "5m",
		"--series-tag", "default",
	})
	require.NoError(t, cmd.Execute())
	require.NotNil(t, gw.readReq)
	assert.Equal(t, "stockcn", gw.readReq.GetSpaceId())
	assert.Equal(t, "dataset_stockcn_equity_kline_5m", gw.readReq.GetDatasetId())
	require.Len(t, gw.readReq.GetSelectors(), 1)
	selector := gw.readReq.GetSelectors()[0]
	assert.Equal(t, "600519.SH", selector.GetSubjectId())
	assert.Equal(t, "5m", selector.GetFreq())
	require.NotNil(t, selector.SeriesTag)
	assert.Equal(t, "default", *selector.SeriesTag)
	assert.Equal(t, uint32(100), gw.readReq.GetLatestPerSeries(), "没有时间范围时取最新的 limit 根")
	assert.Nil(t, gw.readReq.GetTimeRange())
}

func TestDataKlineResolvesDatasetByTagAndIntervalAcrossPages(t *testing.T) {
	gw := &fakeKlineGateway{
		readRsp: &pb.ReadTimeSeriesRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}},
		taskPages: []*collectorpb.GetTaskListRsp{
			{
				RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
				Tasks: []*collectorpb.CollectionTask{
					makeTestKlineTask("crypto", "t_swap", "合约 1m", "1m", "dataset_swap_1m", []string{"binance_swap"}, true),
				},
				PageResult: &commonpb.PageResult{Page: 1, Size: 100, HasMore: true},
			},
			{
				RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
				Tasks: []*collectorpb.CollectionTask{
					makeTestKlineTask("crypto", "t_spot", "现货 1m", "1m", "dataset_spot_1m", []string{"binance_spot"}, true),
				},
				PageResult: &commonpb.PageResult{Page: 2, Size: 100, HasMore: false},
			},
		},
	}
	_, _, cmd := newTestDataKlineCommand(t, gw)
	cmd.SetArgs([]string{"--space-id", "crypto", "--tag-id", "binance_spot", "--interval", "1M", "--symbol", "BTC-USDT"})
	require.NoError(t, cmd.Execute())
	require.Len(t, gw.taskReqs, 2)
	assert.Equal(t, "crypto", gw.taskReqs[0].GetSpaceId())
	assert.Equal(t, "kline", gw.taskReqs[0].GetDataType())
	require.NotNil(t, gw.readReq)
	assert.Equal(t, "dataset_spot_1m", gw.readReq.GetDatasetId())
	assert.Equal(t, "1m", gw.readReq.GetSelectors()[0].GetFreq())
}

func TestDataKlineResolveByTagHandlesDisambiguationAndErrors(t *testing.T) {
	t.Run("仅一条启用任务时优先选中", func(t *testing.T) {
		gw := &fakeKlineGateway{
			readRsp: &pb.ReadTimeSeriesRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}},
			taskPages: []*collectorpb.GetTaskListRsp{{
				RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
				Tasks: []*collectorpb.CollectionTask{
					makeTestKlineTask("stockcn", "t_plain", "纯日线", "1d", "dataset_plain", []string{"cn_hs300"}, false),
					makeTestKlineTask("stockcn", "t_extra", "全字段日线", "1d", "dataset_extra", []string{"cn_hs300"}, true),
				},
			}},
		}
		_, _, cmd := newTestDataKlineCommand(t, gw)
		cmd.SetArgs([]string{"--space-id", "stockcn", "--tag-id", "cn_hs300", "--interval", "1d", "--symbol", "600519.SH"})
		require.NoError(t, cmd.Execute())
		require.NotNil(t, gw.readReq)
		assert.Equal(t, "dataset_extra", gw.readReq.GetDatasetId())
	})

	t.Run("多条同态匹配时列出候选并要求传dataset-id", func(t *testing.T) {
		gw := &fakeKlineGateway{
			taskPages: []*collectorpb.GetTaskListRsp{{
				RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
				Tasks: []*collectorpb.CollectionTask{
					makeTestKlineTask("stockcn", "t_b", "全字段日线", "1d", "dataset_b", []string{"cn_hs300"}, true),
					makeTestKlineTask("stockcn", "t_a", "纯日线", "1d", "dataset_a", []string{"cn_hs300"}, true),
				},
			}},
		}
		_, _, cmd := newTestDataKlineCommand(t, gw)
		cmd.SetArgs([]string{"--space-id", "stockcn", "--tag-id", "cn_hs300", "--interval", "1d", "--symbol", "600519.SH"})
		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "匹配到多个 K 线采集任务")
		assert.Contains(t, err.Error(), "dataset_a")
		assert.Contains(t, err.Error(), "dataset_b")
		assert.Contains(t, err.Error(), "--dataset-id")
		assert.Nil(t, gw.readReq)
	})

	t.Run("未找到匹配任务时提示查看任务列表", func(t *testing.T) {
		gw := &fakeKlineGateway{
			taskPages: []*collectorpb.GetTaskListRsp{{
				RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
			}},
		}
		_, _, cmd := newTestDataKlineCommand(t, gw)
		cmd.SetArgs([]string{"--space-id", "crypto", "--tag-id", "missing_tag", "--interval", "5m", "--symbol", "BTC-USDT"})
		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "未找到标签")
		assert.Contains(t, err.Error(), "collector task list")
		assert.Nil(t, gw.readReq)
	})
}

func TestDataKlineValidatesRequiredFlagsAndRange(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "缺少 space-id", args: []string{"--dataset-id", "d1", "--symbol", "BTC-USDT"}, want: "space-id"},
		{name: "缺少 symbol", args: []string{"--space-id", "crypto", "--dataset-id", "d1"}, want: "symbol"},
		{name: "dataset-id 和 tag-id 都未传", args: []string{"--space-id", "crypto", "--symbol", "BTC-USDT"}, want: "--dataset-id 或 --tag-id"},
		{name: "dataset-id 和 tag-id 同时传", args: []string{"--space-id", "crypto", "--dataset-id", "d1", "--tag-id", "t1", "--symbol", "BTC-USDT"}, want: "不能同时指定"},
		{name: "空 interval", args: []string{"--space-id", "crypto", "--dataset-id", "d1", "--symbol", "BTC-USDT", "--interval", " "}, want: "--interval"},
		{name: "limit 为 0", args: []string{"--space-id", "crypto", "--dataset-id", "d1", "--symbol", "BTC-USDT", "--limit", "0"}, want: "1..1000"},
		{name: "limit 超上限", args: []string{"--space-id", "crypto", "--dataset-id", "d1", "--symbol", "BTC-USDT", "--limit", "1001"}, want: "1..1000"},
		{name: "timeout 为 0", args: []string{"--space-id", "crypto", "--dataset-id", "d1", "--symbol", "BTC-USDT", "--timeout", "0s"}, want: "--timeout"},
		{name: "负 timeout", args: []string{"--space-id", "crypto", "--dataset-id", "d1", "--symbol", "BTC-USDT", "--timeout", "-1s"}, want: "--timeout"},
		{name: "非法起始时间", args: []string{"--space-id", "crypto", "--dataset-id", "d1", "--symbol", "BTC-USDT", "--from", "today"}, want: "RFC3339"},
		{name: "起止时间倒挂", args: []string{"--space-id", "crypto", "--dataset-id", "d1", "--symbol", "BTC-USDT", "--from", "2026-08-28T02:00:00Z", "--to", "2026-08-28T01:00:00Z"}, want: "--from"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gw := &fakeKlineGateway{}
			_, _, cmd := newTestDataKlineCommand(t, gw)
			cmd.SetArgs(test.args)
			err := cmd.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.want)
			assert.Nil(t, gw.readReq)
		})
	}
}

func TestDataKlineRPCTimeout(t *testing.T) {
	gw := &fakeKlineGateway{readWait: true}
	_, _, cmd := newTestDataKlineCommand(t, gw)
	cmd.SetArgs([]string{"--space-id", "crypto", "--dataset-id", "d1", "--symbol", "BTC-USDT", "--timeout", "10ms"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Contains(t, err.Error(), "deadline exceeded")
}

func TestDataKlineRejectsBusinessAndRPCFailures(t *testing.T) {
	gw := &fakeKlineGateway{readErr: errors.New("rpc unavailable")}
	_, _, cmd := newTestDataKlineCommand(t, gw)
	cmd.SetArgs([]string{"--space-id", "crypto", "--dataset-id", "d1", "--symbol", "BTC-USDT"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rpc unavailable")

	gw = &fakeKlineGateway{readRsp: &pb.ReadTimeSeriesRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_INVALID_PARAM, Msg: "bad selector"}}}
	_, _, cmd = newTestDataKlineCommand(t, gw)
	cmd.SetArgs([]string{"--space-id", "crypto", "--dataset-id", "d1", "--symbol", "BTC-USDT"})
	err = cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad selector")
}

func TestDataKlineWritesAtomic0600Output(t *testing.T) {
	gw := &fakeKlineGateway{readRsp: &pb.ReadTimeSeriesRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}}
	stdout, _, cmd := newTestDataKlineCommand(t, gw)
	output := filepath.Join(t.TempDir(), "rows.json")
	cmd.SetArgs([]string{"--space-id", "crypto", "--dataset-id", "d1", "--symbol", "BTC-USDT", "--interval", " 1M ", "--output", output})
	require.NoError(t, cmd.Execute())
	assert.Empty(t, stdout.String())
	info, err := os.Stat(output)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	raw, err := os.ReadFile(output)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "ret_info")
	var rsp pb.ReadTimeSeriesRowsRsp
	require.NoError(t, protojson.Unmarshal(raw, &rsp))
}

func TestDataKlineRejectsInputFileAsOutput(t *testing.T) {
	gw := &fakeKlineGateway{readRsp: &pb.ReadTimeSeriesRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}}
	manifest := filepath.Join(t.TempDir(), "moox.toml")
	original := "hosts = {}\n"
	require.NoError(t, os.WriteFile(manifest, []byte(original), 0o600))
	_, _, cmd := newTestDataKlineCommand(t, gw)
	cmd.SetArgs([]string{"--file", manifest, "--space-id", "crypto", "--dataset-id", "d1", "--symbol", "BTC-USDT", "--output", manifest})
	err := cmd.Execute()
	require.ErrorContains(t, err, "不能是同一个文件")
	assert.Nil(t, gw.readReq)
	raw, readErr := os.ReadFile(manifest)
	require.NoError(t, readErr)
	assert.Equal(t, original, string(raw))
}

func TestDataCommandRemovesSkillSubcommand(t *testing.T) {
	for _, sub := range dataCmd.Commands() {
		assert.NotEqual(t, "skill", sub.Name(), "data skill 子命令已删除")
	}
}
