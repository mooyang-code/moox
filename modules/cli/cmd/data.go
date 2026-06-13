package cmd

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/pkg/quantstore"
	pb "github.com/mooyang-code/moox/modules/storage/proto/genv2"
	"github.com/spf13/cobra"
)

var (
	dataStorageRoot string
	dataCSVFile     string
	dataWorkspaceID string
	dataExchangeID  string
	dataDatasetID   string
	dataInstrument  string
	dataFreq        string
	dataTimeColumn  string
	dataDimensions  []string
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
		instrumentID := dataInstrument
		if instrumentID == "" {
			instrumentID = strings.TrimSuffix(filepath.Base(dataCSVFile), filepath.Ext(dataCSVFile))
		}
		opts := quantstore.CSVImportOptions{
			WorkspaceID:     defaultFlag(dataWorkspaceID, "default"),
			DatasetID:       defaultFlag(dataDatasetID, "binance_spot_kline_1m"),
			InstrumentID:    instrumentID,
			ExchangeID:      defaultFlag(dataExchangeID, "BINANCE"),
			Freq:            defaultFlag(dataFreq, "1m"),
			TimeColumn:      defaultFlag(dataTimeColumn, "candle_begin_time"),
			DimensionValues: parseDimensions(dataDimensions),
		}
		store := quantstore.New(dataStorageRoot)
		affected, err := store.ImportCSV(context.Background(), dataCSVFile, opts)
		if err != nil {
			return err
		}
		fmt.Printf("imported rows=%d workspace=%s dataset=%s instrument=%s root=%s\n",
			affected, opts.WorkspaceID, opts.DatasetID, opts.InstrumentID, store.Root())
		return nil
	},
}

func init() {
	rootCmd.AddCommand(dataCmd)
	dataCmd.AddCommand(dataCSVCmd)
	dataCSVCmd.AddCommand(dataCSVImportCmd)

	dataCSVImportCmd.Flags().StringVar(&dataStorageRoot, "storage-root", "", "本地存储根目录，默认读取 MOOX_STORAGE_HOME 或 var/storage")
	dataCSVImportCmd.Flags().StringVar(&dataCSVFile, "file", "", "CSV 文件路径")
	dataCSVImportCmd.Flags().StringVar(&dataWorkspaceID, "workspace", "default", "Workspace ID")
	dataCSVImportCmd.Flags().StringVar(&dataExchangeID, "exchange", "BINANCE", "Exchange ID")
	dataCSVImportCmd.Flags().StringVar(&dataDatasetID, "dataset", "binance_spot_kline_1m", "DataSet ID")
	dataCSVImportCmd.Flags().StringVar(&dataInstrument, "instrument", "", "Instrument ID，默认取文件名")
	dataCSVImportCmd.Flags().StringVar(&dataFreq, "freq", "1m", "K 线频率")
	dataCSVImportCmd.Flags().StringVar(&dataTimeColumn, "time-column", "candle_begin_time", "时间列名")
	dataCSVImportCmd.Flags().StringArrayVar(&dataDimensions, "dimension", nil, "自定义维度，格式 name=value，可重复")
}

func parseDimensions(items []string) []*pb.DimensionValue {
	values := make([]*pb.DimensionValue, 0, len(items))
	for _, item := range items {
		name, value, ok := strings.Cut(item, "=")
		if !ok || strings.TrimSpace(name) == "" {
			continue
		}
		values = append(values, &pb.DimensionValue{Name: strings.TrimSpace(name), Value: strings.TrimSpace(value)})
	}
	return values
}

func defaultFlag(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
