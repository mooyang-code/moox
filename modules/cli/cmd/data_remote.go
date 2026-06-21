package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const remoteWriteBatchSize = 1000

// retInfoResponse 定义远端接口响应中读取 RetInfo 的公共能力。
type retInfoResponse interface {
	GetRetInfo() *pb.RetInfo
}

func importCSVRowsRemote(ctx context.Context, storageURL string, spaceID string, dataSourceID string, datasetID string, subjectID string, freq string, rows []*pb.TimeSeriesRow) error {
	if err := ensureRemoteDataset(ctx, storageURL, spaceID, dataSourceID, datasetID, subjectID, freq, rows); err != nil {
		return err
	}
	for start := 0; start < len(rows); start += remoteWriteBatchSize {
		end := start + remoteWriteBatchSize
		if end > len(rows) {
			end = len(rows)
		}
		if err := postStorage(ctx, storageURL, accessServiceName, "WriteTimeSeriesRows", &pb.WriteTimeSeriesRowsReq{
			Rows: rows[start:end],
		}, &pb.WriteTimeSeriesRowsRsp{}); err != nil {
			return err
		}
	}
	return nil
}

func exportRowsRemote(ctx context.Context, storageURL string, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	rsp := &pb.ReadTimeSeriesRowsRsp{}
	if err := postStorage(ctx, storageURL, accessServiceName, "ReadTimeSeriesRows", req, rsp); err != nil {
		return nil, err
	}
	return rsp, nil
}

func ensureRemoteDataset(ctx context.Context, storageURL string, spaceID string, dataSourceID string, datasetID string, subjectID string, freq string, rows []*pb.TimeSeriesRow) error {
	calls := []struct {
		method string
		req    proto.Message
		rsp    proto.Message
	}{
		{"CreateSpace", &pb.CreateSpaceReq{Space: &pb.Space{SpaceId: spaceID, Name: spaceID}}, &pb.CreateSpaceRsp{}},
		{"CreateDataSource", &pb.CreateDataSourceReq{DataSource: &pb.DataSource{SpaceId: spaceID, DataSourceId: dataSourceID, Name: dataSourceID, Kind: "file_import", Status: "active"}}, &pb.CreateDataSourceRsp{}},
		{"UpsertSubject", &pb.UpsertSubjectReq{Subject: &pb.Subject{SpaceId: spaceID, SubjectId: subjectID, SubjectType: "instrument", Name: subjectID, Status: "active"}}, &pb.UpsertSubjectRsp{}},
		{"CreateDataset", &pb.CreateDatasetReq{Dataset: &pb.Dataset{SpaceId: spaceID, DatasetId: datasetID, DataSourceId: dataSourceID, Name: datasetID, DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{freq}, Status: "active"}}, &pb.CreateDatasetRsp{}},
		{"BindDatasetSubject", &pb.BindDatasetSubjectReq{DatasetSubject: &pb.DatasetSubject{SpaceId: spaceID, DatasetId: datasetID, SubjectId: subjectID, Status: "active"}}, &pb.BindDatasetSubjectRsp{}},
		{"CreatePrimaryStoreNode", &pb.CreatePrimaryStoreNodeReq{Node: &pb.PrimaryStoreNode{NodeId: "local", Name: "local", Endpoint: "local", Status: "active"}}, &pb.CreatePrimaryStoreNodeRsp{}},
		{"CreatePrimaryStoreRoute", &pb.CreatePrimaryStoreRouteReq{PrimaryStoreRoute: &pb.PrimaryStoreRoute{SpaceId: spaceID, RouteId: "route_" + datasetID, DatasetId: datasetID, SubjectPattern: "*", NodeId: "local", Priority: 100, Status: "active"}}, &pb.CreatePrimaryStoreRouteRsp{}},
	}
	for _, call := range calls {
		if err := postStorage(ctx, storageURL, "trpc.storage.metadata.MetadataService", call.method, call.req, call.rsp); err != nil {
			return err
		}
	}
	for columnName, valueType := range inferColumnTypes(rows) {
		if err := postStorage(ctx, storageURL, "trpc.storage.metadata.MetadataService", "UpsertDatasetColumn", &pb.UpsertDatasetColumnReq{Column: &pb.DatasetColumn{
			SpaceId:    spaceID,
			DatasetId:  datasetID,
			ColumnName: columnName,
			OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD,
			OriginId:   columnName,
			ValueType:  valueType,
			Status:     "active",
		}}, &pb.UpsertDatasetColumnRsp{}); err != nil {
			return err
		}
	}
	return nil
}

func inferColumnTypes(rows []*pb.TimeSeriesRow) map[string]pb.FieldValueType {
	types := make(map[string]pb.FieldValueType)
	for _, row := range rows {
		for _, column := range row.GetColumns() {
			if _, ok := types[column.GetColumnName()]; ok {
				continue
			}
			valueType := column.GetValueType()
			if valueType == pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED {
				valueType = pb.FieldValueType_FIELD_VALUE_TYPE_STRING
			}
			types[column.GetColumnName()] = valueType
		}
	}
	return types
}

func postStorage(ctx context.Context, storageURL string, service string, method string, req proto.Message, rsp proto.Message) error {
	if err := postStorageRaw(ctx, storageURL, service, method, req, rsp); err != nil {
		return err
	}
	return checkStorageRetInfo(service, method, rsp)
}

func postStorageRaw(ctx context.Context, storageURL string, service string, method string, req proto.Message, rsp proto.Message) error {
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(req)
	if err != nil {
		return err
	}
	url := strings.TrimRight(storageURL, "/") + "/" + service + "/" + method
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 60 * time.Second}
	httpRsp, err := client.Do(httpReq)
	if err != nil {
		return err
	}
	defer httpRsp.Body.Close()
	body, _ := io.ReadAll(httpRsp.Body)
	if httpRsp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s/%s HTTP %d: %s", service, method, httpRsp.StatusCode, string(body))
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(body, rsp); err != nil {
		return err
	}
	return nil
}

func checkStorageRetInfo(service string, method string, rsp proto.Message) error {
	retInfo, ok := responseRetInfo(rsp)
	if !ok {
		return nil
	}
	if retInfo == nil {
		return fmt.Errorf("%s/%s failed: missing ret_info", service, method)
	}
	if retInfo.GetCode() != pb.ErrorCode_SUCCESS {
		return fmt.Errorf("%s/%s failed: %s", service, method, retInfo.GetMsg())
	}
	return nil
}

func responseRetInfo(rsp proto.Message) (*pb.RetInfo, bool) {
	withRet, ok := rsp.(retInfoResponse)
	if !ok {
		return nil, false
	}
	return withRet.GetRetInfo(), true
}
