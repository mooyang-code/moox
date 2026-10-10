package command

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/security"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
)

var (
	dataManifest        string
	dataSpaceID         string
	dataDatasetID       string
	dataSubjectID       string
	dataFreq            string
	dataSeriesTag       string
	dataOutputFile      string
	dataStartTime       string
	dataEndTime         string
	dataPageSize        uint32
	dataStorageAuthFile string
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
		auth, err := dataPrimaryAuth(dataStorageAuthFile)
		if err != nil {
			return err
		}
		gateway, err := openCommandGateway(cmd.Context(), dataManifest, nil)
		if err != nil {
			return err
		}
		defer gateway.Close()
		selector := timeSeriesSelectorForExport(datasetID, cmd.Flags().Changed("series-tag"))
		rsp, err := exportRowsRemote(cmd.Context(), gateway, &pb.ReadTimeSeriesRowsReq{
			AuthInfo: auth, SpaceId: defaultFlag(dataSpaceID, "default"), DatasetId: datasetID,
			Selectors: []*pb.TimeSeriesSelector{selector},
			TimeRange: &pb.TimeRange{StartTime: dataStartTime, EndTime: dataEndTime},
			Page:      &pb.Page{Page: 1, Size: dataPageSize},
		})
		if err != nil {
			return err
		}
		return writeRowsExport(rsp, dataOutputFile, accessServiceName, datasetID, dataSubjectID)
	},
}

func timeSeriesSelectorForExport(datasetID string, exactSeriesTag bool) *pb.TimeSeriesSelector {
	selector := &pb.TimeSeriesSelector{
		SpaceId: defaultFlag(dataSpaceID, "default"), DatasetId: datasetID,
		SubjectId: dataSubjectID, Freq: dataFreq,
	}
	if exactSeriesTag {
		selector.SeriesTag = &dataSeriesTag
	}
	return selector
}

func init() {
	rootCmd.AddCommand(dataCmd)
	dataCmd.AddCommand(dataRowsCmd)
	dataRowsCmd.AddCommand(dataRowsExportCmd)

	dataRowsExportCmd.Flags().StringVar(&dataManifest, "file", "./moox.toml", "操作员 SSH 网关使用的部署配置文件")
	dataRowsExportCmd.Flags().StringVar(&dataStorageAuthFile, "storage-auth-file", "secrets/storage-internal-auth.env", "Storage 内部鉴权文件")
	dataRowsExportCmd.Flags().StringVar(&dataSpaceID, "space", "default", "Space ID")
	dataRowsExportCmd.Flags().StringVar(&dataDatasetID, "dataset", "", "Dataset ID")
	dataRowsExportCmd.Flags().StringVar(&dataSubjectID, "subject", "", "Subject ID")
	dataRowsExportCmd.Flags().StringVar(&dataFreq, "freq", "1m", "K 线频率")
	dataRowsExportCmd.Flags().StringVar(&dataSeriesTag, "series-tag", "", "精确序列标签；省略表示全部序列，显式空值表示默认序列")
	dataRowsExportCmd.Flags().StringVar(&dataStartTime, "start-time", "", "起始时间")
	dataRowsExportCmd.Flags().StringVar(&dataEndTime, "end-time", "", "结束时间")
	dataRowsExportCmd.Flags().Uint32Var(&dataPageSize, "page-size", 1000, "最多导出行数")
	dataRowsExportCmd.Flags().StringVar(&dataOutputFile, "output", "", "输出 JSON 文件；为空则输出到 stdout")
}

func dataPrimaryAuth(path string) (*pb.AuthInfo, error) {
	return storagePrimaryRoleAuth(path, "moox-cli-data-export")
}

func storagePrimaryRoleAuth(path, appID string) (*pb.AuthInfo, error) {
	secret := strings.TrimSpace(os.Getenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET"))
	if secret == "" {
		raw, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			return nil, fmt.Errorf("读取 Storage 鉴权文件失败: %w", err)
		}
		normalized, err := normalizeStorageInternalAuth(string(raw))
		if err != nil {
			return nil, fmt.Errorf("Storage 鉴权文件无效: %w", err)
		}
		for _, line := range strings.Split(normalized, "\n") {
			if value, ok := strings.CutPrefix(line, "MOOX_STORAGE_PRIMARY_AUTH_SECRET="); ok {
				secret = value
				break
			}
		}
	}
	if secret == "" {
		return nil, fmt.Errorf("缺少 MOOX_STORAGE_PRIMARY_AUTH_SECRET")
	}
	return &pb.AuthInfo{AppId: appID, AppKey: security.HMACSHA256Hex(secret, []byte(appID))}, nil
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
