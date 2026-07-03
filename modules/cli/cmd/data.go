package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
)

var (
	dataStorageURL  string
	dataSpaceID     string
	dataDatasetID   string
	dataSubjectID   string
	dataFreq        string
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
	dataCmd.AddCommand(dataRowsCmd)
	dataRowsCmd.AddCommand(dataRowsExportCmd)

	dataRowsExportCmd.Flags().StringVar(&dataStorageURL, "storage-url", "", "远端 moox-storage HTTP 地址，例如 http://127.0.0.1:20201")
	dataRowsExportCmd.Flags().StringVar(&dataSpaceID, "space", "default", "Space ID")
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
