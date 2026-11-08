package command

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// importFakeGateway 按方法回固定响应，记下调用顺序与请求。
type importFakeGateway struct {
	calls    *[]string
	requests map[string]proto.Message
	runs     []string // GetArchiveRun 依次返回的状态
	// active 按状态给 ListArchiveRuns 返回的运行；workerID 非空时替换 PrepareArchiveImport 的 worker；importConflict 时 ImportArchiveFile 返回冲突。
	active         map[string][]*archivepb.ArchiveRun
	workerID       string
	importConflict bool
}

func (g *importFakeGateway) Invoke(_ context.Context, _, method string, req, rsp any) error {
	*g.calls = append(*g.calls, method)
	g.requests[method] = req.(proto.Message)
	var out proto.Message
	switch method {
	case "ListDatasetColumns":
		out = &pb.ListDatasetColumnsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Columns: []*pb.DatasetColumn{
			{ColumnName: "open", OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, Status: "active"},
			{ColumnName: "close", OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD},
			{ColumnName: "old", OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, Status: "disabled"},
			{ColumnName: "freq", OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_SYSTEM},
		}}
	case "ListArchiveRuns":
		out = &archivepb.ListArchiveRunsRsp{RetInfo: ok(), Runs: g.active[req.(*archivepb.ListArchiveRunsReq).GetStatus()]}
	case "PrepareArchiveImport":
		worker := "archive-worker@factor"
		if g.workerID != "" {
			worker = g.workerID
		}
		out = &archivepb.PrepareArchiveImportRsp{RetInfo: ok(), RootDir: "/data/archive", WorkerId: worker, ImportId: "imp_1", ImportDir: "/data/archive/.import/imp_1"}
	case "ImportArchiveFile":
		if g.importConflict {
			out = &archivepb.ImportArchiveFileRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_CONFLICT, Msg: "任务已有排队中或运行中的运行，等它结束后再导入"}}
			break
		}
		out = &archivepb.ImportArchiveFileRsp{RetInfo: ok(), Run: &archivepb.ArchiveRun{RunId: "run_1", Kind: "import", Status: "queued"}}
	case "GetArchiveRun":
		status := g.runs[0]
		g.runs = g.runs[1:]
		out = &archivepb.GetArchiveRunRsp{RetInfo: ok(), Run: &archivepb.ArchiveRun{RunId: "run_1", Kind: "import", Status: status, Message: "导入 2 行"}}
	}
	proto.Merge(rsp.(proto.Message), out)
	return nil
}
func (g *importFakeGateway) Close() error { return nil }
func (g *importFakeGateway) Forward(context.Context, string, string, int, []byte) ([]byte, error) {
	return nil, nil
}

type importFakeRemote struct {
	calls    *[]string
	host     string
	argv     []string
	dst      string
	mode     fs.FileMode
	uploaded []byte
}

func (r *importFakeRemote) Exec(_ context.Context, host string, argv []string, _ io.Reader) (setupssh.Result, error) {
	*r.calls = append(*r.calls, "exec")
	r.host, r.argv = host, argv
	return setupssh.Result{}, nil
}

func (r *importFakeRemote) Upload(_ context.Context, host string, src io.Reader, _ int64, dst string, mode fs.FileMode) error {
	*r.calls = append(*r.calls, "upload")
	r.host, r.dst, r.mode = host, dst, mode
	r.uploaded, _ = io.ReadAll(src)
	return nil
}
func (r *importFakeRemote) Close() { *r.calls = append(*r.calls, "close") }

func useImportFakes(t *testing.T, runs ...string) (*importFakeGateway, *importFakeRemote, *[]string) {
	t.Helper()
	var calls []string
	g := &importFakeGateway{calls: &calls, requests: map[string]proto.Message{}, runs: runs}
	r := &importFakeRemote{calls: &calls}
	previousGateway, previousRemote, previousSleep := openCommandGateway, openArchiveImportRemote, archiveSleep
	openCommandGateway = func(context.Context, string, *setupconfig.Snapshot) (commandGateway, error) { return g, nil }
	openArchiveImportRemote = func(string) (archiveImportRemote, string, error) { return r, "factor", nil }
	archiveSleep = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() {
		openCommandGateway, openArchiveImportRemote, archiveSleep = previousGateway, previousRemote, previousSleep
	})
	return g, r, &calls
}

func writeLocalCSV(t *testing.T, name, content string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), name)
	var raw []byte
	if filepath.Ext(name) == ".gz" {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write([]byte(content))
		_ = zw.Close()
		raw = buf.Bytes()
	} else {
		raw = []byte(content)
	}
	require.NoError(t, os.WriteFile(file, raw, 0o644))
	return file
}

func TestArchiveImportTemplatePrintsTheWorkerColumns(t *testing.T) {
	_, _, calls := useImportFakes(t)
	var out bytes.Buffer
	require.NoError(t, runArchiveImportTemplate(context.Background(), &out, archiveFlags{SpaceID: "crypto", DatasetID: "ds"}))
	// 停用列与系统列不在内，值列按列名排序（与 worker 同一口径）。
	assert.Equal(t, "data_time,subject_id,series_tag,close,open\n", out.String())
	assert.Equal(t, []string{"ListDatasetColumns"}, *calls)
}

func TestArchiveImportPrechecksMakesTheDirectoryThenUploadsAndWaits(t *testing.T) {
	g, r, calls := useImportFakes(t, "queued", "running", "succeeded")
	local := writeLocalCSV(t, "我的 数据.csv.gz", "data_time,subject_id,close,open\n2026-09-01T00:00:00Z,BTC,1,1\n")
	var out bytes.Buffer
	require.NoError(t, runArchiveImport(context.Background(), &out, archiveFlags{SpaceID: "crypto", DatasetID: "ds", CSV: local, Overwrite: true, Wait: true, Yes: true}))
	assert.Equal(t, []string{"ListDatasetColumns", "ListArchiveRuns", "ListArchiveRuns", "PrepareArchiveImport", "exec", "upload", "close", "ImportArchiveFile", "GetArchiveRun", "GetArchiveRun", "GetArchiveRun"}, *calls, "上传完就关掉 SSH 连接，等待运行期间不占着")
	assert.Equal(t, "factor", r.host)
	assert.Equal(t, []string{"mkdir", "-p", "-m", "0700", "/data/archive/.import/imp_1"}, r.argv)
	assert.Equal(t, "/data/archive/.import/imp_1/import.csv.gz", r.dst, "上传用固定文件名，不带本地文件名里的空格与中文")
	assert.Equal(t, fs.FileMode(0o600), r.mode)
	raw, _ := os.ReadFile(local)
	assert.Equal(t, raw, r.uploaded, "原样上传（压缩的照样压缩）")
	req := g.requests["ImportArchiveFile"].(*archivepb.ImportArchiveFileReq)
	assert.Equal(t, "imp_1", req.GetImportId())
	assert.Equal(t, "import.csv.gz", req.GetFileName())
	assert.True(t, req.GetOverwrite())
	assert.Contains(t, out.String(), `"status": "succeeded"`)
}

func TestArchiveImportWithoutWaitPrintsTheRunAndFailedRunsReturnAnError(t *testing.T) {
	_, _, calls := useImportFakes(t, "failed")
	local := writeLocalCSV(t, "a.csv", "data_time,subject_id,series_tag,close\n")
	var out bytes.Buffer
	require.NoError(t, runArchiveImport(context.Background(), &out, archiveFlags{SpaceID: "crypto", DatasetID: "ds", CSV: local, Yes: true}))
	assert.Contains(t, out.String(), `"run_id": "run_1"`)
	assert.NotContains(t, *calls, "GetArchiveRun")

	_, _, _ = useImportFakes(t, "failed")
	err := runArchiveImport(context.Background(), &out, archiveFlags{SpaceID: "crypto", DatasetID: "ds", CSV: local, Yes: true, Wait: true})
	require.ErrorContains(t, err, "结束状态为 failed")
}

func TestArchiveImportRejectsBadInputsBeforeTouchingTheWorkerHost(t *testing.T) {
	for name, tc := range map[string]struct {
		file, content string
		flags         archiveFlags
		want          string
	}{
		"缺数据集":   {"a.csv", "data_time,subject_id\n", archiveFlags{SpaceID: "crypto"}, "--dataset-id"},
		"缺文件":    {"", "", archiveFlags{}, "--csv 必填"},
		"扩展名":    {"a.txt", "x", archiveFlags{}, "只接受 .csv 或 .csv.gz"},
		"没确认":    {"a.csv", "data_time,subject_id\n", archiveFlags{}, "--yes"},
		"未知列":    {"a.csv", "data_time,subject_id,volume\n", archiveFlags{}, "数据集没有的列：volume"},
		"缺主键":    {"a.csv", "data_time,close\n", archiveFlags{}, "data_time 与 subject_id"},
		"重复列":    {"a.csv", "data_time,subject_id,close,close\n", archiveFlags{}, "重复"},
		"覆盖缺列":   {"a.csv", "data_time,subject_id,close\n", archiveFlags{Overwrite: true}, "缺少：open"},
		"空文件":    {"a.csv", "", archiveFlags{}, "空文件"},
		"压缩文件坏了": {"a.csv.gz.bad.csv", "\x1f\x8bxx", archiveFlags{}, "解压"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, calls := useImportFakes(t)
			f := tc.flags
			if f.SpaceID == "" {
				f.SpaceID = "crypto"
			}
			if f.DatasetID == "" && name != "缺数据集" {
				f.DatasetID = "ds"
			}
			f.Yes = tc.want != "--yes"
			if tc.file != "" {
				f.CSV = writeLocalCSV(t, tc.file, tc.content)
			}
			err := runArchiveImport(context.Background(), io.Discard, f)
			require.ErrorContains(t, err, tc.want)
			assert.NotContains(t, *calls, "exec")
			assert.NotContains(t, *calls, "PrepareArchiveImport")
		})
	}
}

func TestArchiveClearUnrecoverableNeedsTheRangeAndConfirmation(t *testing.T) {
	g := &archiveFakeGateway{rsp: &archivepb.ClearArchiveUnrecoverableRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
		Removed: []*archivepb.UnrecoverableRange{{StartTime: "2026-10-01T00:00:00Z", EndTime: "2026-10-02T00:00:00Z"}}}}
	useArchiveGateway(t, g)
	var out bytes.Buffer
	f := archiveFlags{SpaceID: "crypto", DatasetID: "ds", From: "2026-10-01T00:00:00Z", To: "2026-10-05T00:00:00Z"}
	require.ErrorContains(t, runArchiveTasksClearUnrecoverable(context.Background(), &out, archiveFlags{SpaceID: "crypto", DatasetID: "ds"}), "--start")
	require.ErrorContains(t, runArchiveTasksClearUnrecoverable(context.Background(), &out, f), "--yes")
	assert.Empty(t, g.method)
	f.Yes = true
	require.NoError(t, runArchiveTasksClearUnrecoverable(context.Background(), &out, f))
	assert.Equal(t, "ClearArchiveUnrecoverable", g.method)
	req := g.req.(*archivepb.ClearArchiveUnrecoverableReq)
	assert.Equal(t, "2026-10-01T00:00:00Z", req.GetStart())
	assert.Equal(t, "2026-10-05T00:00:00Z", req.GetEnd())
	assert.Contains(t, out.String(), `"removed"`)
}

func TestArchiveImportCommandIsMountedWithItsFlags(t *testing.T) {
	cmd, rest, err := archiveCmd.Find([]string{"import"})
	require.NoError(t, err)
	assert.Empty(t, rest)
	for _, flag := range []string{"file", "space-id", "dataset-id", "csv", "overwrite", "wait", "import-id", "yes"} {
		assert.NotNil(t, cmd.Flags().Lookup(flag), flag)
	}
	template, _, err := archiveCmd.Find([]string{"import", "template"})
	require.NoError(t, err)
	assert.Equal(t, "template", template.Name())
	clear, _, err := archiveCmd.Find([]string{"tasks", "clear-unrecoverable"})
	require.NoError(t, err)
	assert.NotNil(t, clear.Flags().Lookup("start"))
}

func TestArchiveImportHeaderIgnoresAUTF8BOM(t *testing.T) {
	header, err := readArchiveCSVHeader(writeLocalCSV(t, "a.csv", "\ufeffdata_time,subject_id,close\n"))
	require.NoError(t, err)
	assert.Equal(t, []string{"data_time", "subject_id", "close"}, header)
}

func TestArchiveImportRefusesToUploadWhileTheTaskHasAnActiveRun(t *testing.T) {
	local := writeLocalCSV(t, "a.csv", "data_time,subject_id,close\n")
	for name, tc := range map[string]struct {
		active map[string][]*archivepb.ArchiveRun
		want   string
	}{
		"排队中的复核": {map[string][]*archivepb.ArchiveRun{"queued": {{RunId: "run_q", Kind: "scheduled", Status: "queued"}}}, "排队中的运行（run_q，scheduled）"},
		"运行中的补拉": {map[string][]*archivepb.ArchiveRun{"running": {{RunId: "run_r", Kind: "range", Status: "running"}}}, "运行中的运行（run_r，range）"},
	} {
		t.Run(name, func(t *testing.T) {
			g, _, calls := useImportFakes(t)
			g.active = tc.active
			err := runArchiveImport(context.Background(), io.Discard, archiveFlags{SpaceID: "crypto", DatasetID: "ds", CSV: local, Yes: true})
			require.ErrorContains(t, err, tc.want)
			require.ErrorContains(t, err, "还没有上传文件")
			assert.NotContains(t, *calls, "PrepareArchiveImport")
			assert.NotContains(t, *calls, "upload")
			req := g.requests["ListArchiveRuns"].(*archivepb.ListArchiveRunsReq)
			assert.Equal(t, "crypto", req.GetSpaceId())
			assert.Equal(t, "ds", req.GetDatasetId())
		})
	}

	// 排队中的到点拉取会给导入让位，不挡上传。
	g, _, calls := useImportFakes(t)
	g.active = map[string][]*archivepb.ArchiveRun{"queued": {{RunId: "run_t", Kind: "tick", Status: "queued"}}}
	require.NoError(t, runArchiveImport(context.Background(), io.Discard, archiveFlags{SpaceID: "crypto", DatasetID: "ds", CSV: local, Yes: true}))
	assert.Contains(t, *calls, "upload")
}

func TestArchiveImportRefusesToUploadWhenTheWorkerIsNotOnTheDeployedHost(t *testing.T) {
	g, _, calls := useImportFakes(t)
	g.workerID = "archive-worker@old"
	local := writeLocalCSV(t, "a.csv", "data_time,subject_id,close\n")
	err := runArchiveImport(context.Background(), io.Discard, archiveFlags{SpaceID: "crypto", DatasetID: "ds", CSV: local, Yes: true})
	require.ErrorContains(t, err, `活动的归档 worker 是 "archive-worker@old"`)
	require.ErrorContains(t, err, `主机是 factor（对应 worker "archive-worker@factor"）`)
	assert.NotContains(t, *calls, "exec")
	assert.NotContains(t, *calls, "upload")
	assert.NotContains(t, *calls, "ImportArchiveFile")
}

func TestArchiveImportConflictPrintsTheImportIDAndTheRetryCommand(t *testing.T) {
	g, _, calls := useImportFakes(t)
	g.importConflict = true
	local := writeLocalCSV(t, "我的 数据.csv", "data_time,subject_id,close,open\n")
	err := runArchiveImport(context.Background(), io.Discard, archiveFlags{File: "/etc/moox.toml", SpaceID: "crypto", DatasetID: "ds", CSV: local, Overwrite: true, Yes: true})
	require.ErrorContains(t, err, "导入 ID：imp_1")
	require.ErrorContains(t, err, "moox-cli archive import --file /etc/moox.toml --space-id crypto --dataset-id ds --csv '"+local+"' --import-id imp_1 --overwrite --yes")
	assert.Contains(t, *calls, "upload")
}

func TestArchiveImportWithAnImportIDSkipsTheUpload(t *testing.T) {
	g, _, calls := useImportFakes(t)
	local := writeLocalCSV(t, "a.csv.gz", "data_time,subject_id,close\n")
	require.NoError(t, runArchiveImport(context.Background(), io.Discard, archiveFlags{SpaceID: "crypto", DatasetID: "ds", CSV: local, ImportID: "imp_old", Yes: true}))
	assert.Equal(t, []string{"ListDatasetColumns", "ListArchiveRuns", "ListArchiveRuns", "ImportArchiveFile"}, *calls)
	req := g.requests["ImportArchiveFile"].(*archivepb.ImportArchiveFileReq)
	assert.Equal(t, "imp_old", req.GetImportId())
	assert.Equal(t, "import.csv.gz", req.GetFileName())

	for _, bad := range []string{"../x", ".hidden", "a/b", " imp"} {
		_, _, calls := useImportFakes(t)
		err := runArchiveImport(context.Background(), io.Discard, archiveFlags{SpaceID: "crypto", DatasetID: "ds", CSV: local, ImportID: bad, Yes: true})
		require.ErrorContains(t, err, "--import-id", bad)
		assert.Empty(t, *calls, bad)
	}
}
