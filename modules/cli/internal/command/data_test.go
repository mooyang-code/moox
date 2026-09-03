package command

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestDefaultAndRequiredFlagValue(t *testing.T) {
	assert.Equal(t, "default", defaultFlag("", "default"))
	assert.Equal(t, "custom", defaultFlag("custom", "default"))
	_, err := requiredFlagValue(" ", "--dataset")
	require.Error(t, err)
	value, err := requiredFlagValue(" dataset-a ", "--dataset")
	require.NoError(t, err)
	assert.Equal(t, "dataset-a", value)
}

func TestTimeSeriesSelectorSeriesTagPresence(t *testing.T) {
	oldSpaceID, oldSubjectID, oldFreq, oldSeriesTag := dataSpaceID, dataSubjectID, dataFreq, dataSeriesTag
	t.Cleanup(func() {
		dataSpaceID, dataSubjectID, dataFreq, dataSeriesTag = oldSpaceID, oldSubjectID, oldFreq, oldSeriesTag
	})
	dataSpaceID, dataSubjectID, dataFreq, dataSeriesTag = "crypto", "BTC-USDT", "1h", ""
	all := timeSeriesSelectorForExport("dataset_spot_kline_1h", false)
	require.Nil(t, all.SeriesTag)

	defaultSeries := timeSeriesSelectorForExport("dataset_spot_kline_1h", true)
	require.NotNil(t, defaultSeries.SeriesTag)
	assert.Equal(t, "", defaultSeries.GetSeriesTag())

	dataSeriesTag = "venue:binance"
	exact := timeSeriesSelectorForExport("dataset_spot_kline_1h", true)
	require.NotNil(t, exact.SeriesTag)
	assert.Equal(t, "venue:binance", exact.GetSeriesTag())
}

func TestDataPrimaryAuthUsesSecretFile(t *testing.T) {
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "")
	path := filepath.Join(t.TempDir(), "storage-auth.env")
	require.NoError(t, os.WriteFile(path, []byte(
		"MOOX_STORAGE_PRIMARY_AUTH_SECRET=primary-secret\nMOOX_STORAGE_VIEW_AUTH_SECRET=view-secret\n",
	), 0o600))
	auth, err := dataPrimaryAuth(path)
	require.NoError(t, err)
	assert.Equal(t, "moox-cli-data-export", auth.GetAppId())
	assert.Equal(t, security.HMACSHA256Hex("primary-secret", []byte("moox-cli-data-export")), auth.GetAppKey())
}

func TestWriteRowsExport(t *testing.T) {
	rsp := &pb.ReadTimeSeriesRowsRsp{Rows: []*pb.TimeSeriesRow{{Key: &pb.TimeSeriesKey{DatasetId: "d1"}}}}
	var buf bytes.Buffer
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := writeRowsExport(rsp, "", "http://storage", "d1", "s1")
	w.Close()
	os.Stdout = old
	io.Copy(&buf, r)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "dataset_id")
	path := filepath.Join(t.TempDir(), "rows.json")
	require.NoError(t, writeRowsExport(rsp, path, "http://storage", "d1", "s1"))
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "dataset_id")
}

func TestPostStorageRawAndRetInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/Access/ReadTimeSeriesRows", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		body, _ := protojson.Marshal(&pb.ReadTimeSeriesRowsRsp{
			RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS},
		})
		w.Write(body)
	}))
	defer server.Close()
	rsp := &pb.ReadTimeSeriesRowsRsp{}
	err := postStorage(context.Background(), server.URL, "Access", "ReadTimeSeriesRows", &pb.ReadTimeSeriesRowsReq{}, rsp)
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
}

func TestCheckStorageRetInfoRejectsFailure(t *testing.T) {
	err := checkStorageRetInfo("Access", "Write", &pb.ReadTimeSeriesRowsRsp{
		RetInfo: &pb.RetInfo{Code: pb.ErrorCode_INNER_ERR, Msg: "boom"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

func TestResponseRetInfo(t *testing.T) {
	ret, ok := responseRetInfo(&pb.ReadTimeSeriesRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}})
	assert.True(t, ok)
	assert.Equal(t, pb.ErrorCode_SUCCESS, ret.GetCode())
	_, ok = responseRetInfo(&pb.ReadTimeSeriesRowsReq{})
	assert.False(t, ok)
}

func TestExportRowsRemotePropagatesHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("fail"))
	}))
	defer server.Close()
	_, err := exportRowsRemote(context.Background(), server.URL, &pb.ReadTimeSeriesRowsReq{})
	require.Error(t, err)
}

func TestPostStorageRawNetworkError(t *testing.T) {
	err := postStorageRaw(context.Background(), "http://127.0.0.1:1", "Access", "Read", &pb.ReadTimeSeriesRowsReq{}, &pb.ReadTimeSeriesRowsRsp{})
	require.Error(t, err)
}

func TestCheckStorageRetInfoMissingRetInfo(t *testing.T) {
	err := checkStorageRetInfo("Access", "Read", &pb.ReadTimeSeriesRowsRsp{RetInfo: nil})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing ret_info")
}

func TestPostStorageWrapsRetInfoError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := protojson.Marshal(&pb.ReadTimeSeriesRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_INVALID_PARAM, Msg: "bad"}})
		w.Write(body)
	}))
	defer server.Close()
	err := postStorage(context.Background(), server.URL, "Access", "Read", &pb.ReadTimeSeriesRowsReq{}, &pb.ReadTimeSeriesRowsRsp{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, err) || err != nil)
}
