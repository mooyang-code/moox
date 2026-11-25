package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
	"github.com/mooyang-code/moox/modules/cli/internal/seriesimport/runner"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"google.golang.org/protobuf/proto"
)

const (
	// archiveWorkerComponent 是归档 worker 在服务目录与 moox.toml [deployments] 里的组件 ID；worker ID 是 archive-worker@<主机 ID>。
	archiveWorkerComponent = "archive-worker"
	// archiveDataPort 是 worker 只听 127.0.0.1 的数据端口（导入写接口在这里）。
	archiveDataPort = 11428
	// archiveImportBatchRows 是一次 POST 的行数（与写 Storage 一致）。
	archiveImportBatchRows = 1000
	// archiveRequestTimeout 是一次写入请求的上限：批次落在已定稿分区时 worker 要先把整个分区读回暂存区。
	archiveRequestTimeout = 10 * time.Minute
)

// 导入运行的等待节奏（测试里改小）。
var (
	archiveStartPollEvery  = 2 * time.Second
	archiveStartQueueLimit = 3 * time.Minute
	archiveFinishPollEvery = 5 * time.Second
)

// archiveSleep 是轮询的等待（测试替换）。
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

// archiveImportWorkerHost 读 moox.toml，返回 [deployments] 里唯一一台部署了 archive-worker 的主机（测试替换）。
var archiveImportWorkerHost = func(file string) (string, error) {
	cfg, err := topology.Load(defaultFlag(file, defaultSetupFile))
	if err != nil {
		return "", err
	}
	host := cfg.ComponentHost(archiveWorkerComponent)
	if host == "" {
		return "", fmt.Errorf("moox.toml 的 [deployments] 里没有唯一一台部署了 %s 的主机", archiveWorkerComponent)
	}
	return host, nil
}

// archiveDataGateway 是连 worker 数据端口要用的网关能力（gatewayio.Client 实现）。
type archiveDataGateway interface {
	Directory() servicecatalog.Directory
	DialRelay(ctx context.Context, hostID string, port int) (net.Conn, error)
	ForwardLocal(ctx context.Context, hostID, remote string) (string, error)
}

// dialArchiveDataPort 连到 hostID 上 worker 的数据端口：只出站主机经 control 中转（以 moox-cli 身份签名），
// 其它主机经 SSH 本地转发到该机 127.0.0.1:11428（测试替换）。
var dialArchiveDataPort = func(ctx context.Context, gateway commandGateway, hostID string) (net.Conn, error) {
	data, ok := gateway.(archiveDataGateway)
	if !ok {
		return nil, errors.New("当前网关连接不支持访问归档 worker 的数据端口")
	}
	if host, ok := data.Directory().Hosts[hostID]; ok && host.Relay != "" {
		return data.DialRelay(ctx, hostID, archiveDataPort)
	}
	address, err := data.ForwardLocal(ctx, hostID, fmt.Sprintf("127.0.0.1:%d", archiveDataPort))
	if err != nil {
		return nil, err
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, "tcp", address)
}

// archiveImportCheck 是归档目标的预检信息：任务存在、worker 在线与它的磁盘余量。
type archiveImportCheck struct {
	DiskFree, MinFree uint64
}

// checkArchiveImport 确认数据集有归档任务、worker 在线，返回 worker 的磁盘余量。
func checkArchiveImport(ctx context.Context, gateway gatewayclient.Invoker, spaceID, datasetID string) (archiveImportCheck, error) {
	found := false
	for page := uint32(1); !found; page++ {
		rsp := &archivepb.ListArchiveTasksRsp{}
		if err := archiveCall(ctx, gateway, "ListArchiveTasks", &archivepb.ListArchiveTasksReq{SpaceId: spaceID, Page: &commonpb.Page{Page: page, Size: 200}}, rsp, rsp.GetRetInfo); err != nil {
			return archiveImportCheck{}, err
		}
		for _, task := range rsp.GetTasks() {
			if task.GetDatasetId() == datasetID {
				found = true
			}
		}
		if !rsp.GetPageResult().GetHasMore() || len(rsp.GetTasks()) == 0 {
			break
		}
	}
	if !found {
		return archiveImportCheck{}, fmt.Errorf("数据集 %s/%s 没有归档任务：--target archive 要先建任务（moox-cli archive tasks create）", spaceID, datasetID)
	}
	overview := &archivepb.GetArchiveOverviewRsp{}
	if err := archiveCall(ctx, gateway, "GetArchiveOverview", &archivepb.GetArchiveOverviewReq{}, overview, overview.GetRetInfo); err != nil {
		return archiveImportCheck{}, err
	}
	if !overview.GetWorker().GetActive() {
		return archiveImportCheck{}, errors.New("归档 worker 不在线（2 分钟内没有心跳），不能写归档；先用 moox-cli archive overview 查看")
	}
	return archiveImportCheck{DiskFree: overview.GetWorker().GetDiskFreeBytes(), MinFree: overview.GetWorker().GetMinFreeBytes()}, nil
}

// archiveBytesPerRow 估算暂存库每行的字节数（实测约 40 + 10 × 值列数），再乘 1.5 计入导出文件与 WAL。
func archiveBytesPerRow(valueColumns int) float64 {
	return float64(40+10*valueColumns) * 1.5
}

// checkArchiveDisk 按“导入行数 + 时间范围内已登记分区的行数（落在已定稿分区的批次会把整个分区读回）”估算暂存库峰值，
// 与 worker 的磁盘余量（可用 − 下限）比较，不够就拒绝。
func checkArchiveDisk(ctx context.Context, gateway gatewayclient.Invoker, spaceID, datasetID string, check archiveImportCheck, summary runner.Summary, valueColumns int) error {
	first, last, ok := summaryRange(summary)
	if !ok {
		return nil
	}
	var registered uint64
	for page := uint32(1); ; page++ {
		rsp := &archivepb.ListArchivePartitionsRsp{}
		if err := archiveCall(ctx, gateway, "ListArchivePartitions", &archivepb.ListArchivePartitionsReq{
			SpaceId: spaceID, DatasetId: datasetID, FromMonth: first.AddDate(0, -1, 0).Format("2006-01"), ToMonth: last.AddDate(0, 1, 0).Format("2006-01"),
			Page: &commonpb.Page{Page: page, Size: 200},
		}, rsp, rsp.GetRetInfo); err != nil {
			return err
		}
		for _, p := range rsp.GetPartitions() {
			start, serr := time.Parse(time.RFC3339, p.GetRangeStart())
			end, eerr := time.Parse(time.RFC3339, p.GetRangeEnd())
			// 范围读不出来时按重叠算（宁可多估）。
			if serr == nil && eerr == nil && (start.After(last) || !end.After(first)) {
				continue
			}
			registered += p.GetRowCount()
		}
		if !rsp.GetPageResult().GetHasMore() || len(rsp.GetPartitions()) == 0 {
			break
		}
	}
	need := uint64(float64(uint64(summary.Rows)+registered) * archiveBytesPerRow(valueColumns))
	var available uint64
	if check.DiskFree > check.MinFree {
		available = check.DiskFree - check.MinFree
	}
	if need > available {
		return fmt.Errorf("写归档要在 worker 暂存库里装下整次导入：估算需要 %s（%d 行导入 + 时间范围内已登记分区 %d 行，按每行 %.0f 字节），worker 可用 %s（可用空间 %s − 下限 %s），不够；按时间拆成几次导入",
			formatBytes(need), summary.Rows, registered, archiveBytesPerRow(valueColumns), formatBytes(available), formatBytes(check.DiskFree), formatBytes(check.MinFree))
	}
	return nil
}

func summaryRange(summary runner.Summary) (first, last time.Time, ok bool) {
	for _, f := range summary.Files {
		a, aerr := time.Parse(time.RFC3339, f.First)
		b, berr := time.Parse(time.RFC3339, f.Last)
		if aerr != nil || berr != nil {
			continue
		}
		if !ok || a.Before(first) {
			first = a
		}
		if !ok || b.After(last) {
			last = b
		}
		ok = true
	}
	return first, last, ok
}

func formatBytes(n uint64) string {
	const gib = 1 << 30
	if n >= gib {
		return fmt.Sprintf("%.1f GiB", float64(n)/gib)
	}
	return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
}

// importArchiveTarget 是 moox-cli import 的归档目标：第一次写入时建导入运行（占住任务），每批行经 worker 数据端口
// 写进暂存区，Finish 通知 worker 写完并等导入运行结束（结束时 worker 统一导出一次）。
type importArchiveTarget struct {
	gateway commandGateway
	file    string
	space   string
	dataset string
	types   map[string]pb.FieldValueType

	run    *archivepb.ArchiveRun
	client *http.Client
	final  *archivepb.ArchiveRun
}

func (t *importArchiveTarget) Name() string { return "archive" }

// TailOnly：归档写全量，不受 --tail-rows 影响。
func (t *importArchiveTarget) TailOnly() bool { return false }

// ForwardOnly：倒序写只为因子机本地连续，归档按正序写。
func (t *importArchiveTarget) ForwardOnly() bool { return true }

func (t *importArchiveTarget) Write(ctx context.Context, c runner.Chunk) error {
	if err := t.start(ctx); err != nil {
		return err
	}
	rows, err := importUpserts(c, t.types)
	if err != nil {
		return err
	}
	for start := 0; start < len(rows); start += archiveImportBatchRows {
		end := min(start+archiveImportBatchRows, len(rows))
		body, err := proto.Marshal(&pb.PrimaryUpsertFieldsReq{SpaceId: t.space, DatasetId: t.dataset, Rows: rows[start:end]})
		if err != nil {
			return err
		}
		if _, err := t.post(ctx, "/v1/import/rows", body); err != nil {
			return err
		}
	}
	return nil
}

// start 建导入运行并等它开始执行；排队超过 3 分钟就取消自己建的运行，报出挡住它的运行。
func (t *importArchiveTarget) start(ctx context.Context) error {
	if t.run != nil {
		return nil
	}
	started := &archivepb.StartArchiveImportRsp{}
	if err := archiveCall(ctx, t.gateway, "StartArchiveImport", &archivepb.StartArchiveImportReq{SpaceId: t.space, DatasetId: t.dataset}, started, started.GetRetInfo); err != nil {
		if started.GetRetInfo().GetCode() == commonpb.ErrorCode_CONFLICT {
			return fmt.Errorf("%w；确认挡住它的运行可以停掉后执行 moox-cli archive runs cancel --run-id <run_id>，再重跑同一条命令（已写的批次按断点跳过）", err)
		}
		return err
	}
	host, ok := strings.CutPrefix(started.GetWorkerId(), archiveWorkerComponent+"@")
	if !ok || host == "" {
		return fmt.Errorf("StartArchiveImport 返回的 worker ID %q 不是 %s@<主机 ID>", started.GetWorkerId(), archiveWorkerComponent)
	}
	deployed, err := archiveImportWorkerHost(t.file)
	if err != nil {
		return err
	}
	runID := started.GetRun().GetRunId()
	if deployed != host {
		t.cancel(ctx, runID)
		return fmt.Errorf("在线的归档 worker 是 %q，而 moox.toml [deployments] 里部署 %s 的主机是 %s，两边不一致，已取消导入运行 %s；先用 moox-cli archive overview 核对在线的 worker，再核对部署清单",
			started.GetWorkerId(), archiveWorkerComponent, deployed, runID)
	}
	deadline := time.Now().Add(archiveStartQueueLimit)
	for {
		got := &archivepb.GetArchiveRunRsp{}
		if err := archiveCall(ctx, t.gateway, "GetArchiveRun", &archivepb.GetArchiveRunReq{RunId: runID}, got, got.GetRetInfo); err != nil {
			return err
		}
		switch got.GetRun().GetStatus() {
		case "running":
			t.run = got.GetRun()
			t.client = &http.Client{Transport: &http.Transport{
				Proxy:               nil,
				MaxIdleConnsPerHost: 1,
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return dialArchiveDataPort(ctx, t.gateway, host)
				},
			}}
			return nil
		case "queued":
		default:
			return fmt.Errorf("导入运行 %s 没有开始就结束了（%s）：%s", runID, got.GetRun().GetStatus(), got.GetRun().GetMessage())
		}
		if time.Now().After(deadline) {
			t.cancel(ctx, runID)
			return fmt.Errorf("导入运行 %s 排队超过 %s，已取消；%s", runID, archiveStartQueueLimit, t.blocking(ctx))
		}
		if err := archiveSleep(ctx, archiveStartPollEvery); err != nil {
			return err
		}
	}
}

// blocking 说明挡住导入运行的那条运行（任务运行中的运行）。
func (t *importArchiveTarget) blocking(ctx context.Context) string {
	rsp := &archivepb.ListArchiveRunsRsp{}
	if err := archiveCall(ctx, t.gateway, "ListArchiveRuns", &archivepb.ListArchiveRunsReq{
		SpaceId: t.space, DatasetId: t.dataset, Status: "running", IncludeFollow: true, Page: &commonpb.Page{Page: 1, Size: 5},
	}, rsp, rsp.GetRetInfo); err != nil || len(rsp.GetRuns()) == 0 {
		return "worker 一直没有领取（用 moox-cli archive overview 查看 worker 是否在线、是否在每天维护）"
	}
	run := rsp.GetRuns()[0]
	return fmt.Sprintf("任务上有运行中的 %s 运行 %s；等它结束或 moox-cli archive runs cancel --run-id %s 后重跑", run.GetKind(), run.GetRunId(), run.GetRunId())
}

func (t *importArchiveTarget) cancel(ctx context.Context, runID string) {
	rsp := &archivepb.CancelArchiveRunRsp{}
	_ = archiveCall(context.WithoutCancel(ctx), t.gateway, "CancelArchiveRun", &archivepb.CancelArchiveRunReq{RunId: runID}, rsp, rsp.GetRetInfo)
}

// post 向 worker 数据端口发一个请求，非 2xx 时带上 worker 的报错。
func (t *importArchiveTarget) post(ctx context.Context, path string, body []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, archiveRequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://archive-worker"+path+"?run="+t.run.GetRunId(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/x-protobuf")
	response, err := t.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("写归档 worker 数据端口：%w", err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode/100 == 2 {
		return raw, nil
	}
	var failure struct {
		Msg string `json:"msg"`
	}
	_ = json.Unmarshal(raw, &failure)
	message := strings.TrimSpace(failure.Msg)
	if message == "" {
		message = strings.TrimSpace(string(raw))
	}
	err = fmt.Errorf("归档 worker 拒绝写入（HTTP %d）：%s", response.StatusCode, message)
	if response.StatusCode == http.StatusNotFound {
		err = fmt.Errorf("%w；导入运行 %s 已不在 worker 上执行（worker 重启或空闲超时），重跑同一条命令会按断点跳过已写批次", err, t.run.GetRunId())
	}
	return nil, err
}

// Finish 通知 worker 批次已写完，再等导入运行结束（worker 结束时导出、上传、定稿）；不是成功就返回错误。
// 本次一批都没写（断点全部已完成的重跑）时没有导入运行，直接返回。
func (t *importArchiveTarget) Finish(ctx context.Context) error {
	if t.run == nil {
		return nil
	}
	if _, err := t.post(ctx, "/v1/import/finish", nil); err != nil {
		return err
	}
	runID := t.run.GetRunId()
	for {
		got := &archivepb.GetArchiveRunRsp{}
		if err := archiveCall(ctx, t.gateway, "GetArchiveRun", &archivepb.GetArchiveRunReq{RunId: runID}, got, got.GetRetInfo); err != nil {
			return err
		}
		switch status := got.GetRun().GetStatus(); status {
		case "queued", "running":
		case "succeeded":
			t.final = got.GetRun()
			return nil
		default:
			t.final = got.GetRun()
			return fmt.Errorf("导入运行 %s 结束状态为 %s：%s", runID, status, got.GetRun().GetMessage())
		}
		if err := archiveSleep(ctx, archiveFinishPollEvery); err != nil {
			return err
		}
	}
}

// ArchiveRun 返回导入运行的结果，写进命令的 JSON 摘要。
func (t *importArchiveTarget) ArchiveRun() *runner.ArchiveRunSummary {
	run := t.final
	if run == nil {
		run = t.run
	}
	if run == nil {
		return nil
	}
	return &runner.ArchiveRunSummary{RunID: run.GetRunId(), Status: run.GetStatus(), Message: run.GetMessage()}
}

// archiveSubjectScope 一次取全数据集的标的（按页），返回判断标的是否在标签范围内的函数（只写归档时只列进摘要、不拒绝）。
func archiveSubjectScope(ctx context.Context, meta gatewayStorageImportMetadataClient, spaceID, datasetID string) (func(context.Context, string) (bool, error), error) {
	bound := map[string]bool{}
	for page := uint32(1); ; page++ {
		rsp := &pb.ListDatasetSubjectsRsp{}
		if err := postStorage(ctx, meta.Gateway, metadataServiceName, "ListDatasetSubjects", &pb.ListDatasetSubjectsReq{
			SpaceId: spaceID, DatasetId: datasetID, Page: &pb.Page{Page: page, Size: 1000},
		}, rsp); err != nil {
			return nil, err
		}
		for _, item := range rsp.GetDatasetSubjects() {
			if item.GetStatus() != "deleted" {
				bound[item.GetSubjectId()] = true
			}
		}
		if !rsp.GetPageResult().GetHasMore() || len(rsp.GetDatasetSubjects()) == 0 {
			break
		}
	}
	return func(_ context.Context, subject string) (bool, error) { return bound[subject], nil }, nil
}
