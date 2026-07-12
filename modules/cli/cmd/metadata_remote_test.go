package cmd

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	commonpb "github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func metadataTestServer(t *testing.T, handlers map[string]func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		method := parts[len(parts)-1]
		if h, ok := handlers[method]; ok {
			h(w, r)
			return
		}
		t.Errorf("unexpected metadata call: %s", r.URL.Path)
		http.NotFound(w, r)
	}))
}

func writeProtoJSON(w http.ResponseWriter, msg proto.Message) {
	w.Header().Set("Content-Type", "application/json")
	raw, _ := protojson.MarshalOptions{UseProtoNames: true}.Marshal(msg)
	_, _ = w.Write(raw)
}

func TestRunMetadataImportCreatesResource(t *testing.T) {
	server := metadataTestServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"CreateSpace": func(w http.ResponseWriter, r *http.Request) {
			writeProtoJSON(w, &pb.CreateSpaceRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}})
		},
	})
	defer server.Close()

	space := &pb.Space{SpaceId: "crypto", Name: "Crypto", Status: "active"}
	calls := []metadataImportCall{{
		Resource: "spaces", Method: "CreateSpace",
		Request: &pb.CreateSpaceReq{Space: space}, Response: &pb.CreateSpaceRsp{},
	}}
	summary, err := runMetadataImport(context.Background(), server.URL, calls, false)
	require.NoError(t, err)
	assert.Equal(t, 1, summary.Applied)
	assert.Equal(t, 0, summary.Skipped)
}

func TestRunMetadataImportSkipsWhenExists(t *testing.T) {
	server := metadataTestServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"GetSpace": func(w http.ResponseWriter, r *http.Request) {
			writeProtoJSON(w, &pb.GetSpaceRsp{
				RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS},
				Space:   &pb.Space{SpaceId: "crypto", Name: "Crypto"},
			})
		},
	})
	defer server.Close()

	space := &pb.Space{SpaceId: "crypto", Name: "Crypto", Status: "active"}
	calls := []metadataImportCall{{
		Resource: "spaces", Method: "CreateSpace",
		Request: &pb.CreateSpaceReq{Space: space}, Response: &pb.CreateSpaceRsp{},
		Exists: &metadataExistsProbe{
			Method: "GetSpace", Request: &pb.GetSpaceReq{SpaceId: "crypto"}, Response: &pb.GetSpaceRsp{},
		},
	}}
	summary, err := runMetadataImport(context.Background(), server.URL, calls, true)
	require.NoError(t, err)
	assert.Equal(t, 0, summary.Applied)
	assert.Equal(t, 1, summary.Skipped)
}

func TestMetadataResourceExists(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		server := metadataTestServer(t, map[string]func(http.ResponseWriter, *http.Request){
			"GetSpace": func(w http.ResponseWriter, r *http.Request) {
				writeProtoJSON(w, &pb.GetSpaceRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}})
			},
		})
		defer server.Close()
		probe := &metadataExistsProbe{Method: "GetSpace", Request: &pb.GetSpaceReq{SpaceId: "crypto"}, Response: &pb.GetSpaceRsp{}}
		ok, err := metadataResourceExists(context.Background(), server.URL, probe)
		require.NoError(t, err)
		assert.True(t, ok)
	})
	t.Run("not found", func(t *testing.T) {
		server := metadataTestServer(t, map[string]func(http.ResponseWriter, *http.Request){
			"GetSpace": func(w http.ResponseWriter, r *http.Request) {
				writeProtoJSON(w, &pb.GetSpaceRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SPACE_NOT_FOUND, Msg: "missing"}})
			},
		})
		defer server.Close()
		probe := &metadataExistsProbe{Method: "GetSpace", Request: &pb.GetSpaceReq{SpaceId: "crypto"}, Response: &pb.GetSpaceRsp{}}
		ok, err := metadataResourceExists(context.Background(), server.URL, probe)
		require.NoError(t, err)
		assert.False(t, ok)
	})
	t.Run("error", func(t *testing.T) {
		server := metadataTestServer(t, map[string]func(http.ResponseWriter, *http.Request){
			"GetSpace": func(w http.ResponseWriter, r *http.Request) {
				writeProtoJSON(w, &pb.GetSpaceRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_INNER_ERR, Msg: "boom"}})
			},
		})
		defer server.Close()
		probe := &metadataExistsProbe{Method: "GetSpace", Request: &pb.GetSpaceReq{SpaceId: "crypto"}, Response: &pb.GetSpaceRsp{}}
		_, err := metadataResourceExists(context.Background(), server.URL, probe)
		require.Error(t, err)
	})
}

func TestRunMetadataApplyCreatesWhenMissing(t *testing.T) {
	createCalled := false
	server := metadataTestServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"GetSpace": func(w http.ResponseWriter, r *http.Request) {
			writeProtoJSON(w, &pb.GetSpaceRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SPACE_NOT_FOUND, Msg: "missing"}})
		},
		"CreateSpace": func(w http.ResponseWriter, r *http.Request) {
			createCalled = true
			writeProtoJSON(w, &pb.CreateSpaceRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}})
		},
	})
	defer server.Close()

	space := &pb.Space{SpaceId: "crypto", Name: "Crypto", Status: "active"}
	calls := []metadataImportCall{{
		Resource: "spaces", Method: "CreateSpace",
		Request: &pb.CreateSpaceReq{Space: space}, Response: &pb.CreateSpaceRsp{},
		Exists: &metadataExistsProbe{
			Method: "GetSpace", Request: &pb.GetSpaceReq{SpaceId: "crypto"}, Response: &pb.GetSpaceRsp{},
		},
	}}
	summary, err := runMetadataApply(context.Background(), server.URL, calls)
	require.NoError(t, err)
	assert.True(t, createCalled)
	assert.Equal(t, 1, summary.Applied)
}

func TestRunMetadataApplySkipsUnchanged(t *testing.T) {
	server := metadataTestServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"GetSpace": func(w http.ResponseWriter, r *http.Request) {
			writeProtoJSON(w, &pb.GetSpaceRsp{
				RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS},
				Space:   &pb.Space{SpaceId: "crypto", Name: "Crypto", Status: "active"},
			})
		},
	})
	defer server.Close()

	space := &pb.Space{SpaceId: "crypto", Name: "Crypto", Status: "active"}
	calls := []metadataImportCall{{
		Resource: "spaces", Method: "CreateSpace",
		Request: &pb.CreateSpaceReq{Space: space}, Response: &pb.CreateSpaceRsp{},
		Exists: &metadataExistsProbe{
			Method: "GetSpace", Request: &pb.GetSpaceReq{SpaceId: "crypto"}, Response: &pb.GetSpaceRsp{},
		},
	}}
	summary, err := runMetadataApply(context.Background(), server.URL, calls)
	require.NoError(t, err)
	assert.Equal(t, 0, summary.Applied)
	assert.Equal(t, 1, summary.Skipped)
	assert.Equal(t, 1, summary.Unchanged)
}

func TestRunMetadataApplyDatasetColumnProbe(t *testing.T) {
	createCalled := false
	column := &pb.DatasetColumn{SpaceId: "crypto", DatasetId: "kline", ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active"}
	server := metadataTestServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"ListDatasetColumns": func(w http.ResponseWriter, r *http.Request) {
			writeProtoJSON(w, &pb.ListDatasetColumnsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Columns: []*pb.DatasetColumn{}})
		},
		"UpsertDatasetColumn": func(w http.ResponseWriter, r *http.Request) {
			createCalled = true
			writeProtoJSON(w, &pb.UpsertDatasetColumnRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}})
		},
	})
	defer server.Close()

	calls := []metadataImportCall{{
		Resource: "dataset_columns", Method: "UpsertDatasetColumn",
		Request: &pb.UpsertDatasetColumnReq{Column: column}, Response: &pb.UpsertDatasetColumnRsp{},
	}}
	summary, err := runMetadataApply(context.Background(), server.URL, calls)
	require.NoError(t, err)
	assert.True(t, createCalled)
	assert.Equal(t, 1, summary.Applied)
}

func TestRunMetadataApplyRejectsUnsupportedResource(t *testing.T) {
	_, err := runMetadataApply(context.Background(), "http://unused", []metadataImportCall{{
		Resource: "subjects", Method: "UpsertSubject",
		Request: &pb.UpsertSubjectReq{Subject: &pb.Subject{SpaceId: "crypto", SubjectId: "BTC"}},
	}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "apply does not support")
}

func TestWriteMetadataImportSummary(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	err = writeMetadataImportSummary(metadataImportSummary{Status: "ok", Planned: 1})
	w.Close()
	os.Stdout = old
	require.NoError(t, err)
	raw, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"status": "ok"`)
}

func TestApplyProbeResultDatasetColumns(t *testing.T) {
	column := &pb.DatasetColumn{SpaceId: "crypto", DatasetId: "kline", ColumnName: "close"}
	probe := &metadataExistsProbe{
		Method:  "ListDatasetColumns",
		Request: &pb.ListDatasetColumnsReq{SpaceId: "crypto", DatasetId: "kline", Page: &commonpb.Page{Page: 1, Size: 500}},
		Response: &pb.ListDatasetColumnsRsp{
			RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS},
			Columns: []*pb.DatasetColumn{column},
		},
	}
	found, actual := applyProbeResult("dataset_columns", probe, &pb.UpsertDatasetColumnReq{Column: column})
	assert.True(t, found)
	assert.Equal(t, column, actual)
}
