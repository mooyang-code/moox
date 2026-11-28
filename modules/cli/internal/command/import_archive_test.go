package command

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	archivepb "github.com/mooyang-code/moox/modules/archive/proto/archivegen"
	"github.com/mooyang-code/moox/modules/cli/internal/seriesformat"
	"github.com/mooyang-code/moox/modules/cli/internal/seriesimport/runner"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func ok() *commonpb.RetInfo { return &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS} }

// archiveImportGateway 按方法回响应：GetArchiveRun 依次返回 statuses（用完后停在最后一个）。
type archiveImportGateway struct {
	mu         sync.Mutex
	calls      []string
	workerID   string
	statuses   []string
	startCode  commonpb.ErrorCode
	partitions []*archivepb.ArchivePartition
	message    string
}

func (g *archiveImportGateway) Invoke(_ context.Context, _, method string, req, rsp any) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, method)
	var out proto.Message
	switch method {
	case "StartArchiveImport":
		if g.startCode != 0 {
			out = &archivepb.StartArchiveImportRsp{RetInfo: &commonpb.RetInfo{Code: g.startCode, Msg: "任务已有运行中的 range 运行 run_x"}}
			break
		}
		out = &archivepb.StartArchiveImportRsp{RetInfo: ok(), WorkerId: g.workerID, Run: &archivepb.ArchiveRun{RunId: "run_1", Kind: "import", Status: "queued"}}
	case "GetArchiveRun":
		status := g.statuses[0]
		if len(g.statuses) > 1 {
			g.statuses = g.statuses[1:]
		}
		out = &archivepb.GetArchiveRunRsp{RetInfo: ok(), Run: &archivepb.ArchiveRun{RunId: "run_1", Kind: "import", Status: status, Message: g.message}}
	case "CancelArchiveRun":
		out = &archivepb.CancelArchiveRunRsp{RetInfo: ok()}
	case "ListArchiveRuns":
		out = &archivepb.ListArchiveRunsRsp{RetInfo: ok(), Runs: []*archivepb.ArchiveRun{{RunId: "run_range", Kind: "range", Status: "running"}}}
	case "ListArchivePartitions":
		out = &archivepb.ListArchivePartitionsRsp{RetInfo: ok(), Partitions: g.partitions, PageResult: &commonpb.PageResult{}}
	}
	proto.Merge(rsp.(proto.Message), out)
	return nil
}
func (g *archiveImportGateway) Close() error { return nil }
func (g *archiveImportGateway) Forward(context.Context, string, string, int, []byte) ([]byte, error) {
	return nil, nil
}
func (g *archiveImportGateway) called(method string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, c := range g.calls {
		if c == method {
			n++
		}
	}
	return n
}

// archiveWorkerFake 是 worker 数据端口：记下收到的批次，status 非 0 时写接口返回它。
type archiveWorkerFake struct {
	mu       sync.Mutex
	batches  []*pb.PrimaryUpsertFieldsReq
	finished int
	status   int
}

func (w *archiveWorkerFake) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		defer w.mu.Unlock()
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "run_1", r.URL.Query().Get("run"))
		if w.status != 0 {
			rw.WriteHeader(w.status)
			_ = json.NewEncoder(rw).Encode(map[string]any{"msg": "没有正在执行的这条导入运行"})
			return
		}
		switch r.URL.Path {
		case "/v1/import/rows":
			raw, _ := io.ReadAll(r.Body)
			req := &pb.PrimaryUpsertFieldsReq{}
			require.NoError(t, proto.Unmarshal(raw, req))
			w.batches = append(w.batches, req)
			_ = json.NewEncoder(rw).Encode(map[string]uint64{"added": uint64(len(req.GetRows()))})
		case "/v1/import/finish":
			w.finished++
			rw.WriteHeader(http.StatusAccepted)
		default:
			rw.WriteHeader(http.StatusNotFound)
		}
	})
}

func useArchiveImportFakes(t *testing.T, statuses ...string) (*archiveImportGateway, *archiveWorkerFake) {
	t.Helper()
	g := &archiveImportGateway{workerID: "archive-worker@factor", statuses: statuses}
	w := &archiveWorkerFake{}
	server := httptest.NewServer(w.handler(t))
	t.Cleanup(server.Close)
	previousDial, previousHost, previousSleep := dialArchiveDataPort, archiveImportWorkerHost, archiveSleep
	previousLimit := archiveStartQueueLimit
	dialArchiveDataPort = func(ctx context.Context, _ commandGateway, hostID string) (net.Conn, error) {
		assert.Equal(t, "factor", hostID)
		var d net.Dialer
		return d.DialContext(ctx, "tcp", server.Listener.Addr().String())
	}
	archiveImportWorkerHost = func(string) (string, error) { return "factor", nil }
	archiveSleep = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() {
		dialArchiveDataPort, archiveImportWorkerHost, archiveSleep, archiveStartQueueLimit = previousDial, previousHost, previousSleep, previousLimit
	})
	return g, w
}

func archiveChunk(rows int, subjects bool) runner.Chunk {
	fields := []seriesformat.Field{{Name: "close", Type: seriesformat.TypeDouble}}
	b := seriesformat.NewBatch(fields, rows)
	t0 := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	b.Cols[0] = seriesformat.Column{}
	for i := 0; i < rows; i++ {
		b.Times = append(b.Times, t0.Add(time.Duration(i)*time.Minute))
		b.Tags = append(b.Tags, "spot")
		b.Cols[0].Null = append(b.Cols[0].Null, false)
		b.Cols[0].F = append(b.Cols[0].F, float64(i))
		if subjects {
			b.Subjects = append(b.Subjects, []string{"A", "B"}[i%2])
		}
	}
	c := runner.Chunk{Space: "crypto", Dataset: "ds", Freq: "1m", Fields: fields, Batch: b}
	if !subjects {
		c.Subject = "BTC"
	}
	return c
}

func newArchiveTarget(g *archiveImportGateway) *importArchiveTarget {
	return &importArchiveTarget{gateway: g, space: "crypto", dataset: "ds", types: map[string]pb.FieldValueType{"close": pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}
}

func TestArchiveTargetStartsOnFirstWriteSplitsBatchesAndFinishes(t *testing.T) {
	g, w := useArchiveImportFakes(t, "queued", "running", "running", "succeeded")
	g.message = "导入 2500 行"
	target := newArchiveTarget(g)
	ctx := context.Background()
	require.NoError(t, target.Finish(ctx), "一批都没写时 Finish 直接返回")
	require.Zero(t, g.called("StartArchiveImport"))

	require.NoError(t, target.Write(ctx, archiveChunk(2500, false)))
	require.Equal(t, 1, g.called("StartArchiveImport"))
	require.NoError(t, target.Write(ctx, archiveChunk(10, true)))
	require.Equal(t, 1, g.called("StartArchiveImport"), "只在第一次写入时建导入运行")
	require.Len(t, w.batches, 4, "2500 行按 1000 行切成 3 个请求，再加 1 个")
	assert.Len(t, w.batches[0].GetRows(), 1000)
	assert.Len(t, w.batches[2].GetRows(), 500)
	assert.Equal(t, "ds", w.batches[0].GetDatasetId())
	assert.Equal(t, "BTC", w.batches[0].GetRows()[0].GetKey().GetTimeSeries().GetSubjectId())
	multi := w.batches[3].GetRows()
	assert.Equal(t, "A", multi[0].GetKey().GetTimeSeries().GetSubjectId())
	assert.Equal(t, "B", multi[1].GetKey().GetTimeSeries().GetSubjectId())

	require.NoError(t, target.Finish(ctx))
	assert.Equal(t, 1, w.finished)
	summary := target.ArchiveRun()
	require.NotNil(t, summary)
	assert.Equal(t, runner.ArchiveRunSummary{RunID: "run_1", Status: "succeeded", Message: "导入 2500 行"}, *summary)
}

func TestArchiveTargetFinishReportsAFailedRun(t *testing.T) {
	g, _ := useArchiveImportFakes(t, "running", "failed")
	g.message = "导入批次不合法"
	target := newArchiveTarget(g)
	require.NoError(t, target.Write(context.Background(), archiveChunk(3, false)))
	err := target.Finish(context.Background())
	require.ErrorContains(t, err, "结束状态为 failed")
	require.ErrorContains(t, err, "导入批次不合法")
}

func TestArchiveTargetReportsAMissingRunWithTheRerunHint(t *testing.T) {
	g, w := useArchiveImportFakes(t, "running")
	w.status = http.StatusNotFound
	err := newArchiveTarget(g).Write(context.Background(), archiveChunk(3, false))
	require.ErrorContains(t, err, "HTTP 404")
	require.ErrorContains(t, err, "重跑同一条命令")
}

func TestArchiveTargetCancelsWhenTheWorkerHostDoesNotMatch(t *testing.T) {
	g, w := useArchiveImportFakes(t, "running")
	g.workerID = "archive-worker@control"
	err := newArchiveTarget(g).Write(context.Background(), archiveChunk(3, false))
	require.ErrorContains(t, err, "两边不一致")
	assert.Equal(t, 1, g.called("CancelArchiveRun"))
	assert.Empty(t, w.batches)
}

func TestArchiveTargetCancelsAfterQueueingTooLongAndNamesTheBlockingRun(t *testing.T) {
	g, _ := useArchiveImportFakes(t, "queued")
	archiveStartQueueLimit = 0
	err := newArchiveTarget(g).Write(context.Background(), archiveChunk(3, false))
	require.ErrorContains(t, err, "排队超过")
	require.ErrorContains(t, err, "run_range")
	assert.Equal(t, 1, g.called("CancelArchiveRun"))
}

func TestArchiveTargetConflictHintsTheCancelCommand(t *testing.T) {
	g, _ := useArchiveImportFakes(t, "running")
	g.startCode = commonpb.ErrorCode_CONFLICT
	err := newArchiveTarget(g).Write(context.Background(), archiveChunk(3, false))
	require.ErrorContains(t, err, "run_x")
	require.ErrorContains(t, err, "moox-cli archive runs cancel")
}

func TestCheckArchiveDiskCountsRegisteredPartitions(t *testing.T) {
	g := &archiveImportGateway{}
	summary := runner.Summary{Rows: 1000, Files: []runner.FileSummary{{First: "2020-01-01T00:00:00Z", Last: "2020-01-02T00:00:00Z"}}}
	// 每行 (40 + 10×1) × 1.5 = 75 字节；1000 行要 75000 字节。
	enough := archiveImportCheck{DiskFree: 200000 + 100000, MinFree: 100000}
	require.NoError(t, checkArchiveDisk(context.Background(), g, "crypto", "ds", enough, summary, 1))

	g.partitions = []*archivepb.ArchivePartition{
		{RangeStart: "2020-01-01T00:00:00Z", RangeEnd: "2020-01-02T00:00:00Z", RowCount: 5000},
		{RangeStart: "2019-11-01T00:00:00Z", RangeEnd: "2019-11-02T00:00:00Z", RowCount: 1 << 30}, // 不在时间范围内，不计
	}
	err := checkArchiveDisk(context.Background(), g, "crypto", "ds", enough, summary, 1)
	require.ErrorContains(t, err, "按时间拆成几次导入")
	require.True(t, strings.Contains(err.Error(), "已登记分区 5000 行"), err.Error())
}
