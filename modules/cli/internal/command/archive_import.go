package command

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
)

// archiveWorkerComponent 是归档 worker 在服务目录与 moox.toml [deployments] 里的组件 ID。
const archiveWorkerComponent = "archive-worker"

// archiveImportPollEvery 是 --wait 时查询运行状态的间隔。
const archiveImportPollEvery = 5 * time.Second

// archiveSystemColumns 是归档 CSV 的三个系统列，排在值列之前。
var archiveSystemColumns = []string{"data_time", "subject_id", "series_tag"}

// archiveImportRemote 是导入用到的远程能力：在 worker 所在主机建目录、上传文件。
type archiveImportRemote interface {
	Exec(ctx context.Context, hostID string, argv []string, stdin io.Reader) (setupssh.Result, error)
	Upload(ctx context.Context, hostID string, src io.Reader, size int64, dst string, mode fs.FileMode) error
	Close()
}

// openArchiveImportRemote 读 moox.toml，返回经 SSH 访问主机的连接层与 archive-worker 所在主机（[deployments] 里唯一的一台）。
var openArchiveImportRemote = func(file string) (archiveImportRemote, string, error) {
	absolute, err := filepath.Abs(defaultFlag(file, "./moox.toml"))
	if err != nil {
		return nil, "", err
	}
	snapshot, err := setupconfig.Load(absolute, filepath.Dir(absolute))
	if err != nil {
		return nil, "", err
	}
	host := snapshot.Manifest.ComponentHost(archiveWorkerComponent)
	if host == "" {
		clearSetupSecrets(snapshot)
		return nil, "", fmt.Errorf("moox.toml 的 [deployments] 里没有唯一一台部署了 %s 的主机", archiveWorkerComponent)
	}
	// 本节点 ID 留空：worker 所在主机一律经 SSH 访问（与 deploy-unit 的前提相同）。
	return newHostRemoteFromSnapshot(snapshot, "", nil), host, nil
}

// archiveSleep 是 --wait 轮询的等待（测试替换）。
var archiveSleep = func(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// archiveValueColumns 返回数据集的值列（与 worker 同一口径：状态为空或 active、来源为字段或自定义，按列名排序）。
func archiveValueColumns(ctx context.Context, gateway commandGateway, spaceID, datasetID string) ([]string, error) {
	var names []string
	for page := uint32(1); ; page++ {
		rsp := &pb.ListDatasetColumnsRsp{}
		if err := postStorage(ctx, gateway, metadataServiceName, "ListDatasetColumns", &pb.ListDatasetColumnsReq{
			SpaceId: spaceID, DatasetId: datasetID, Page: &pb.Page{Page: page, Size: 200},
		}, rsp); err != nil {
			return nil, err
		}
		for _, column := range rsp.GetColumns() {
			if status := column.GetStatus(); status != "" && status != "active" {
				continue
			}
			switch column.GetOriginType() {
			case pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_CUSTOM:
				names = append(names, column.GetColumnName())
			}
		}
		if !rsp.GetPageResult().GetHasMore() {
			break
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("数据集 %s/%s 没有可归档的值列", spaceID, datasetID)
	}
	sort.Strings(names)
	return names, nil
}

// runArchiveImportTemplate 打印导入文件的表头 data_time,subject_id,series_tag,<值列…>。
func runArchiveImportTemplate(ctx context.Context, out io.Writer, f archiveFlags) error {
	if err := f.requireSpaceAndDataset(); err != nil {
		return err
	}
	gateway, err := openCommandGateway(ctx, f.File, nil)
	if err != nil {
		return err
	}
	defer gateway.Close()
	columns, err := archiveValueColumns(ctx, gateway, f.SpaceID, f.DatasetID)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, strings.Join(append(append([]string(nil), archiveSystemColumns...), columns...), ","))
	return err
}

// readArchiveCSVHeader 读本地文件的表头（.gz 按魔数解压）。
func readArchiveCSVHeader(file string) ([]string, error) {
	handle, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	reader := bufio.NewReader(handle)
	var src io.Reader = reader
	if magic, _ := reader.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		zr, err := gzip.NewReader(reader)
		if err != nil {
			return nil, fmt.Errorf("解压 %s: %w", file, err)
		}
		defer zr.Close()
		src = zr
	}
	header, err := csv.NewReader(src).Read()
	if errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s 是空文件", file)
	}
	if err != nil {
		return nil, fmt.Errorf("读取 %s 的表头: %w", file, err)
	}
	for i := range header {
		header[i] = strings.TrimSpace(header[i])
	}
	return header, nil
}

// checkArchiveImportHeader 本地预检表头（worker 还会再查一遍）：列名属于数据集，data_time、subject_id 必须有，
// 不能重复；覆盖时必须带齐全部值列（覆盖是整行替换，缺列会清空已有值）。
func checkArchiveImportHeader(header, columns []string, overwrite bool) error {
	known := map[string]bool{}
	for _, name := range append(append([]string(nil), archiveSystemColumns...), columns...) {
		known[name] = true
	}
	seen := map[string]bool{}
	var unknown []string
	for _, name := range header {
		if seen[name] {
			return fmt.Errorf("表头里列 %q 重复", name)
		}
		seen[name] = true
		if !known[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("表头里有数据集没有的列：%s（用 archive import template 取表头）", strings.Join(unknown, "、"))
	}
	if !seen["data_time"] || !seen["subject_id"] {
		return errors.New("表头必须有 data_time 与 subject_id 两列")
	}
	if overwrite {
		var missing []string
		for _, name := range columns {
			if !seen[name] {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("--overwrite 时必须带齐数据集的全部值列（覆盖是整行替换，缺列会清空已有值），缺少：%s", strings.Join(missing, "、"))
		}
	}
	return nil
}

// archiveImportFileName 返回上传到导入目录里的文件名：只看扩展名，不用本地文件名（避免空格、中文等字符进路径）。
func archiveImportFileName(local string) (string, error) {
	lower := strings.ToLower(local)
	switch {
	case strings.HasSuffix(lower, ".csv.gz"):
		return "import.csv.gz", nil
	case strings.HasSuffix(lower, ".csv"):
		return "import.csv", nil
	}
	return "", fmt.Errorf("--csv 只接受 .csv 或 .csv.gz 文件：%s", local)
}

func archiveCall(ctx context.Context, gateway commandGateway, method string, req, rsp proto.Message, ret func() *commonpb.RetInfo) error {
	if err := gateway.Invoke(ctx, archiveMgrService, method, req, rsp); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	if info := ret(); info.GetCode() != commonpb.ErrorCode_SUCCESS {
		return fmt.Errorf("%s: %s (code %d)", method, info.GetMsg(), info.GetCode())
	}
	return nil
}

// runArchiveImport 把本地 CSV 导进归档：本地预检表头 → 取上传位置 → 在 worker 所在主机建目录并上传 → 建导入运行；
// --wait 时轮询到运行结束。
func runArchiveImport(ctx context.Context, out io.Writer, f archiveFlags) error {
	if err := f.requireSpaceAndDataset(); err != nil {
		return err
	}
	if strings.TrimSpace(f.CSV) == "" {
		return errors.New("--csv 必填（本地 .csv 或 .csv.gz 文件）")
	}
	name, err := archiveImportFileName(f.CSV)
	if err != nil {
		return err
	}
	info, err := os.Stat(f.CSV)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s 不是普通文件", f.CSV)
	}
	if !f.Yes {
		mode := "同键已存在时以已归档的为准，只补空缺"
		if f.Overwrite {
			mode = "同键已存在时用文件里的值覆盖"
		}
		return fmt.Errorf("导入会把 %s 写进 %s/%s 的归档（%s），有一行不对就整份拒绝；确认后加 --yes", f.CSV, f.SpaceID, f.DatasetID, mode)
	}
	header, err := readArchiveCSVHeader(f.CSV)
	if err != nil {
		return err
	}
	gateway, err := openCommandGateway(ctx, f.File, nil)
	if err != nil {
		return err
	}
	defer gateway.Close()
	columns, err := archiveValueColumns(ctx, gateway, f.SpaceID, f.DatasetID)
	if err != nil {
		return err
	}
	if err := checkArchiveImportHeader(header, columns, f.Overwrite); err != nil {
		return err
	}
	prepared := &archivepb.PrepareArchiveImportRsp{}
	if err := archiveCall(ctx, gateway, "PrepareArchiveImport", &archivepb.PrepareArchiveImportReq{SpaceId: f.SpaceID, DatasetId: f.DatasetID}, prepared, prepared.GetRetInfo); err != nil {
		return err
	}
	if prepared.GetImportDir() == "" || !path.IsAbs(prepared.GetImportDir()) {
		return fmt.Errorf("PrepareArchiveImport 返回的导入目录 %q 无效", prepared.GetImportDir())
	}
	remote, host, err := openArchiveImportRemote(f.File)
	if err != nil {
		return err
	}
	defer remote.Close()
	// Upload 不建目录：先建好只有本用户能进的导入目录，再上传成 0600。
	if _, err := remote.Exec(ctx, host, []string{"mkdir", "-p", "-m", "0700", prepared.GetImportDir()}, nil); err != nil {
		return fmt.Errorf("在 %s 上建导入目录 %s 失败: %w", host, prepared.GetImportDir(), err)
	}
	src, err := os.Open(f.CSV)
	if err != nil {
		return err
	}
	defer src.Close()
	target := path.Join(prepared.GetImportDir(), name)
	if err := remote.Upload(ctx, host, src, info.Size(), target, 0o600); err != nil {
		return fmt.Errorf("上传 %s 到 %s:%s 失败: %w", f.CSV, host, target, err)
	}
	created := &archivepb.ImportArchiveFileRsp{}
	if err := archiveCall(ctx, gateway, "ImportArchiveFile", &archivepb.ImportArchiveFileReq{
		SpaceId: f.SpaceID, DatasetId: f.DatasetID, ImportId: prepared.GetImportId(), FileName: name, Overwrite: f.Overwrite,
	}, created, created.GetRetInfo); err != nil {
		return err
	}
	if !f.Wait {
		return printArchiveProto(out, created)
	}
	runID := created.GetRun().GetRunId()
	for {
		got := &archivepb.GetArchiveRunRsp{}
		if err := archiveCall(ctx, gateway, "GetArchiveRun", &archivepb.GetArchiveRunReq{RunId: runID}, got, got.GetRetInfo); err != nil {
			return err
		}
		switch got.GetRun().GetStatus() {
		case "queued", "running":
		default:
			if err := printArchiveProto(out, got); err != nil {
				return err
			}
			if status := got.GetRun().GetStatus(); status != "succeeded" {
				return fmt.Errorf("导入运行 %s 结束状态为 %s：%s", runID, status, got.GetRun().GetMessage())
			}
			return nil
		}
		if err := archiveSleep(ctx, archiveImportPollEvery); err != nil {
			return err
		}
	}
}

// runArchiveTasksClearUnrecoverable 删掉任务累积的、整段落在 [start, end) 里的无法补齐区间。
func runArchiveTasksClearUnrecoverable(ctx context.Context, out io.Writer, f archiveFlags) error {
	if err := f.requireSpaceAndDataset(); err != nil {
		return err
	}
	if f.From == "" || f.To == "" {
		return errors.New("--start 与 --end 必填（RFC3339，左闭右开）")
	}
	if !f.Yes {
		return errors.New("清除后这些区间不再提示无法补齐；先确认历史缺口已经补上（例如导入之后），再加 --yes")
	}
	rsp := &archivepb.ClearArchiveUnrecoverableRsp{}
	return archiveInvoke(ctx, out, f.File, "ClearArchiveUnrecoverable", &archivepb.ClearArchiveUnrecoverableReq{
		SpaceId: f.SpaceID, DatasetId: f.DatasetID, Start: f.From, End: f.To,
	}, rsp, rsp.GetRetInfo)
}

// newArchiveImportCommand 创建 archive import（导入）与它的 template 子命令。
func newArchiveImportCommand() *cobra.Command {
	var f archiveFlags
	cmd := &cobra.Command{
		Use:   "import",
		Short: "把本地 CSV（归档格式）作为历史数据直接导进归档，不经 Storage",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runArchiveImport(cmd.Context(), cmd.OutOrStdout(), f)
		},
	}
	cmd.Flags().StringVar(&f.File, "file", "./moox.toml", "部署配置文件；用于解析操作员 SSH 网关与 archive-worker 所在主机")
	bindDataset(cmd, &f)
	cmd.Flags().StringVar(&f.CSV, "csv", "", "本地 .csv 或 .csv.gz 文件（表头用 archive import template 取）")
	cmd.Flags().BoolVar(&f.Overwrite, "overwrite", false, "同键已存在时用文件里的值覆盖（必须带齐全部值列）；默认以已归档的为准，只补空缺")
	cmd.Flags().BoolVar(&f.Wait, "wait", false, "等导入运行结束并打印结果")
	cmd.Flags().BoolVar(&f.Yes, "yes", false, "确认导入")
	cmd.AddCommand(newArchiveLeaf("template", "打印导入文件的表头 data_time,subject_id,series_tag,<值列…>", runArchiveImportTemplate, bindDataset))
	return cmd
}
