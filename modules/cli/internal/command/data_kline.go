package command

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"trpc.group/trpc-go/trpc-go/client"
)

type timeSeriesReader interface {
	ReadTimeSeriesRows(context.Context, *pb.ReadTimeSeriesRowsReq, ...client.Option) (*pb.ReadTimeSeriesRowsRsp, error)
}

type dataKlineDeps struct {
	loadConfig func(string) (dataAccessConfig, error)
	newReader  func(dataAccessConfig) timeSeriesReader
}

func defaultDataKlineDeps() dataKlineDeps {
	return dataKlineDeps{
		loadConfig: loadDataAccessConfig,
		newReader: func(cfg dataAccessConfig) timeSeriesReader {
			options := gatewayauth.NewTRPCClientOptions(
				strings.TrimSpace(cfg.Gateway.Target),
				strings.TrimSpace(cfg.Gateway.TargetNode),
				gatewayauth.Credentials{
					KeyID:  strings.TrimSpace(cfg.Gateway.KeyID),
					Caller: strings.TrimSpace(cfg.Gateway.Caller),
					Secret: cfg.Gateway.Secret,
				},
			)
			return pb.NewPrimaryStoreClientProxy(options...)
		},
	}
}

func newDataKlineGetCmd(deps dataKlineDeps) *cobra.Command {
	var (
		configPath string
		dataType   string
		exchange   string
		symbol     string
		interval   string
		limit      uint32
		startTime  string
		endTime    string
		timeout    time.Duration
		output     string
	)
	cmd := &cobra.Command{
		Use:          "get",
		Short:        "获取 K 线数据",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dataType, err := requiredFlagValue(dataType, "--data-type")
			if err != nil {
				return err
			}
			symbol, err := requiredFlagValue(symbol, "--symbol")
			if err != nil {
				return err
			}
			if limit < 1 || limit > 1000 {
				return fmt.Errorf("--limit 必须在 1..1000 范围内")
			}
			if timeout <= 0 {
				return fmt.Errorf("--timeout 必须大于 0")
			}
			startTime = strings.TrimSpace(startTime)
			endTime = strings.TrimSpace(endTime)
			if err := validateKlineTimeRange(startTime, endTime); err != nil {
				return err
			}

			cfg, err := deps.loadConfig(configPath)
			if err != nil {
				return err
			}
			if err := rejectInputOutputCollision(resolveDataAccessConfigPath(configPath), output); err != nil {
				return fmt.Errorf("write kline response: %w", err)
			}
			selection, err := cfg.resolveKline(dataType, exchange, interval)
			if err != nil {
				return err
			}
			seriesTag := selection.SeriesTag
			req := &pb.ReadTimeSeriesRowsReq{
				AuthInfo: &pb.AuthInfo{
					AppId:  strings.TrimSpace(cfg.Storage.AppID),
					AppKey: cfg.Storage.AppKey,
				},
				Selectors: []*pb.TimeSeriesSelector{{
					SpaceId:   selection.SpaceID,
					DatasetId: selection.DatasetID,
					SubjectId: symbol,
					Freq:      selection.Interval,
					SeriesTag: &seriesTag,
				}},
				TimeRange: &pb.TimeRange{StartTime: startTime, EndTime: endTime},
				Order:     pb.SortOrder_SORT_ORDER_DESC,
				Page:      &pb.Page{Page: 1, Size: limit},
				SpaceId:   selection.SpaceID,
				DatasetId: selection.DatasetID,
			}
			rpcCtx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			rsp, err := deps.newReader(cfg).ReadTimeSeriesRows(rpcCtx, req)
			if err != nil {
				return fmt.Errorf("PrimaryStore/ReadTimeSeriesRows RPC failed: %w", err)
			}
			if rsp == nil {
				return fmt.Errorf("PrimaryStore/ReadTimeSeriesRows RPC failed: empty response")
			}
			if err := checkStorageRetInfo("PrimaryStore", "ReadTimeSeriesRows", rsp); err != nil {
				return err
			}
			return writeKlineResponse(cmd, rsp, output)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "独立数据访问配置；默认读取 MOOX_SKILL_CONFIG 或 config/data-access.yaml")
	cmd.Flags().StringVar(&dataType, "data-type", "", "数据类型，例如 crypto")
	cmd.Flags().StringVar(&exchange, "exchange", "", "交易所；省略时使用数据类型配置的默认值")
	cmd.Flags().StringVar(&symbol, "symbol", "", "交易标的，例如 BTC-USDT")
	cmd.Flags().StringVar(&interval, "interval", "1m", "K 线周期")
	cmd.Flags().Uint32Var(&limit, "limit", 100, "返回条数，范围 1..1000")
	cmd.Flags().StringVar(&startTime, "start-time", "", "RFC3339 起始时间")
	cmd.Flags().StringVar(&endTime, "end-time", "", "RFC3339 结束时间")
	cmd.Flags().DurationVar(&timeout, "timeout", 15*time.Second, "RPC 超时时间，必须大于 0")
	cmd.Flags().StringVar(&output, "output", "", "输出 JSON 文件；为空则输出到 stdout")
	_ = cmd.MarkFlagRequired("data-type")
	_ = cmd.MarkFlagRequired("symbol")
	return cmd
}

func validateKlineTimeRange(start, end string) error {
	var startValue, endValue time.Time
	var err error
	if start != "" {
		startValue, err = time.Parse(time.RFC3339, start)
		if err != nil {
			return fmt.Errorf("--start-time 必须是 RFC3339 时间: %w", err)
		}
	}
	if end != "" {
		endValue, err = time.Parse(time.RFC3339, end)
		if err != nil {
			return fmt.Errorf("--end-time 必须是 RFC3339 时间: %w", err)
		}
	}
	if start != "" && end != "" && startValue.After(endValue) {
		return fmt.Errorf("--start-time 不能晚于 --end-time")
	}
	return nil
}

func writeKlineResponse(cmd *cobra.Command, rsp *pb.ReadTimeSeriesRowsRsp, output string) error {
	raw, err := protojson.MarshalOptions{UseProtoNames: true, Multiline: true, EmitUnpopulated: true}.Marshal(rsp)
	if err != nil {
		return fmt.Errorf("encode kline response: %w", err)
	}
	if strings.TrimSpace(output) == "" {
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), string(raw)); err != nil {
			return fmt.Errorf("write kline response: %w", err)
		}
		return nil
	}
	if err := writeAtomic0600(strings.TrimSpace(output), raw); err != nil {
		return fmt.Errorf("write kline response: %w", err)
	}
	return nil
}

func writeAtomic0600(path string, content []byte) (err error) {
	if info, statErr := os.Lstat(path); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("output %q must not be a symlink", path)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("output %q must be a regular file", path)
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() {
		temp.Close()
		if err != nil {
			os.Remove(tempPath)
		}
	}()
	if err = temp.Chmod(0o600); err != nil {
		return err
	}
	if _, err = temp.Write(content); err != nil {
		return err
	}
	if err = temp.Sync(); err != nil {
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tempPath, path); err != nil {
		return err
	}
	return nil
}

var dataKlineCmd = &cobra.Command{
	Use:   "kline",
	Short: "K 线数据读取工具",
}

func init() {
	dataCmd.AddCommand(dataKlineCmd)
	dataKlineCmd.AddCommand(newDataKlineGetCmd(defaultDataKlineDeps()))
}
