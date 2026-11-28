package command

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/gatewayio"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"trpc.group/trpc-go/trpc-go/client"
)

type klineGateway interface {
	ReadTimeSeriesRows(context.Context, *pb.ReadTimeSeriesRowsReq, ...client.Option) (*pb.ReadTimeSeriesRowsRsp, error)
	ListCollectionTasks(context.Context, *collectorpb.GetTaskListReq) (*collectorpb.GetTaskListRsp, error)
}

type klineRPCGateway struct {
	gateway gatewayclient.Invoker
}

func (g klineRPCGateway) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq, options ...client.Option) (*pb.ReadTimeSeriesRowsRsp, error) {
	return storageGateway{gateway: g.gateway}.ReadTimeSeriesRows(ctx, req, options...)
}

func (g klineRPCGateway) ListCollectionTasks(ctx context.Context, req *collectorpb.GetTaskListReq) (*collectorpb.GetTaskListRsp, error) {
	rsp := &collectorpb.GetTaskListRsp{}
	if err := g.gateway.Invoke(ctx, collectMgr, "GetTaskList", req, rsp); err != nil {
		return nil, err
	}
	return rsp, nil
}

type dataKlineDeps struct {
	open func(context.Context, string) (klineGateway, io.Closer, error)
}

func defaultDataKlineDeps() dataKlineDeps {
	return dataKlineDeps{
		open: func(ctx context.Context, file string) (klineGateway, io.Closer, error) {
			cfg, err := topology.Load(file)
			if err != nil {
				return nil, nil, err
			}
			gateway, err := gatewayio.Open(ctx, cfg)
			if err != nil {
				return nil, nil, err
			}
			return klineRPCGateway{gateway: gateway}, gateway, nil
		},
	}
}

func newDataKlineGetCmd(deps dataKlineDeps) *cobra.Command {
	var (
		file      string
		spaceID   string
		datasetID string
		tagID     string
		symbol    string
		interval  string
		seriesTag string
		limit     uint32
		startTime string
		endTime   string
		timeout   time.Duration
		output    string
		columns   []string
	)
	cmd := &cobra.Command{
		Use:          "get",
		Short:        "获取 K 线数据",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			spaceID, err := requiredFlagValue(spaceID, "--space-id")
			if err != nil {
				return err
			}
			symbol, err := requiredFlagValue(symbol, "--symbol")
			if err != nil {
				return err
			}
			datasetID = strings.TrimSpace(datasetID)
			tagID = strings.TrimSpace(tagID)
			if datasetID == "" && tagID == "" {
				return fmt.Errorf("必须指定 --dataset-id 或 --tag-id 之一")
			}
			if datasetID != "" && tagID != "" {
				return fmt.Errorf("--dataset-id 与 --tag-id 不能同时指定")
			}
			interval = strings.ToLower(strings.TrimSpace(interval))
			if interval == "" {
				return fmt.Errorf("--interval 不能为空")
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
			if err := rejectInputOutputCollision(file, output); err != nil {
				return fmt.Errorf("写出 K 线结果失败: %w", err)
			}

			rpcCtx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			callCtx := gatewayclient.WithCallMetadata(rpcCtx, gatewayclient.CallMetadata{SpaceID: spaceID})
			gw, owner, err := deps.open(callCtx, file)
			if err != nil {
				return fmt.Errorf("连接数据网关失败: %w", err)
			}
			if owner != nil {
				defer owner.Close()
			}

			resolvedDatasetID := datasetID
			if resolvedDatasetID == "" {
				resolvedDatasetID, err = resolveKlineDatasetByTag(callCtx, gw, spaceID, tagID, interval)
				if err != nil {
					return err
				}
			}

			selector := &pb.TimeSeriesSelector{
				SpaceId:   spaceID,
				DatasetId: resolvedDatasetID,
				SubjectId: symbol,
				Freq:      interval,
			}
			if cmd.Flags().Changed("series-tag") {
				st := seriesTag
				selector.SeriesTag = &st
			}
			req := &pb.ReadTimeSeriesRowsReq{
				Selectors:   []*pb.TimeSeriesSelector{selector},
				ColumnNames: klineColumns(columns),
				Page:        &pb.Page{Size: limit},
				SpaceId:     spaceID,
				DatasetId:   resolvedDatasetID,
			}
			if startTime == "" && endTime == "" {
				// 没有给时间范围：返回该序列最新的 limit 根（从新到旧）。
				req.LatestPerSeries = limit
			} else {
				// 给了时间范围：从起点起按时间升序返回最多 limit 根；缺省的一端分别取最早和当前时间。
				start, end := startTime, endTime
				if start == "" {
					start = "1970-01-01T00:00:00Z"
				}
				if end == "" {
					end = time.Now().UTC().Format(time.RFC3339)
				}
				req.TimeRange = &pb.TimeRange{StartTime: start, EndTime: end}
			}
			rsp, err := gw.ReadTimeSeriesRows(callCtx, req)
			if err != nil {
				return fmt.Errorf("PrimaryStore/ReadTimeSeriesRows 调用失败: %w", err)
			}
			if rsp == nil {
				return fmt.Errorf("PrimaryStore/ReadTimeSeriesRows 调用失败: 空响应")
			}
			if err := checkStorageRetInfo("PrimaryStore", "ReadTimeSeriesRows", rsp); err != nil {
				return err
			}
			return writeKlineResponse(cmd, rsp, output)
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "SSH 主机配置文件")
	cmd.Flags().StringVar(&spaceID, "space-id", "", "空间 ID，例如 crypto、stockcn")
	cmd.Flags().StringVar(&datasetID, "dataset-id", "", "结果数据集 ID（与 --tag-id 二选一）")
	cmd.Flags().StringVar(&tagID, "tag-id", "", "标的标签 ID，按空间、标签与周期现查 Collector 的 K 线任务结果（与 --dataset-id 二选一）")
	cmd.Flags().StringVar(&symbol, "symbol", "", "交易标的，例如 BTC-USDT")
	cmd.Flags().StringVar(&interval, "interval", "1m", "K 线周期")
	cmd.Flags().StringVar(&seriesTag, "series-tag", "", "精确序列标签；省略表示全部序列，显式传空值表示默认序列")
	cmd.Flags().Uint32Var(&limit, "limit", 100, "返回条数，范围 1..1000；不给时间范围时是最新的 limit 根，给了时间范围时是范围内最早的 limit 根")
	cmd.Flags().StringVar(&startTime, "from", "", "RFC3339 起始时间")
	cmd.Flags().StringVar(&endTime, "to", "", "RFC3339 结束时间")
	cmd.Flags().DurationVar(&timeout, "timeout", 15*time.Second, "RPC 超时时间，必须大于 0")
	cmd.Flags().StringSliceVar(&columns, "columns", nil, "返回的列，逗号分隔；默认 open,high,low,close,volume")
	cmd.Flags().StringVar(&output, "output", "", "输出 JSON 文件；为空则输出到 stdout")
	_ = cmd.MarkFlagRequired("space-id")
	_ = cmd.MarkFlagRequired("symbol")
	return cmd
}

const maxKlineTaskPages = 100

// resolveKlineDatasetByTag 向 Collector 查询该空间下匹配标签与频率的 K 线任务，并返回其结果数据集 ID。
func resolveKlineDatasetByTag(ctx context.Context, gw klineGateway, spaceID, tagID, interval string) (string, error) {
	var matches []*collectorpb.CollectionTask
	for page := uint32(1); page <= maxKlineTaskPages; page++ {
		rsp, err := gw.ListCollectionTasks(ctx, &collectorpb.GetTaskListReq{
			SpaceId:  spaceID,
			DataType: "kline",
			Page:     &commonpb.Page{Page: page, Size: 100},
		})
		if err != nil {
			return "", fmt.Errorf("查询采集任务列表失败: %w", err)
		}
		if rsp == nil {
			return "", fmt.Errorf("查询采集任务列表失败: 空响应")
		}
		if err := collectorRetError("查询采集任务列表", rsp.GetRetInfo()); err != nil {
			return "", err
		}
		for _, task := range rsp.GetTasks() {
			if !strings.EqualFold(strings.TrimSpace(task.GetDataType()), "kline") {
				continue
			}
			if taskSpace := strings.TrimSpace(task.GetSpaceId()); taskSpace != "" && taskSpace != spaceID {
				continue
			}
			if !taskHasTag(task.GetTagIds(), tagID) {
				continue
			}
			if !strings.EqualFold(taskFrequency(task), interval) {
				continue
			}
			if strings.TrimSpace(task.GetResult().GetDatasetId()) == "" {
				continue
			}
			matches = append(matches, task)
		}
		if len(rsp.GetTasks()) == 0 || !rsp.GetPage().GetHasMore() {
			break
		}
		if page == maxKlineTaskPages {
			return "", fmt.Errorf("查询采集任务列表超过 %d 页上限，请改用 --dataset-id 指定结果数据集", maxKlineTaskPages)
		}
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("空间 %q 下未找到标签 %q、周期 %q 的 K 线采集任务结果；可用 moox-cli collector task list --space-id %s 查看任务并直接传 --dataset-id", spaceID, tagID, interval, spaceID)
	}
	if len(matches) == 1 {
		return strings.TrimSpace(matches[0].GetResult().GetDatasetId()), nil
	}
	var enabled []*collectorpb.CollectionTask
	for _, task := range matches {
		if task.GetEnabled() {
			enabled = append(enabled, task)
		}
	}
	if len(enabled) == 1 {
		return strings.TrimSpace(enabled[0].GetResult().GetDatasetId()), nil
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].GetTaskId() < matches[j].GetTaskId()
	})
	candidates := make([]string, 0, len(matches))
	for _, task := range matches {
		name := strings.TrimSpace(task.GetTaskName())
		if name != "" {
			candidates = append(candidates, fmt.Sprintf("%s（%s）-> %s", task.GetTaskId(), name, strings.TrimSpace(task.GetResult().GetDatasetId())))
		} else {
			candidates = append(candidates, fmt.Sprintf("%s -> %s", task.GetTaskId(), strings.TrimSpace(task.GetResult().GetDatasetId())))
		}
	}
	return "", fmt.Errorf("空间 %q 下标签 %q、周期 %q 匹配到多个 K 线采集任务（%s），请用 --dataset-id 明确指定", spaceID, tagID, interval, strings.Join(candidates, "；"))
}

func taskHasTag(tagIDs []string, target string) bool {
	for _, candidate := range tagIDs {
		if strings.TrimSpace(candidate) == target {
			return true
		}
	}
	return false
}

func taskFrequency(task *collectorpb.CollectionTask) string {
	fields := task.GetCollectParams().GetFields()
	if len(fields) == 0 {
		return ""
	}
	val, ok := fields["frequency"]
	if !ok || val == nil {
		return ""
	}
	return strings.TrimSpace(val.GetStringValue())
}

func rejectInputOutputCollision(input, output string) error {
	input = strings.TrimSpace(input)
	output = strings.TrimSpace(output)
	if input == "" || output == "" {
		return nil
	}
	inputAbs, err := filepath.Abs(input)
	if err != nil {
		return err
	}
	outputAbs, err := filepath.Abs(output)
	if err != nil {
		return err
	}
	if filepath.Clean(inputAbs) == filepath.Clean(outputAbs) {
		return fmt.Errorf("输入文件 %q 与输出文件 %q 不能是同一个文件", input, output)
	}

	inputInfo, err := os.Lstat(input)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	outputInfo, err := os.Lstat(output)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if os.SameFile(inputInfo, outputInfo) {
		return fmt.Errorf("输入文件 %q 与输出文件 %q 不能是同一个文件", input, output)
	}
	return nil
}

func validateKlineTimeRange(start, end string) error {
	var startValue, endValue time.Time
	var err error
	if start != "" {
		startValue, err = time.Parse(time.RFC3339, start)
		if err != nil {
			return fmt.Errorf("--from 必须是 RFC3339 时间: %w", err)
		}
	}
	if end != "" {
		endValue, err = time.Parse(time.RFC3339, end)
		if err != nil {
			return fmt.Errorf("--to 必须是 RFC3339 时间: %w", err)
		}
	}
	if start != "" && end != "" && startValue.After(endValue) {
		return fmt.Errorf("--from 不能晚于 --to")
	}
	return nil
}

func writeKlineResponse(cmd *cobra.Command, rsp *pb.ReadTimeSeriesRowsRsp, output string) error {
	raw, err := protojson.MarshalOptions{UseProtoNames: true, Multiline: true, EmitUnpopulated: true}.Marshal(rsp)
	if err != nil {
		return fmt.Errorf("编码 K 线响应失败: %w", err)
	}
	if strings.TrimSpace(output) == "" {
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), string(raw)); err != nil {
			return fmt.Errorf("写出 K 线响应失败: %w", err)
		}
		return nil
	}
	if err := writeAtomic0600(strings.TrimSpace(output), raw); err != nil {
		return fmt.Errorf("写出 K 线响应失败: %w", err)
	}
	return nil
}

func writeAtomic0600(path string, content []byte) (err error) {
	if info, statErr := os.Lstat(path); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("输出路径 %q 不能是符号链接", path)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("输出路径 %q 必须是普通文件", path)
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

// klineColumns 返回 K 线读取的列：用户没有指定时用标准行情列。
func klineColumns(columns []string) []string {
	if len(columns) > 0 {
		return columns
	}
	return []string{"open", "high", "low", "close", "volume"}
}
