package command

import (
	"bytes"
	"context"
	"io"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

const (
	unifiedSpotTag = "venue:binance|market:spot|source:spot_http"
	unifiedSwapTag = "venue:binance|market:swap|source:swap_http"
)

// unifiedReader 模拟统一数据集：探测（不带 series_tag）返回两个市场各一根，带 tag 的读取只返回那个 tag 的行，并记下每次请求。
type unifiedReader struct {
	tags     []string
	requests []*pb.ReadTimeSeriesRowsReq
}

func (r *unifiedReader) ReadTimeSeriesRows(_ context.Context, req *pb.ReadTimeSeriesRowsReq, _ ...client.Option) (*pb.ReadTimeSeriesRowsRsp, error) {
	r.requests = append(r.requests, req)
	rsp := &pb.ReadTimeSeriesRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}
	selector := req.GetSelectors()[0]
	for _, tag := range r.tags {
		if selector.SeriesTag != nil && selector.GetSeriesTag() != tag {
			continue
		}
		rsp.Rows = append(rsp.Rows, &pb.TimeSeriesRow{Key: &pb.TimeSeriesKey{SubjectId: selector.GetSubjectId(), Freq: selector.GetFreq(), DataTime: "2026-11-04T00:00:00Z", SeriesTag: tag}})
	}
	return rsp, nil
}

func runUnifiedKline(t *testing.T, reader timeSeriesReader, args ...string) (*bytes.Buffer, error) {
	t.Helper()
	cfg := testSkillDataAccessConfig()
	stdout := &bytes.Buffer{}
	cmd := newDataKlineGetCmd(dataKlineDeps{
		loadConfig: func(string) (dataAccessConfig, error) { return cfg, nil },
		newReader: func(context.Context, string, dataAccessConfig, string) (timeSeriesReader, io.Closer, error) {
			return reader, nil, nil
		},
	})
	cmd.SetOut(stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(append([]string{"--data-type", "crypto", "--symbol", "BTC-USDT"}, args...))
	return stdout, cmd.Execute()
}

// 统一数据集：--market 先探测数据集里已有的序列，按 series_tag 所属市场选出那一条，真正的读取带上它。
func TestDataKlineMarketSelectsSeriesByMarket(t *testing.T) {
	for market, want := range map[string]string{"swap": unifiedSwapTag, " SPOT ": unifiedSpotTag} {
		reader := &unifiedReader{tags: []string{unifiedSpotTag, unifiedSwapTag}}
		stdout, err := runUnifiedKline(t, reader, "--market", market, "--limit", "3")
		require.NoError(t, err)
		require.Len(t, reader.requests, 2, "探测一次，真正读取一次")
		assert.Nil(t, reader.requests[0].GetSelectors()[0].SeriesTag, "探测不带 series_tag")
		assert.Equal(t, uint32(1), reader.requests[0].GetLatestPerSeries())
		final := reader.requests[1]
		assert.Equal(t, want, final.GetSelectors()[0].GetSeriesTag())
		assert.Equal(t, uint32(3), final.GetLatestPerSeries(), "--limit 只针对选中的序列")
		assert.Contains(t, stdout.String(), want)
		assert.NotContains(t, stdout.String(), "market:"+map[string]string{unifiedSwapTag: "spot", unifiedSpotTag: "swap"}[want])
	}
}

// 不给 --market 时沿用交易所配置的 series_tag，一次读取，行为不变。
func TestDataKlineWithoutMarketKeepsConfiguredSeriesTag(t *testing.T) {
	reader := &unifiedReader{tags: []string{unifiedSpotTag, unifiedSwapTag}}
	_, err := runUnifiedKline(t, reader)
	require.NoError(t, err)
	require.Len(t, reader.requests, 1)
	assert.Equal(t, unifiedSpotTag, reader.requests[0].GetSelectors()[0].GetSeriesTag())
}

func TestDataKlineMarketRejectsUnknownOrMissingMarket(t *testing.T) {
	_, err := runUnifiedKline(t, &unifiedReader{tags: []string{unifiedSpotTag}}, "--market", "futures")
	require.ErrorContains(t, err, "spot 或 swap")

	_, err = runUnifiedKline(t, &unifiedReader{tags: []string{unifiedSpotTag}}, "--market", "swap")
	require.ErrorContains(t, err, "没有 BTC-USDT 的 swap 序列")

	_, err = runUnifiedKline(t, &unifiedReader{tags: []string{unifiedSwapTag, "venue:okx|market:swap|source:swap_http"}}, "--market", "swap")
	require.ErrorContains(t, err, "多条序列")
}
