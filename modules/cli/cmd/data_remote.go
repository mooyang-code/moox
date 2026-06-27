package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"
)

const remoteWriteBatchSize = 1000

// remoteImportOptions 汇总 CSV 远端导入时自动补齐元数据所需的参数。
type remoteImportOptions struct {
	SpaceID         string
	DataSourceID    string
	DataSourceName  string
	DatasetID       string
	DatasetName     string
	SubjectID       string
	SubjectName     string
	Freq            string
	FieldConfigPath string
}

// retInfoResponse 定义远端接口响应中读取 RetInfo 的公共能力。
type retInfoResponse interface {
	GetRetInfo() *pb.RetInfo
}

func importCSVRowsRemote(ctx context.Context, storageURL string, options remoteImportOptions, rows []*pb.TimeSeriesRow) error {
	if err := ensureRemoteDataset(ctx, storageURL, options, rows); err != nil {
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

func ensureRemoteDataset(ctx context.Context, storageURL string, options remoteImportOptions, rows []*pb.TimeSeriesRow) error {
	dataSourceName, err := resolveChineseDisplayName("数据源", options.DataSourceName, "导入来源")
	if err != nil {
		return err
	}
	datasetName, err := resolveChineseDisplayName("数据集", options.DatasetName, "导入K线")
	if err != nil {
		return err
	}
	subjectName, err := resolveChineseDisplayName("数据对象", options.SubjectName, "导入标的")
	if err != nil {
		return err
	}
	columnDisplayNames, err := loadColumnDisplayNames(options.FieldConfigPath)
	if err != nil {
		return err
	}
	calls := []struct {
		method string
		req    proto.Message
		rsp    proto.Message
	}{
		{"CreateSpace", &pb.CreateSpaceReq{Space: &pb.Space{SpaceId: options.SpaceID, Name: "导入空间"}}, &pb.CreateSpaceRsp{}},
		{"CreateDataSource", &pb.CreateDataSourceReq{DataSource: &pb.DataSource{SpaceId: options.SpaceID, DataSourceId: options.DataSourceID, Name: dataSourceName, Kind: "file_import", Status: "active"}}, &pb.CreateDataSourceRsp{}},
		{"UpsertSubject", &pb.UpsertSubjectReq{Subject: &pb.Subject{SpaceId: options.SpaceID, SubjectId: options.SubjectID, SubjectType: "instrument", Name: subjectName, Status: "active"}}, &pb.UpsertSubjectRsp{}},
		{"CreateDataset", &pb.CreateDatasetReq{Dataset: &pb.Dataset{SpaceId: options.SpaceID, DatasetId: options.DatasetID, DataSourceId: options.DataSourceID, Name: datasetName, DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{options.Freq}, Status: "active"}}, &pb.CreateDatasetRsp{}},
		{"BindDatasetSubject", &pb.BindDatasetSubjectReq{DatasetSubject: &pb.DatasetSubject{SpaceId: options.SpaceID, DatasetId: options.DatasetID, SubjectId: options.SubjectID, Status: "active"}}, &pb.BindDatasetSubjectRsp{}},
		{"CreatePrimaryStoreNode", &pb.CreatePrimaryStoreNodeReq{Node: &pb.PrimaryStoreNode{NodeId: "local", Name: "local", Endpoint: "local", Status: "active"}}, &pb.CreatePrimaryStoreNodeRsp{}},
		{"CreatePrimaryStoreRoute", &pb.CreatePrimaryStoreRouteReq{PrimaryStoreRoute: &pb.PrimaryStoreRoute{SpaceId: options.SpaceID, RouteId: "route_" + options.DatasetID, DatasetId: options.DatasetID, SubjectPattern: "*", NodeId: "local", Priority: 100, Status: "active"}}, &pb.CreatePrimaryStoreRouteRsp{}},
	}
	for _, call := range calls {
		if err := postStorage(ctx, storageURL, "trpc.storage.metadata.Metadata", call.method, call.req, call.rsp); err != nil {
			return err
		}
	}
	for columnName, valueType := range inferColumnTypes(rows) {
		if err := postStorage(ctx, storageURL, "trpc.storage.metadata.Metadata", "UpsertDatasetColumn", &pb.UpsertDatasetColumnReq{Column: &pb.DatasetColumn{
			SpaceId:    options.SpaceID,
			DatasetId:  options.DatasetID,
			ColumnName: columnName,
			OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD,
			OriginId:   columnName,
			ValueType:  valueType,
			Status:     "active",
			Attributes: map[string]string{"display_name": remoteColumnDisplayName(columnDisplayNames, columnName)},
		}}, &pb.UpsertDatasetColumnRsp{}); err != nil {
			return err
		}
	}
	return nil
}

func resolveChineseDisplayName(field string, value string, fallback string) (string, error) {
	name := strings.TrimSpace(value)
	if name == "" {
		name = fallback
	}
	if utf8.RuneCountInString(name) > 10 {
		return "", fmt.Errorf("%s中文名不能超过10个字符", field)
	}
	for _, r := range name {
		if unicode.Is(unicode.Han, r) {
			return name, nil
		}
	}
	return "", fmt.Errorf("%s中文名必须包含中文", field)
}

type columnDisplayNameConfig struct {
	ColumnDisplayNames map[string]string `yaml:"column_display_names"`
}

func loadColumnDisplayNames(path string) (map[string]string, error) {
	resolved, required, err := resolveColumnDisplayNameConfigPath(path)
	if err != nil {
		return nil, err
	}
	if resolved == "" {
		return map[string]string{}, nil
	}
	raw, err := os.ReadFile(resolved)
	if err != nil {
		if required {
			return nil, fmt.Errorf("读取字段展示名配置失败 %s: %w", resolved, err)
		}
		return map[string]string{}, nil
	}
	var config columnDisplayNameConfig
	if err := yaml.Unmarshal(raw, &config); err != nil {
		return nil, fmt.Errorf("解析字段展示名配置失败 %s: %w", resolved, err)
	}
	out := make(map[string]string, len(config.ColumnDisplayNames))
	for columnName, displayName := range config.ColumnDisplayNames {
		columnName = strings.TrimSpace(columnName)
		if columnName == "" {
			continue
		}
		name, err := resolveChineseDisplayName("列", displayName, "")
		if err != nil {
			return nil, fmt.Errorf("字段展示名配置 %s.%s 无效: %w", resolved, columnName, err)
		}
		out[columnName] = name
	}
	return out, nil
}

func resolveColumnDisplayNameConfigPath(path string) (string, bool, error) {
	if strings.TrimSpace(path) != "" {
		return path, true, nil
	}
	candidates := []string{}
	if envPath := strings.TrimSpace(os.Getenv("MOOX_FIELD_CONFIG")); envPath != "" {
		candidates = append(candidates, envPath)
	}
	candidates = append(candidates,
		"config/fields.yaml",
		"modules/cli/config/fields.yaml",
		filepath.Join("..", "config", "fields.yaml"),
	)
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, false, nil
		} else if !os.IsNotExist(err) {
			return "", false, err
		}
	}
	return "", false, nil
}

func remoteColumnDisplayName(displayNames map[string]string, columnName string) string {
	if name := displayNames[strings.TrimSpace(columnName)]; name != "" {
		return name
	}
	return "导入列"
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
