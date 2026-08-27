package command

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

type fakeTimeSeriesReader struct {
	request *pb.ReadTimeSeriesRowsReq
	rsp     *pb.ReadTimeSeriesRowsRsp
	err     error
}

func (f *fakeTimeSeriesReader) ReadTimeSeriesRows(_ context.Context, req *pb.ReadTimeSeriesRowsReq, _ ...client.Option) (*pb.ReadTimeSeriesRowsRsp, error) {
	f.request = req
	return f.rsp, f.err
}

func newTestDataKlineCommand(t *testing.T, reader *fakeTimeSeriesReader) (*bytes.Buffer, *bytes.Buffer, *cobra.Command) {
	t.Helper()
	configPath := writeDataAccessConfig(t, validDataAccessYAML, 0o600)
	cmd := newDataKlineGetCmd(dataKlineDeps{
		loadConfig: func(string) (dataAccessConfig, error) { return loadDataAccessConfig(configPath) },
		newReader:  func(dataAccessConfig) timeSeriesReader { return reader },
	})
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	return stdout, stderr, cmd
}

func TestDataKlineBuildsCatalogBackedRPCRequest(t *testing.T) {
	reader := &fakeTimeSeriesReader{rsp: &pb.ReadTimeSeriesRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}}
	stdout, _, cmd := newTestDataKlineCommand(t, reader)
	cmd.SetArgs([]string{"--data-type", " CRYPTO ", "--symbol", "BTC-USDT", "--start-time", "2026-08-28T00:00:00Z", "--end-time", "2026-08-28T01:00:00Z", "--limit", "5"})
	err := cmd.Execute()
	require.NoError(t, err)
	require.NotNil(t, reader.request)
	assert.Equal(t, "moox-skill", reader.request.GetAuthInfo().GetAppId())
	assert.Equal(t, "storage-key", reader.request.GetAuthInfo().GetAppKey())
	assert.Equal(t, pb.SortOrder_SORT_ORDER_DESC, reader.request.GetOrder())
	assert.Equal(t, uint32(5), reader.request.GetPage().GetSize())
	require.Len(t, reader.request.GetSelectors(), 1)
	selector := reader.request.GetSelectors()[0]
	assert.Equal(t, "crypto_market", selector.GetSpaceId())
	assert.Equal(t, "binance_spot_kline_1m", selector.GetDatasetId())
	assert.Equal(t, "BTC-USDT", selector.GetSubjectId())
	assert.Equal(t, "1m", selector.GetFreq())
	assert.Equal(t, "venue:binance", selector.GetSeriesTag())
	assert.Contains(t, stdout.String(), "ret_info")
}

func TestDataKlineValidatesRequiredFlagsAndRange(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "data type", args: []string{"--symbol", "BTC-USDT"}, want: "data-type"},
		{name: "symbol", args: []string{"--data-type", "crypto"}, want: "symbol"},
		{name: "zero limit", args: []string{"--data-type", "crypto", "--symbol", "BTC-USDT", "--limit", "0"}, want: "1..1000"},
		{name: "large limit", args: []string{"--data-type", "crypto", "--symbol", "BTC-USDT", "--limit", "1001"}, want: "1..1000"},
		{name: "bad start", args: []string{"--data-type", "crypto", "--symbol", "BTC-USDT", "--start-time", "today"}, want: "RFC3339"},
		{name: "reverse range", args: []string{"--data-type", "crypto", "--symbol", "BTC-USDT", "--start-time", "2026-08-28T02:00:00Z", "--end-time", "2026-08-28T01:00:00Z"}, want: "start-time"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := &fakeTimeSeriesReader{}
			_, _, cmd := newTestDataKlineCommand(t, reader)
			cmd.SetArgs(test.args)
			err := cmd.Execute()
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.want)
			assert.Nil(t, reader.request)
		})
	}
}

func TestDataKlineRejectsBusinessAndRPCFailures(t *testing.T) {
	reader := &fakeTimeSeriesReader{err: errors.New("rpc unavailable")}
	_, _, cmd := newTestDataKlineCommand(t, reader)
	cmd.SetArgs([]string{"--data-type", "crypto", "--symbol", "BTC-USDT"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rpc unavailable")

	reader = &fakeTimeSeriesReader{rsp: &pb.ReadTimeSeriesRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_INVALID_PARAM, Msg: "bad selector"}}}
	_, _, cmd = newTestDataKlineCommand(t, reader)
	cmd.SetArgs([]string{"--data-type", "crypto", "--symbol", "BTC-USDT"})
	err = cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad selector")
}

func TestDataKlineWritesAtomic0600Output(t *testing.T) {
	reader := &fakeTimeSeriesReader{rsp: &pb.ReadTimeSeriesRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}}
	stdout, _, cmd := newTestDataKlineCommand(t, reader)
	output := filepath.Join(t.TempDir(), "rows.json")
	cmd.SetArgs([]string{"--data-type", "crypto", "--exchange", " BINANCE ", "--symbol", "BTC-USDT", "--interval", " 1M ", "--output", output})
	require.NoError(t, cmd.Execute())
	assert.Empty(t, stdout.String())
	info, err := os.Stat(output)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	raw, err := os.ReadFile(output)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "ret_info")
}
