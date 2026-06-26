package cmd

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
)

var (
	dataStorageURL  string
	dataSpaceID     string
	dataSourceID    string
	dataSourceName  string
	dataCSVFile     string
	dataDatasetID   string
	dataDatasetName string
	dataSubjectID   string
	dataSubjectName string
	dataFreq        string
	dataTimeColumn  string
	dataDimensions  []string
	dataFieldConfig string
	dataOutputFile  string
	dataStartTime   string
	dataEndTime     string
	dataPageSize    uint32
)

var dataCmd = &cobra.Command{
	Use:   "data",
	Short: "量化数据读写工具",
}

var dataCSVCmd = &cobra.Command{
	Use:   "csv",
	Short: "CSV 数据导入工具",
}

var dataCSVImportCmd = &cobra.Command{
	Use:   "import",
	Short: "导入 CSV K 线数据",
	RunE: func(cmd *cobra.Command, args []string) error {
		if dataCSVFile == "" {
			return fmt.Errorf("必须指定 --file")
		}
		datasetID, err := requiredFlagValue(dataDatasetID, "--dataset")
		if err != nil {
			return err
		}
		dataSourceID, err := requiredFlagValue(dataSourceID, "--data-source")
		if err != nil {
			return err
		}
		subjectID := dataSubjectID
		if subjectID == "" {
			subjectID = strings.TrimSuffix(filepath.Base(dataCSVFile), filepath.Ext(dataCSVFile))
		}
		rows, err := readCSVRows(dataCSVFile, &pb.TimeSeriesKey{
			SpaceId:    defaultFlag(dataSpaceID, "default"),
			DatasetId:  datasetID,
			SubjectId:  subjectID,
			Freq:       defaultFlag(dataFreq, "1m"),
			Dimensions: parseDimensions(dataDimensions),
		}, defaultFlag(dataTimeColumn, "candle_begin_time"))
		if err != nil {
			return err
		}
		if dataStorageURL != "" {
			if err := importCSVRowsRemote(context.Background(), dataStorageURL, remoteImportOptions{
				SpaceID:         defaultFlag(dataSpaceID, "default"),
				DataSourceID:    dataSourceID,
				DataSourceName:  dataSourceName,
				DatasetID:       datasetID,
				DatasetName:     dataDatasetName,
				SubjectID:       subjectID,
				SubjectName:     dataSubjectName,
				Freq:            defaultFlag(dataFreq, "1m"),
				FieldConfigPath: dataFieldConfig,
			}, rows); err != nil {
				return err
			}
			fmt.Printf("imported dataset=%s subject=%s rows=%d storage_url=%s\n", datasetID, subjectID, len(rows), dataStorageURL)
			return nil
		}
		return fmt.Errorf("必须指定 --storage-url，通过 moox-storage Access Service 写入")
	},
}

var dataRowsCmd = &cobra.Command{
	Use:   "rows",
	Short: "Dataset 行读取工具",
}

var dataRowsExportCmd = &cobra.Command{
	Use:   "export",
	Short: "导出 Dataset 行为 JSON",
	RunE: func(cmd *cobra.Command, args []string) error {
		datasetID, err := requiredFlagValue(dataDatasetID, "--dataset")
		if err != nil {
			return err
		}
		if dataStorageURL != "" {
			rsp, err := exportRowsRemote(context.Background(), dataStorageURL, &pb.ReadTimeSeriesRowsReq{
				Keys: []*pb.TimeSeriesKey{{
					SpaceId:    defaultFlag(dataSpaceID, "default"),
					DatasetId:  datasetID,
					SubjectId:  dataSubjectID,
					Freq:       dataFreq,
					Dimensions: parseDimensions(dataDimensions),
				}},
				TimeRange: &pb.TimeRange{
					StartTime: dataStartTime,
					EndTime:   dataEndTime,
				},
				Page: &pb.Page{Page: 1, Size: dataPageSize},
			})
			if err != nil {
				return err
			}
			return writeRowsExport(rsp, dataOutputFile, dataStorageURL, datasetID, dataSubjectID)
		}
		return fmt.Errorf("必须指定 --storage-url，通过 moox-storage Access Service 读取")
	},
}

func init() {
	rootCmd.AddCommand(dataCmd)
	dataCmd.AddCommand(dataCSVCmd)
	dataCmd.AddCommand(dataRowsCmd)
	dataCSVCmd.AddCommand(dataCSVImportCmd)
	dataRowsCmd.AddCommand(dataRowsExportCmd)

	dataCSVImportCmd.Flags().StringVar(&dataStorageURL, "storage-url", "", "远端 moox-storage HTTP 地址，例如 http://127.0.0.1:19104")
	dataCSVImportCmd.Flags().StringVar(&dataSpaceID, "space", "default", "Space ID")
	dataCSVImportCmd.Flags().StringVar(&dataSpaceID, "workspace", "default", "Space ID，兼容旧参数名")
	dataCSVImportCmd.Flags().StringVar(&dataSourceID, "data-source", "", "DataSource ID")
	dataCSVImportCmd.Flags().StringVar(&dataSourceName, "data-source-name", "", "DataSource 中文名，留空默认使用“导入来源”")
	dataCSVImportCmd.Flags().StringVar(&dataCSVFile, "file", "", "CSV 文件路径")
	dataCSVImportCmd.Flags().StringVar(&dataDatasetID, "dataset", "", "Dataset ID")
	dataCSVImportCmd.Flags().StringVar(&dataDatasetName, "dataset-name", "", "Dataset 中文名，留空默认使用“导入K线”")
	dataCSVImportCmd.Flags().StringVar(&dataSubjectID, "subject", "", "Subject ID，默认取文件名")
	dataCSVImportCmd.Flags().StringVar(&dataSubjectName, "subject-name", "", "Subject 中文名，留空默认使用“导入标的”")
	dataCSVImportCmd.Flags().StringVar(&dataFreq, "freq", "1m", "K 线频率")
	dataCSVImportCmd.Flags().StringVar(&dataTimeColumn, "time-column", "candle_begin_time", "时间列名")
	dataCSVImportCmd.Flags().StringArrayVar(&dataDimensions, "dimension", nil, "自定义维度，格式 name=value，可重复")
	dataCSVImportCmd.Flags().StringVar(&dataFieldConfig, "field-config", "", "字段展示名 YAML 配置路径，默认读取 config/fields.yaml")

	dataRowsExportCmd.Flags().StringVar(&dataStorageURL, "storage-url", "", "远端 moox-storage HTTP 地址，例如 http://127.0.0.1:19104")
	dataRowsExportCmd.Flags().StringVar(&dataSpaceID, "space", "default", "Space ID")
	dataRowsExportCmd.Flags().StringVar(&dataSpaceID, "workspace", "default", "Space ID，兼容旧参数名")
	dataRowsExportCmd.Flags().StringVar(&dataDatasetID, "dataset", "", "Dataset ID")
	dataRowsExportCmd.Flags().StringVar(&dataSubjectID, "subject", "", "Subject ID")
	dataRowsExportCmd.Flags().StringVar(&dataFreq, "freq", "1m", "K 线频率")
	dataRowsExportCmd.Flags().StringArrayVar(&dataDimensions, "dimension", nil, "自定义维度，格式 name=value，可重复")
	dataRowsExportCmd.Flags().StringVar(&dataStartTime, "start-time", "", "起始时间")
	dataRowsExportCmd.Flags().StringVar(&dataEndTime, "end-time", "", "结束时间")
	dataRowsExportCmd.Flags().Uint32Var(&dataPageSize, "page-size", 1000, "最多导出行数")
	dataRowsExportCmd.Flags().StringVar(&dataOutputFile, "output", "", "输出 JSON 文件；为空则输出到 stdout")
}

func writeRowsExport(rsp *pb.ReadTimeSeriesRowsRsp, outputFile string, source string, datasetID string, subjectID string) error {
	raw, err := protojson.MarshalOptions{UseProtoNames: true, Multiline: true}.Marshal(rsp)
	if err != nil {
		return err
	}
	if outputFile == "" {
		fmt.Println(string(raw))
		return nil
	}
	if err := os.WriteFile(outputFile, raw, 0o600); err != nil {
		return err
	}
	fmt.Printf("exported dataset=%s subject=%s rows=%d source=%s output=%s\n", datasetID, subjectID, len(rsp.GetRows()), source, outputFile)
	return nil
}

func readCSVRows(path string, key *pb.TimeSeriesKey, timeColumn string) ([]*pb.TimeSeriesRow, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true

	var header []string
	timeIndex := -1
	for {
		record, err := reader.Read()
		if err != nil {
			if err == io.EOF {
				return nil, fmt.Errorf("CSV header with time column %q not found", timeColumn)
			}
			return nil, err
		}
		for index, name := range record {
			if strings.TrimSpace(name) == timeColumn {
				header = normalizeHeader(record)
				timeIndex = index
				break
			}
		}
		if timeIndex >= 0 {
			break
		}
	}

	var rows []*pb.TimeSeriesRow
	for {
		record, err := reader.Read()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if timeIndex >= len(record) || strings.TrimSpace(record[timeIndex]) == "" {
			continue
		}
		row := &pb.TimeSeriesRow{
			Key: cloneCSVTimeSeriesKey(key, strings.TrimSpace(record[timeIndex])),
		}
		for index, name := range header {
			if index >= len(record) || name == "" || name == timeColumn {
				continue
			}
			value := strings.TrimSpace(record[index])
			if value == "" {
				continue
			}
			row.Columns = append(row.Columns, csvColumnValue(name, value))
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("CSV %s has no data rows", path)
	}
	return rows, nil
}

func cloneCSVTimeSeriesKey(key *pb.TimeSeriesKey, dataTime string) *pb.TimeSeriesKey {
	cloned := &pb.TimeSeriesKey{
		SpaceId:   key.GetSpaceId(),
		DatasetId: key.GetDatasetId(),
		SubjectId: key.GetSubjectId(),
		Freq:      key.GetFreq(),
		DataTime:  dataTime,
	}
	if len(key.GetDimensions()) > 0 {
		cloned.Dimensions = make(map[string]string, len(key.GetDimensions()))
		for name, value := range key.GetDimensions() {
			cloned.Dimensions[name] = value
		}
	}
	return cloned
}

func normalizeHeader(record []string) []string {
	header := make([]string, len(record))
	for index, name := range record {
		header[index] = strings.TrimSpace(name)
	}
	return header
}

func csvColumnValue(name string, value string) *pb.ColumnValue {
	if parsed, err := strconv.ParseFloat(value, 64); err == nil {
		return &pb.ColumnValue{
			ColumnName: name,
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
			Value:      &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: parsed}},
		}
	}
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		Value:      &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: value}},
	}
}

func parseDimensions(items []string) map[string]string {
	values := make(map[string]string, len(items))
	for _, item := range items {
		name, value, ok := strings.Cut(item, "=")
		if !ok || strings.TrimSpace(name) == "" {
			continue
		}
		values[strings.TrimSpace(name)] = strings.TrimSpace(value)
	}
	return values
}

func defaultFlag(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func requiredFlagValue(value string, flagName string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", fmt.Errorf("必须指定 %s", flagName)
	}
	return trimmed, nil
}
