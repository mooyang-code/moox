package command

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/seriesformat"
	"github.com/mooyang-code/moox/modules/cli/internal/seriesimport/runner"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"google.golang.org/protobuf/proto"
)

// 导入的因子机本地目标：经操作员网关调因子引擎 EngineControl 的本地写入方法（只给 moox-cli）。因子机只能出站时，
// 网关客户端经 control 中转到达因子机的主机网关，再转给引擎的回环端口。本地存储同一时刻只能由引擎进程打开，
// 所以写入只能经过引擎；引擎未就绪时也接受写入。

const engineControl = "trpc.moox.factor.EngineControl"

// factorEngine 是因子引擎本地写入方法的客户端。
type factorEngine struct {
	Gateway gatewayclient.Invoker
}

func (e factorEngine) call(ctx context.Context, method string, req, rsp any, ret func() *commonpb.RetInfo) error {
	if err := e.Gateway.Invoke(ctx, engineControl, method, req, rsp); err != nil {
		return fmt.Errorf("调用因子引擎 %s：%w", method, err)
	}
	if info := ret(); info.GetCode() != commonpb.ErrorCode_SUCCESS {
		return fmt.Errorf("因子引擎 %s 失败（%s）：%s", method, info.GetCode(), info.GetMsg())
	}
	return nil
}

// Fields 返回数据集的字段表。
func (e factorEngine) Fields(ctx context.Context, space, dataset, freq string) ([]seriesformat.Field, error) {
	rsp := &factorpb.GetSeriesFieldsRsp{}
	if err := e.call(ctx, "GetSeriesFields", &factorpb.GetSeriesFieldsReq{SpaceId: space, DatasetId: dataset, Frequency: freq}, rsp, rsp.GetRetInfo); err != nil {
		return nil, err
	}
	out := make([]seriesformat.Field, 0, len(rsp.GetFields()))
	for _, f := range rsp.GetFields() {
		out = append(out, seriesformat.Field{Name: f.GetName(), Type: seriesformat.FieldType(f.GetType())})
	}
	return out, nil
}

// SubjectInScope 判断标的是否在数据集的标签范围内。
func (e factorEngine) SubjectInScope(ctx context.Context, space, dataset, subject string) (bool, error) {
	rsp := &factorpb.CheckSeriesSubjectRsp{}
	if err := e.call(ctx, "CheckSeriesSubject", &factorpb.CheckSeriesSubjectReq{SpaceId: space, DatasetId: dataset, SubjectId: subject}, rsp, rsp.GetRetInfo); err != nil {
		return false, err
	}
	return rsp.GetInScope(), nil
}

// LocalBounds 返回标的在因子机本地的最早与最新时间（补更早的历史时批要从后往前写）。
func (e factorEngine) LocalBounds(space, dataset, freq string) func(ctx context.Context, subject string) (time.Time, time.Time, bool, error) {
	return func(ctx context.Context, subject string) (time.Time, time.Time, bool, error) {
		rsp := &factorpb.GetSeriesBoundsRsp{}
		if err := e.call(ctx, "GetSeriesBounds", &factorpb.GetSeriesBoundsReq{SpaceId: space, DatasetId: dataset, Frequency: freq, SubjectId: subject}, rsp, rsp.GetRetInfo); err != nil || !rsp.GetExists() {
			return time.Time{}, time.Time{}, false, err
		}
		earliest, err := time.Parse(time.RFC3339, rsp.GetEarliest())
		if err != nil {
			return time.Time{}, time.Time{}, false, err
		}
		latest, err := time.Parse(time.RFC3339, rsp.GetLatest())
		return earliest, latest, err == nil, err
	}
}

// factorTarget 实现 runner.Target：一个批对应一次整段替换。
type factorTarget struct {
	engine factorEngine
	// limit 是 WriteSeries 的包体上限（服务目录里 EngineControl 的 max_body_bytes）。
	limit int64
}

func newFactorTarget(engine factorEngine) *factorTarget {
	limit := servicecatalog.DefaultMaxBodyBytes
	if catalog, err := servicecatalog.LoadEmbedded(); err == nil {
		if service, ok := catalog.Service(engineControl); ok && service.MaxBodyBytes > 0 {
			limit = service.MaxBodyBytes
		}
	}
	return &factorTarget{engine: engine, limit: limit}
}

// Name 实现 runner.Target。
func (t *factorTarget) Name() string { return "factor" }

// Write 实现 runner.Target。编码后超过包体上限时直接报错并提示减小批大小：同一批要整段替换，不能拆成几次写。
func (t *factorTarget) Write(ctx context.Context, c runner.Chunk) error {
	req := writeSeriesRequest(c)
	if size := int64(proto.Size(req)); size > t.limit {
		return fmt.Errorf("标的 %s 的这一批（%d 行）编码后 %d 字节，超过因子引擎写入接口的上限 %d 字节：用更小的 --batch-rows 重跑（已写完的批按断点续传跳过）",
			c.Subject, c.Batch.Len(), size, t.limit)
	}
	rsp := &factorpb.WriteSeriesRsp{}
	if err := t.engine.Gateway.Invoke(ctx, engineControl, "WriteSeries", req, rsp); err != nil {
		return fmt.Errorf("调用因子引擎 WriteSeries：%w", err)
	}
	if info := rsp.GetRetInfo(); info.GetCode() != commonpb.ErrorCode_SUCCESS {
		return fmt.Errorf("因子引擎拒绝写入（%s）：%s", info.GetCode(), info.GetMsg())
	}
	return nil
}

// writeSeriesRequest 把一批行转成 WriteSeries 请求：每列按字段名给出 null 标记与按类型的值。
func writeSeriesRequest(c runner.Chunk) *factorpb.WriteSeriesReq {
	req := &factorpb.WriteSeriesReq{
		SpaceId: c.Space, DatasetId: c.Dataset, Frequency: c.Freq, SubjectId: c.Subject,
		From: c.From.UTC().Format(time.RFC3339), To: c.To.UTC().Format(time.RFC3339),
		AllowMissing: c.AllowMissing, RecordPending: c.RecordPending, Tags: c.Batch.Tags,
		Cols: make(map[string]*factorpb.SeriesColumn, len(c.Fields)),
	}
	if c.RecordPending {
		req.Reason = "import"
	}
	req.Times = make([]int64, c.Batch.Len())
	for i, at := range c.Batch.Times {
		req.Times[i] = at.Unix()
	}
	for i, field := range c.Fields {
		col := c.Batch.Cols[i]
		out := &factorpb.SeriesColumn{Null: col.Null}
		switch field.Type {
		case seriesformat.TypeDouble:
			out.F = col.F
		case seriesformat.TypeInt, seriesformat.TypeBool:
			out.I = col.I
		default:
			out.S = col.S
		}
		req.Cols[field.Name] = out
	}
	return req
}
