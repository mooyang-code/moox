package storage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientPostsNewStorageEndpointsAndReturnsRetInfoErrors(t *testing.T) {
	t.Parallel()

	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/trpc.moox.storage.Metadata/RegisterDataSubject":
			var req RegisterDataSubjectRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode register request: %v", err)
			}
			if req.AuthInfo.AppID != "collector" || req.Subject.SubjectID != "BTC-USDT" || req.DatasetBindings[0].DatasetID != "binance_spot_symbols" {
				t.Fatalf("unexpected register request: %+v", req)
			}
			_, _ = w.Write([]byte(`{"ret_info":{"code":0,"msg":"ok"}}`))
		case "/trpc.moox.storage.Access/WriteTimeSeriesRows":
			var req WriteTimeSeriesRowsRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode time series request: %v", err)
			}
			if got := req.Rows[0].Key.DataTime; got != "2026-06-25T01:02:03.000000004Z" {
				t.Fatalf("unexpected data_time: %s", got)
			}
			_, _ = w.Write([]byte(`{"ret_info":{"code":0,"msg":"ok"}}`))
		case "/trpc.moox.storage.Access/WriteRecordRows":
			var req WriteRecordRowsRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode record request: %v", err)
			}
			if got := req.Rows[0].Key.Version; got != "latest" {
				t.Fatalf("unexpected record version: %s", got)
			}
			_, _ = w.Write([]byte(`{"ret_info":{"code":7,"msg":"blocked"}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, AuthInfo{AppID: "collector", AppKey: "test"})
	ctx := context.Background()

	err := client.RegisterDataSubject(ctx, RegisterDataSubjectRequest{
		Subject: Subject{SubjectID: "BTC-USDT", SubjectType: "crypto_pair", Market: "spot", Status: "active"},
		DatasetBindings: []DatasetSubject{
			{DatasetID: "binance_spot_symbols", SubjectRole: "record", Status: "active"},
		},
	})
	if err != nil {
		t.Fatalf("RegisterDataSubject returned error: %v", err)
	}

	err = client.WriteTimeSeriesRows(ctx, []TimeSeriesRow{{
		Key:     TimeSeriesKey{SpaceID: "crypto", DatasetID: "binance_spot_kline", SubjectID: "BTC-USDT", Freq: "1m", DataTime: "2026-06-25T01:02:03.000000004Z"},
		Columns: []ColumnValue{DoubleField("open", 100.25)},
	}})
	if err != nil {
		t.Fatalf("WriteTimeSeriesRows returned error: %v", err)
	}

	err = client.WriteRecordRows(ctx, []RecordRow{{
		Key:     RecordKey{SpaceID: "crypto", DatasetID: "binance_spot_symbols", RecordID: "BTC-USDT", Version: "latest"},
		Columns: []ColumnValue{StringField("symbol", "BTC-USDT")},
	}})
	if err == nil {
		t.Fatalf("WriteRecordRows should return ret_info error")
	}

	want := []string{
		"/trpc.moox.storage.Metadata/RegisterDataSubject",
		"/trpc.moox.storage.Access/WriteTimeSeriesRows",
		"/trpc.moox.storage.Access/WriteRecordRows",
	}
	if len(paths) != len(want) {
		t.Fatalf("paths length = %d, want %d: %v", len(paths), len(want), paths)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("path[%d] = %s, want %s", i, paths[i], want[i])
		}
	}
}

func TestFieldHelpersBuildTypedValues(t *testing.T) {
	t.Parallel()

	fields := []ColumnValue{
		StringField("symbol", "BTC-USDT"),
		IntField("trade_num", 12),
		DoubleField("open", 100.25),
		JSONField("raw", `{"ok":true}`),
	}

	if fields[0].ValueType != "FIELD_VALUE_TYPE_STRING" || fields[0].Value.StringValue == nil || *fields[0].Value.StringValue != "BTC-USDT" {
		t.Fatalf("bad string field: %+v", fields[0])
	}
	if fields[1].ValueType != "FIELD_VALUE_TYPE_INT" || fields[1].Value.IntValue == nil || *fields[1].Value.IntValue != 12 {
		t.Fatalf("bad int field: %+v", fields[1])
	}
	if fields[2].ValueType != "FIELD_VALUE_TYPE_DOUBLE" || fields[2].Value.DoubleValue == nil || *fields[2].Value.DoubleValue != 100.25 {
		t.Fatalf("bad double field: %+v", fields[2])
	}
	if fields[3].ValueType != "FIELD_VALUE_TYPE_JSON" || fields[3].Value.JSONValue == nil || *fields[3].Value.JSONValue != `{"ok":true}` {
		t.Fatalf("bad JSON field: %+v", fields[3])
	}
}
