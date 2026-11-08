package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const archiveMgrService = "trpc.moox.archive.ArchiveMgr"

// archiveFlags 是 archive 子命令共用的参数。
type archiveFlags struct {
	File         string
	SpaceID      string
	DatasetID    string
	DirName      string
	PullEvery    uint32
	BackfillFrom string
	Paused       bool
	Status       string
	IncludeTicks bool
	ClearUnrecov bool
	From         string
	To           string
	RunID        string
	WorkerID     string
	PartitionKey string
	FromMonth    string
	ToMonth      string
	Page         uint32
	PageSize     uint32
	Yes          bool
	// CSV、Overwrite、Wait 是 archive import 的本地文件、是否覆盖与是否等待结束；
	// ImportID 非空时重试已上传的文件（跳过上传）。
	CSV       string
	Overwrite bool
	Wait      bool
	ImportID  string
}

var archiveCmd = &cobra.Command{Use: "archive", Short: "数据归档：任务、运行与分区（调用 ArchiveMgr）"}

// archiveInvoke 经网关调用 ArchiveMgr 的一个方法，成功后把响应按 proto 字段名输出成 JSON。
func archiveInvoke(ctx context.Context, out io.Writer, file, method string, req proto.Message, rsp proto.Message, ret func() *commonpb.RetInfo) error {
	gateway, err := openCommandGateway(ctx, file, nil)
	if err != nil {
		return err
	}
	defer gateway.Close()
	if err := gateway.Invoke(ctx, archiveMgrService, method, req, rsp); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	if info := ret(); info.GetCode() != commonpb.ErrorCode_SUCCESS {
		return fmt.Errorf("%s: %s (code %d)", method, info.GetMsg(), info.GetCode())
	}
	return printArchiveProto(out, rsp)
}

// printArchiveProto 把响应按 proto 字段名输出成缩进的 JSON。
func printArchiveProto(out io.Writer, rsp proto.Message) error {
	// protojson 的输出空白是故意随机化的，先输出紧凑 JSON 再统一缩进，保证输出稳定。
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(rsp)
	if err != nil {
		return err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, pretty.String())
	return err
}

func (f archiveFlags) requireSpaceAndDataset() error {
	if strings.TrimSpace(f.SpaceID) == "" || strings.TrimSpace(f.DatasetID) == "" {
		return errors.New("--space-id 与 --dataset-id 必填")
	}
	return nil
}

func (f archiveFlags) page() *commonpb.Page {
	return &commonpb.Page{Page: f.Page, Size: f.PageSize}
}

func runArchiveOverview(ctx context.Context, out io.Writer, f archiveFlags) error {
	rsp := &archivepb.GetArchiveOverviewRsp{}
	return archiveInvoke(ctx, out, f.File, "GetArchiveOverview", &archivepb.GetArchiveOverviewReq{}, rsp, rsp.GetRetInfo)
}

func runArchiveTasksList(ctx context.Context, out io.Writer, f archiveFlags) error {
	rsp := &archivepb.ListArchiveTasksRsp{}
	return archiveInvoke(ctx, out, f.File, "ListArchiveTasks", &archivepb.ListArchiveTasksReq{SpaceId: f.SpaceID, Page: f.page()}, rsp, rsp.GetRetInfo)
}

func runArchiveTasksPrepare(ctx context.Context, out io.Writer, f archiveFlags) error {
	if err := f.requireSpaceAndDataset(); err != nil {
		return err
	}
	rsp := &archivepb.PrepareArchiveTaskRsp{}
	return archiveInvoke(ctx, out, f.File, "PrepareArchiveTask", &archivepb.PrepareArchiveTaskReq{SpaceId: f.SpaceID, DatasetId: f.DatasetID, DirName: f.DirName}, rsp, rsp.GetRetInfo)
}

func runArchiveTasksPreview(ctx context.Context, out io.Writer, f archiveFlags) error {
	if err := f.requireSpaceAndDataset(); err != nil {
		return err
	}
	rsp := &archivepb.PreviewArchiveScheduleRsp{}
	return archiveInvoke(ctx, out, f.File, "PreviewArchiveSchedule", &archivepb.PreviewArchiveScheduleReq{
		SpaceId: f.SpaceID, DatasetId: f.DatasetID, PullEveryBars: f.PullEvery,
	}, rsp, rsp.GetRetInfo)
}

func runArchiveTasksCreate(ctx context.Context, out io.Writer, f archiveFlags) error {
	if err := f.requireSpaceAndDataset(); err != nil {
		return err
	}
	rsp := &archivepb.CreateArchiveTaskRsp{}
	return archiveInvoke(ctx, out, f.File, "CreateArchiveTask", &archivepb.CreateArchiveTaskReq{
		SpaceId: f.SpaceID, DatasetId: f.DatasetID, PullEveryBars: f.PullEvery, BackfillFrom: f.BackfillFrom, Paused: f.Paused, DirName: f.DirName,
	}, rsp, rsp.GetRetInfo)
}

func runArchiveTasksUpdate(ctx context.Context, out io.Writer, f archiveFlags) error {
	if err := f.requireSpaceAndDataset(); err != nil {
		return err
	}
	rsp := &archivepb.UpdateArchiveTaskRsp{}
	return archiveInvoke(ctx, out, f.File, "UpdateArchiveTask", &archivepb.UpdateArchiveTaskReq{
		SpaceId: f.SpaceID, DatasetId: f.DatasetID, PullEveryBars: f.PullEvery, BackfillFrom: f.BackfillFrom, Status: f.Status, ClearUnrecoverable: f.ClearUnrecov,
	}, rsp, rsp.GetRetInfo)
}

func runArchiveTasksDelete(ctx context.Context, out io.Writer, f archiveFlags) error {
	if err := f.requireSpaceAndDataset(); err != nil {
		return err
	}
	if !f.Yes {
		return errors.New("删除任务不可恢复（已有分区的任务不允许删除）；确认后加 --yes")
	}
	rsp := &archivepb.DeleteArchiveTaskRsp{}
	return archiveInvoke(ctx, out, f.File, "DeleteArchiveTask", &archivepb.DeleteArchiveTaskReq{SpaceId: f.SpaceID, DatasetId: f.DatasetID}, rsp, rsp.GetRetInfo)
}

// runArchiveWorkerRetire 删除一条不再使用的 worker 登记；仍持有活动租约的 worker 不能退役。
func runArchiveWorkerRetire(ctx context.Context, out io.Writer, f archiveFlags) error {
	workerID := strings.TrimSpace(f.WorkerID)
	if workerID == "" {
		return errors.New("--worker 必填")
	}
	if !f.Yes {
		return errors.New("退役会删除这条 worker 登记（仍持有活动租约的 worker 不能退役）；确认后加 --yes")
	}
	rsp := &archivepb.RetireArchiveWorkerRsp{}
	return archiveInvoke(ctx, out, f.File, "RetireArchiveWorker", &archivepb.RetireArchiveWorkerReq{WorkerId: workerID}, rsp, rsp.GetRetInfo)
}

func runArchiveTasksRun(ctx context.Context, out io.Writer, f archiveFlags) error {
	if err := f.requireSpaceAndDataset(); err != nil {
		return err
	}
	rsp := &archivepb.RunArchiveTaskNowRsp{}
	return archiveInvoke(ctx, out, f.File, "RunArchiveTaskNow", &archivepb.RunArchiveTaskNowReq{SpaceId: f.SpaceID, DatasetId: f.DatasetID}, rsp, rsp.GetRetInfo)
}

func runArchiveTasksBackfill(ctx context.Context, out io.Writer, f archiveFlags) error {
	if err := f.requireSpaceAndDataset(); err != nil {
		return err
	}
	if f.From == "" || f.To == "" {
		return errors.New("--from 与 --to 必填（RFC3339，数据时间，左闭右开）")
	}
	rsp := &archivepb.BackfillArchiveRangeRsp{}
	return archiveInvoke(ctx, out, f.File, "BackfillArchiveRange", &archivepb.BackfillArchiveRangeReq{SpaceId: f.SpaceID, DatasetId: f.DatasetID, RangeStart: f.From, RangeEnd: f.To}, rsp, rsp.GetRetInfo)
}

func runArchiveRunsList(ctx context.Context, out io.Writer, f archiveFlags) error {
	rsp := &archivepb.ListArchiveRunsRsp{}
	return archiveInvoke(ctx, out, f.File, "ListArchiveRuns", &archivepb.ListArchiveRunsReq{SpaceId: f.SpaceID, DatasetId: f.DatasetID, Status: f.Status, IncludeTicks: f.IncludeTicks, Page: f.page()}, rsp, rsp.GetRetInfo)
}

func runArchiveRunsGet(ctx context.Context, out io.Writer, f archiveFlags) error {
	if strings.TrimSpace(f.RunID) == "" {
		return errors.New("--run-id 必填")
	}
	rsp := &archivepb.GetArchiveRunRsp{}
	return archiveInvoke(ctx, out, f.File, "GetArchiveRun", &archivepb.GetArchiveRunReq{RunId: f.RunID}, rsp, rsp.GetRetInfo)
}

func runArchiveRunsCancel(ctx context.Context, out io.Writer, f archiveFlags) error {
	if strings.TrimSpace(f.RunID) == "" {
		return errors.New("--run-id 必填")
	}
	rsp := &archivepb.CancelArchiveRunRsp{}
	return archiveInvoke(ctx, out, f.File, "CancelArchiveRun", &archivepb.CancelArchiveRunReq{RunId: f.RunID}, rsp, rsp.GetRetInfo)
}

func runArchivePartitionsList(ctx context.Context, out io.Writer, f archiveFlags) error {
	if strings.TrimSpace(f.SpaceID) == "" {
		return errors.New("--space-id 必填")
	}
	rsp := &archivepb.ListArchivePartitionsRsp{}
	return archiveInvoke(ctx, out, f.File, "ListArchivePartitions", &archivepb.ListArchivePartitionsReq{
		SpaceId: f.SpaceID, DatasetId: f.DatasetID, FromMonth: f.FromMonth, ToMonth: f.ToMonth, Page: f.page(),
	}, rsp, rsp.GetRetInfo)
}

func runArchivePartitionsGet(ctx context.Context, out io.Writer, f archiveFlags) error {
	if err := f.requireSpaceAndDataset(); err != nil {
		return err
	}
	if strings.TrimSpace(f.PartitionKey) == "" {
		return errors.New("--partition-key 必填（日分区 2006-01-02，月分区 2006-01）")
	}
	rsp := &archivepb.GetArchivePartitionRsp{}
	return archiveInvoke(ctx, out, f.File, "GetArchivePartition", &archivepb.GetArchivePartitionReq{SpaceId: f.SpaceID, DatasetId: f.DatasetID, PartitionKey: f.PartitionKey}, rsp, rsp.GetRetInfo)
}

// newArchiveLeaf 创建一个叶子命令，flags 由 bind 决定。
func newArchiveLeaf(use, short string, run func(context.Context, io.Writer, archiveFlags) error, bind func(*cobra.Command, *archiveFlags)) *cobra.Command {
	var f archiveFlags
	cmd := &cobra.Command{Use: use, Short: short, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return run(cmd.Context(), cmd.OutOrStdout(), f)
	}}
	cmd.Flags().StringVar(&f.File, "file", "./moox.toml", "部署配置文件；用于解析操作员 SSH 网关")
	bind(cmd, &f)
	return cmd
}

func bindSpace(cmd *cobra.Command, f *archiveFlags) {
	cmd.Flags().StringVar(&f.SpaceID, "space-id", "", "空间 ID")
}

func bindDataset(cmd *cobra.Command, f *archiveFlags) {
	bindSpace(cmd, f)
	cmd.Flags().StringVar(&f.DatasetID, "dataset-id", "", "数据集 ID")
}

func bindSchedule(cmd *cobra.Command, f *archiveFlags, zero string) {
	bindDataset(cmd, f)
	cmd.Flags().Uint32Var(&f.PullEvery, "pull-every-bars", 0, "每收盘多少根 K 线拉取一次（1～1440，频率 × N 不超过 1 天；1d 及以上每天一次，统一存 1）；0 表示"+zero)
}

func bindPaging(cmd *cobra.Command, f *archiveFlags) {
	cmd.Flags().Uint32Var(&f.Page, "page", 1, "页号")
	cmd.Flags().Uint32Var(&f.PageSize, "page-size", 50, "每页条数")
}

func init() {
	rootCmd.AddCommand(archiveCmd)
	tasks := &cobra.Command{Use: "tasks", Short: "归档任务"}
	runs := &cobra.Command{Use: "runs", Short: "归档运行"}
	partitions := &cobra.Command{Use: "partitions", Short: "归档分区"}
	workers := &cobra.Command{Use: "worker", Short: "归档 worker 登记"}
	archiveCmd.AddCommand(
		newArchiveLeaf("overview", "worker、正在执行的运行与告警总览", runArchiveOverview, func(*cobra.Command, *archiveFlags) {}),
		tasks, runs, partitions, workers, newArchiveImportCommand(),
	)
	workers.AddCommand(
		newArchiveLeaf("retire", "退役一个不再使用的 worker 登记（例如换机后的旧记录）", runArchiveWorkerRetire, func(c *cobra.Command, f *archiveFlags) {
			c.Flags().StringVar(&f.WorkerID, "worker", "", "worker ID（归档总览里的 worker_id）")
			c.Flags().BoolVar(&f.Yes, "yes", false, "确认退役")
		}),
	)
	tasks.AddCommand(
		newArchiveLeaf("list", "列出归档任务（--space-id 为空表示全部空间）", runArchiveTasksList, func(c *cobra.Command, f *archiveFlags) { bindSpace(c, f); bindPaging(c, f) }),
		newArchiveLeaf("prepare", "查看数据集的频率、保留期、推荐的拉取间隔、最早回补起点与建议的目录名", runArchiveTasksPrepare, func(c *cobra.Command, f *archiveFlags) {
			bindDataset(c, f)
			c.Flags().StringVar(&f.DirName, "dir-name", "", "检查这个目录名是否已被同空间同用途的其他任务使用；为空检查建议的目录名")
		}),
		newArchiveLeaf("preview", "预览接下来 5 次拉取、3 次复核、复核读取区间与分区导出时间", runArchiveTasksPreview, func(c *cobra.Command, f *archiveFlags) {
			bindSchedule(c, f, "默认 1")
		}),
		newArchiveLeaf("create", "创建归档任务（下一次检查立即复核，之后按收盘拉取）", runArchiveTasksCreate, func(c *cobra.Command, f *archiveFlags) {
			bindSchedule(c, f, "默认 1")
			c.Flags().StringVar(&f.BackfillFrom, "backfill-from", "", "首次运行回补的数据起点，RFC3339；不早于线上保留期")
			c.Flags().BoolVar(&f.Paused, "paused", false, "创建后先停用")
			c.Flags().StringVar(&f.DirName, "dir-name", "", "目录名（小写字母、数字与连字符，最长 64）；为空取 prepare 给出的建议值。建好后不能改")
		}),
		newArchiveLeaf("update", "修改拉取间隔、回补起点或启停任务", runArchiveTasksUpdate, func(c *cobra.Command, f *archiveFlags) {
			bindSchedule(c, f, "不修改")
			c.Flags().StringVar(&f.BackfillFrom, "backfill-from", "", "回补起点，RFC3339")
			c.Flags().StringVar(&f.Status, "status", "", "enabled 或 paused；重新启用后下一次检查立即复核")
			c.Flags().BoolVar(&f.ClearUnrecov, "clear-unrecoverable", false, "清除任务累积的无法补齐区间（确认过、不再需要提示时）")
		}),
		newArchiveLeaf("delete", "删除没有分区的任务", runArchiveTasksDelete, func(c *cobra.Command, f *archiveFlags) {
			bindDataset(c, f)
			c.Flags().BoolVar(&f.Yes, "yes", false, "确认删除")
		}),
		newArchiveLeaf("run", "立即运行一次（同复核）", runArchiveTasksRun, bindDataset),
		newArchiveLeaf("backfill", "补拉指定数据时间区间", runArchiveTasksBackfill, func(c *cobra.Command, f *archiveFlags) {
			bindDataset(c, f)
			c.Flags().StringVar(&f.From, "from", "", "起始数据时间（含），RFC3339")
			c.Flags().StringVar(&f.To, "to", "", "结束数据时间（不含），RFC3339")
		}),
		newArchiveLeaf("clear-unrecoverable", "删掉任务累积的、整段落在 [start, end) 里的无法补齐区间（导入补齐历史缺口后由人确认再清）", runArchiveTasksClearUnrecoverable, func(c *cobra.Command, f *archiveFlags) {
			bindDataset(c, f)
			c.Flags().StringVar(&f.From, "start", "", "起点（含），RFC3339")
			c.Flags().StringVar(&f.To, "end", "", "终点（不含），RFC3339")
			c.Flags().BoolVar(&f.Yes, "yes", false, "确认清除")
		}),
	)
	runs.AddCommand(
		newArchiveLeaf("list", "列出运行记录", runArchiveRunsList, func(c *cobra.Command, f *archiveFlags) {
			bindDataset(c, f)
			bindPaging(c, f)
			c.Flags().StringVar(&f.Status, "status", "", "queued、running、succeeded、failed、canceled")
			c.Flags().BoolVar(&f.IncludeTicks, "include-ticks", false, "同时列出成功的到点拉取（tick）运行；默认不列")
		}),
		newArchiveLeaf("get", "查看一次运行（含无法补齐的区间）", runArchiveRunsGet, func(c *cobra.Command, f *archiveFlags) {
			c.Flags().StringVar(&f.RunID, "run-id", "", "运行 ID")
		}),
		newArchiveLeaf("cancel", "取消排队中或运行中的运行", runArchiveRunsCancel, func(c *cobra.Command, f *archiveFlags) {
			c.Flags().StringVar(&f.RunID, "run-id", "", "运行 ID")
		}),
	)
	partitions.AddCommand(
		newArchiveLeaf("list", "列出分区", runArchivePartitionsList, func(c *cobra.Command, f *archiveFlags) {
			bindDataset(c, f)
			bindPaging(c, f)
			c.Flags().StringVar(&f.FromMonth, "from-month", "", "起始月份 YYYY-MM")
			c.Flags().StringVar(&f.ToMonth, "to-month", "", "结束月份 YYYY-MM")
		}),
		newArchiveLeaf("get", "查看分区详情（状态、文件、sha256、预览）", runArchivePartitionsGet, func(c *cobra.Command, f *archiveFlags) {
			bindDataset(c, f)
			c.Flags().StringVar(&f.PartitionKey, "partition-key", "", "分区键")
		}),
	)
}
