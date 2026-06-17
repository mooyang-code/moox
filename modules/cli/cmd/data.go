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

	"github.com/mooyang-code/moox/modules/storage/pkg/quantstore"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
)

var (
	dataStorageRoot string
	dataStorageURL  string
	dataSpaceID     string
	dataSourceID    string
	dataCSVFile     string
	dataDatasetID   string
	dataSubjectID   string
	dataFreq        string
	dataTimeColumn  string
	dataDimensions  []string
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
		subjectID := dataSubjectID
		if subjectID == "" {
			subjectID = strings.TrimSuffix(filepath.Base(dataCSVFile), filepath.Ext(dataCSVFile))
		}
		rows, err := readCSVRows(dataCSVFile, &pb.DataScope{
			SpaceId:    defaultFlag(dataSpaceID, "default"),
			DatasetId:  defaultFlag(dataDatasetID, "binance_spot_kline_1m"),
			SubjectId:  subjectID,
			Freq:       defaultFlag(dataFreq, "1m"),
			Dimensions: parseDimensions(dataDimensions),
		}, defaultFlag(dataTimeColumn, "candle_begin_time"))
		if err != nil {
			return err
		}
		if dataStorageURL != "" {
			if err := importCSVRowsRemote(context.Background(), dataStorageURL, defaultFlag(dataSpaceID, "default"), defaultFlag(dataSourceID, "binance"), defaultFlag(dataDatasetID, "binance_spot_kline_1m"), subjectID, defaultFlag(dataFreq, "1m"), rows); err != nil {
				return err
			}
			fmt.Printf("imported dataset=%s subject=%s rows=%d storage_url=%s\n", defaultFlag(dataDatasetID, "binance_spot_kline_1m"), subjectID, len(rows), dataStorageURL)
			return nil
		}
		store := quantstore.New(dataStorageRoot)
		if err := store.WriteRows(context.Background(), rows, pb.WriteMode_WRITE_MODE_UPSERT); err != nil {
			return err
		}
		fmt.Printf("imported dataset=%s subject=%s rows=%d root=%s\n", defaultFlag(dataDatasetID, "binance_spot_kline_1m"), subjectID, len(rows), store.Root())
		return nil
	},
}

var dataRowsCmd = &cobra.Command{
	Use:   "rows",
	Short: "DataSet 行读取工具",
}

var dataRowsExportCmd = &cobra.Command{
	Use:   "export",
	Short: "导出 DataSet 行为 JSON",
	RunE: func(cmd *cobra.Command, args []string) error {
		if dataStorageURL != "" {
			rsp, err := exportRowsRemote(context.Background(), dataStorageURL, &pb.ReadRowsReq{
				Scope: &pb.DataScope{
					SpaceId:    defaultFlag(dataSpaceID, "default"),
					DatasetId:  defaultFlag(dataDatasetID, "binance_spot_kline_1m"),
					SubjectId:  dataSubjectID,
					Freq:       dataFreq,
					Dimensions: parseDimensions(dataDimensions),
				},
				ReadMode: pb.ReadMode_READ_MODE_RANGE,
				TimeRange: &pb.TimeRange{
					StartTime:      dataStartTime,
					StartInclusive: true,
					EndTime:        dataEndTime,
					EndInclusive:   true,
				},
				Page: &pb.Page{Page: 1, Size: dataPageSize},
			})
			if err != nil {
				return err
			}
			return writeRowsExport(rsp, dataOutputFile, dataStorageURL, defaultFlag(dataDatasetID, "binance_spot_kline_1m"), dataSubjectID)
		}
		store := quantstore.New(dataStorageRoot)
		rows, page, err := store.ReadRows(context.Background(), &pb.DataScope{
			SpaceId:    defaultFlag(dataSpaceID, "default"),
			DatasetId:  defaultFlag(dataDatasetID, "binance_spot_kline_1m"),
			SubjectId:  dataSubjectID,
			Freq:       dataFreq,
			Dimensions: parseDimensions(dataDimensions),
		}, pb.ReadMode_READ_MODE_RANGE, &pb.TimeRange{
			StartTime:      dataStartTime,
			StartInclusive: true,
			EndTime:        dataEndTime,
			EndInclusive:   true,
		}, "", nil, nil, &pb.Page{Page: 1, Size: dataPageSize})
		if err != nil {
			return err
		}
		return writeRowsExport(&pb.ReadRowsRsp{
			RetInfo:    quantstore.Success("success"),
			Rows:       rows,
			PageResult: page,
		}, dataOutputFile, quantstore.New(dataStorageRoot).Root(), defaultFlag(dataDatasetID, "binance_spot_kline_1m"), dataSubjectID)
	},
}

func init() {
	rootCmd.AddCommand(dataCmd)
	dataCmd.AddCommand(dataCSVCmd)
	dataCmd.AddCommand(dataRowsCmd)
	dataCSVCmd.AddCommand(dataCSVImportCmd)
	dataRowsCmd.AddCommand(dataRowsExportCmd)

	dataCSVImportCmd.Flags().StringVar(&dataStorageRoot, "storage-root", "", "本地存储根目录，默认读取 MOOX_STORAGE_HOME 或 var/storage")
	dataCSVImportCmd.Flags().StringVar(&dataStorageURL, "storage-url", "", "远端 moox-storage HTTP 地址，例如 http://127.0.0.1:19104")
	dataCSVImportCmd.Flags().StringVar(&dataSpaceID, "space", "default", "Space ID")
	dataCSVImportCmd.Flags().StringVar(&dataSpaceID, "workspace", "default", "Space ID，兼容旧参数名")
	dataCSVImportCmd.Flags().StringVar(&dataSourceID, "data-source", "binance", "DataSource ID")
	dataCSVImportCmd.Flags().StringVar(&dataCSVFile, "file", "", "CSV 文件路径")
	dataCSVImportCmd.Flags().StringVar(&dataDatasetID, "dataset", "binance_spot_kline_1m", "DataSet ID")
	dataCSVImportCmd.Flags().StringVar(&dataSubjectID, "subject", "", "Subject ID，默认取文件名")
	dataCSVImportCmd.Flags().StringVar(&dataFreq, "freq", "1m", "K 线频率")
	dataCSVImportCmd.Flags().StringVar(&dataTimeColumn, "time-column", "candle_begin_time", "时间列名")
	dataCSVImportCmd.Flags().StringArrayVar(&dataDimensions, "dimension", nil, "自定义维度，格式 name=value，可重复")

	dataRowsExportCmd.Flags().StringVar(&dataStorageRoot, "storage-root", "", "本地存储根目录，默认读取 MOOX_STORAGE_HOME 或 var/storage")
	dataRowsExportCmd.Flags().StringVar(&dataStorageURL, "storage-url", "", "远端 moox-storage HTTP 地址，例如 http://127.0.0.1:19104")
	dataRowsExportCmd.Flags().StringVar(&dataSpaceID, "space", "default", "Space ID")
	dataRowsExportCmd.Flags().StringVar(&dataSpaceID, "workspace", "default", "Space ID，兼容旧参数名")
	dataRowsExportCmd.Flags().StringVar(&dataDatasetID, "dataset", "binance_spot_kline_1m", "DataSet ID")
	dataRowsExportCmd.Flags().StringVar(&dataSubjectID, "subject", "", "Subject ID")
	dataRowsExportCmd.Flags().StringVar(&dataFreq, "freq", "1m", "K 线频率")
	dataRowsExportCmd.Flags().StringArrayVar(&dataDimensions, "dimension", nil, "自定义维度，格式 name=value，可重复")
	dataRowsExportCmd.Flags().StringVar(&dataStartTime, "start-time", "", "起始时间")
	dataRowsExportCmd.Flags().StringVar(&dataEndTime, "end-time", "", "结束时间")
	dataRowsExportCmd.Flags().Uint32Var(&dataPageSize, "page-size", 1000, "最多导出行数")
	dataRowsExportCmd.Flags().StringVar(&dataOutputFile, "output", "", "输出 JSON 文件；为空则输出到 stdout")
}

func writeRowsExport(rsp *pb.ReadRowsRsp, outputFile string, source string, datasetID string, subjectID string) error {
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

func readCSVRows(path string, scope *pb.DataScope, timeColumn string) ([]*pb.DataRow, error) {
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

	var rows []*pb.DataRow
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
		row := &pb.DataRow{
			Key: &pb.DataKey{
				Scope:    scope,
				DataTime: strings.TrimSpace(record[timeIndex]),
			},
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

func normalizeHeader(record []string) []string {
	header := make([]string, len(record))
	for index, name := range record {
		header[index] = strings.TrimSpace(name)
	}
	return header
}

func csvColumnValue(name string, value string) *pb.ColumnValue {
	if parsed, err := strconv.ParseFloat(value, 64); err == nil {
		return quantstore.DoubleValue(name, parsed)
	}
	return quantstore.StringValue(name, value)
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
