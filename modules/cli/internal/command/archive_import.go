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
	// Excel 等工具另存的 UTF-8 CSV 开头带 BOM。
	header[0] = strings.TrimPrefix(header[0], "\ufeff")
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

// checkArchiveTaskIdle 确认任务没有运行中的运行、也没有排队中的另一条导入；其余排队中的运行（复核、立即运行、补拉、到点拉取）不挡导入。
func checkArchiveTaskIdle(ctx context.Context, gateway commandGateway, spaceID, datasetID string) error {
	for _, status := range []string{"queued", "running"} {
		rsp := &archivepb.ListArchiveRunsRsp{}
		if err := archiveCall(ctx, gateway, "ListArchiveRuns", &archivepb.ListArchiveRunsReq{
			SpaceId: spaceID, DatasetId: datasetID, Status: status, IncludeTicks: true, Page: &commonpb.Page{Page: 1, Size: 20},
		}, rsp, rsp.GetRetInfo); err != nil {
			return err
		}
		for _, run := range rsp.GetRuns() {
			// 与 mgr 的口径一致：只有运行中的运行、或排队中的另一条导入会挡住导入；排队中的复核、立即运行、补拉区间、到点拉取不挡。
			if run.GetStatus() == "queued" && run.GetKind() != "import" {
				continue
			}
			label := map[string]string{"queued": "排队中", "running": "运行中"}[run.GetStatus()]
			return fmt.Errorf("任务 %s/%s 有一条%s的运行（%s，%s），等它结束后再导入；还没有上传文件", spaceID, datasetID, label, run.GetRunId(), run.GetKind())
		}
	}
	return nil
}

// uploadArchiveImport 取上传位置，核对 worker 与 moox.toml 部署的主机一致后，在该主机建导入目录并上传文件，返回导入 ID。
func uploadArchiveImport(ctx context.Context, gateway commandGateway, f archiveFlags, name string, size int64) (string, error) {
	prepared := &archivepb.PrepareArchiveImportRsp{}
	if err := archiveCall(ctx, gateway, "PrepareArchiveImport", &archivepb.PrepareArchiveImportReq{SpaceId: f.SpaceID, DatasetId: f.DatasetID}, prepared, prepared.GetRetInfo); err != nil {
		return "", err
	}
	if prepared.GetImportDir() == "" || !path.IsAbs(prepared.GetImportDir()) {
		return "", fmt.Errorf("PrepareArchiveImport 返回的导入目录 %q 无效", prepared.GetImportDir())
	}
	remote, host, err := openArchiveImportRemote(f.File)
	if err != nil {
		return "", err
	}
	defer remote.Close()
	// worker ID 由 deploy-unit 写成 archive-worker@<主机>；对不上时传到别的主机上，worker 读不到文件。
	if want := archiveWorkerComponent + "@" + host; prepared.GetWorkerId() != want {
		return "", fmt.Errorf("活动的归档 worker 是 %q，而 moox.toml [deployments] 里部署 %s 的主机是 %s（对应 worker %q），两边不一致，没有上传文件；先用 moox-cli archive overview 核对活动 worker，再核对部署清单",
			prepared.GetWorkerId(), archiveWorkerComponent, host, want)
	}
	// Upload 不建目录：先建好只有本用户能进的导入目录，再上传成 0600。
	if _, err := remote.Exec(ctx, host, []string{"mkdir", "-p", "-m", "0700", prepared.GetImportDir()}, nil); err != nil {
		return "", fmt.Errorf("在 %s 上建导入目录 %s 失败: %w", host, prepared.GetImportDir(), err)
	}
	src, err := os.Open(f.CSV)
	if err != nil {
		return "", err
	}
	defer src.Close()
	target := path.Join(prepared.GetImportDir(), name)
	if err := remote.Upload(ctx, host, src, size, target, 0o600); err != nil {
		return "", fmt.Errorf("上传 %s 到 %s:%s 失败: %w", f.CSV, host, target, err)
	}
	return prepared.GetImportId(), nil
}

// archiveImportRetryCommand 返回用已上传文件重试导入的命令（参数与这次一致，含 --overwrite）。
func archiveImportRetryCommand(f archiveFlags, importID string) string {
	args := []string{"moox-cli", "archive", "import"}
	if f.File != "" && f.File != "./moox.toml" {
		args = append(args, "--file", shellArg(f.File))
	}
	args = append(args, "--space-id", shellArg(f.SpaceID), "--dataset-id", shellArg(f.DatasetID), "--csv", shellArg(f.CSV), "--import-id", importID)
	if f.Overwrite {
		args = append(args, "--overwrite")
	}
	if f.Wait {
		args = append(args, "--wait")
	}
	return strings.Join(append(args, "--yes"), " ")
}

// shellArg 在参数含空白或 shell 特殊字符时加单引号。
func shellArg(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\n'\"\\$`!*?[](){}<>|&;#~") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

// validArchiveImportID 按目录片段规则检查 --import-id（不能含斜杠、不能以点开头），mgr 与 worker 还会再查。
func validArchiveImportID(id string) error {
	if strings.TrimSpace(id) != id || strings.ContainsAny(id, `/\`) || strings.HasPrefix(id, ".") || strings.ContainsRune(id, 0) {
		return fmt.Errorf("--import-id %q 无效：不能含斜杠或首尾空白，不能以点开头", id)
	}
	return nil
}

// runArchiveImport 把本地 CSV 导进归档：本地预检表头 → 确认任务空闲 → 取上传位置并核对 worker 主机 →
// 在 worker 所在主机建目录并上传 → 建导入运行；带 --import-id 时跳过上传，直接用已上传的文件建运行。
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
	if f.ImportID != "" {
		if err := validArchiveImportID(f.ImportID); err != nil {
			return err
		}
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
	// 先确认任务没有排队中或运行中的运行，免得传完大文件才发现建不了运行。
	// 这只是预检：两次调用之间仍可能有新运行，ImportArchiveFile 冲突时下面会给出重试命令。
	if err := checkArchiveTaskIdle(ctx, gateway, f.SpaceID, f.DatasetID); err != nil {
		return err
	}
	importID := f.ImportID
	if importID == "" {
		if importID, err = uploadArchiveImport(ctx, gateway, f, name, info.Size()); err != nil {
			return err
		}
	}
	created := &archivepb.ImportArchiveFileRsp{}
	if err := archiveCall(ctx, gateway, "ImportArchiveFile", &archivepb.ImportArchiveFileReq{
		SpaceId: f.SpaceID, DatasetId: f.DatasetID, ImportId: importID, FileName: name, Overwrite: f.Overwrite,
	}, created, created.GetRetInfo); err != nil {
		// 文件已经在 worker 上：不论哪种失败都给出导入 ID 与重试命令，免得重新上传整个文件。
		hint := "排除原因后"
		if created.GetRetInfo().GetCode() == commonpb.ErrorCode_CONFLICT {
			hint = "等运行结束后"
		}
		return fmt.Errorf("%w\n文件已在 worker 上，导入 ID：%s（导入目录保留 7 天）；%s不用重新上传，执行：\n  %s",
			err, importID, hint, archiveImportRetryCommand(f, importID))
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
	cmd.Flags().StringVar(&f.ImportID, "import-id", "", "重试已上传的文件：跳过上传，直接用这个导入 ID 建运行（--csv 仍要给同一份本地文件，用来预检表头、确定文件名）")
	cmd.Flags().BoolVar(&f.Yes, "yes", false, "确认导入")
	cmd.AddCommand(newArchiveLeaf("template", "打印导入文件的表头 data_time,subject_id,series_tag,<值列…>", runArchiveImportTemplate, bindDataset))
	return cmd
}
