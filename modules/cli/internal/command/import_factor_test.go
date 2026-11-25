package command

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/seriesformat"
	"github.com/mooyang-code/moox/modules/cli/internal/seriesimport/runner"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// engineGateway 记下发给因子引擎的本地写入请求，并按方法回答。
type engineGateway struct {
	writes []*factorpb.WriteSeriesReq
	reject string
}

func (g *engineGateway) Invoke(_ context.Context, service, method string, req, rsp any) error {
	if service != engineControl {
		return errors.New("unexpected service " + service)
	}
	switch method {
	case "WriteSeries":
		g.writes = append(g.writes, req.(*factorpb.WriteSeriesReq))
		out := rsp.(*factorpb.WriteSeriesRsp)
		out.RetInfo = &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}
		if g.reject != "" {
			out.RetInfo = &commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM, Msg: g.reject}
		}
	case "GetSeriesFields":
		out := rsp.(*factorpb.GetSeriesFieldsRsp)
		out.RetInfo = &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}
		out.Fields = []*factorpb.SeriesField{{Name: "close", Type: "double", Fill: "prev_close"}, {Name: "trades", Type: "int", Fill: "zero"}}
	case "CheckSeriesSubject":
		out := rsp.(*factorpb.CheckSeriesSubjectRsp)
		out.RetInfo = &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}
		out.InScope = req.(*factorpb.CheckSeriesSubjectReq).GetSubjectId() == "BTC"
	case "GetSeriesBounds":
		out := rsp.(*factorpb.GetSeriesBoundsRsp)
		out.RetInfo = &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}
		if req.(*factorpb.GetSeriesBoundsReq).GetSubjectId() == "BTC" {
			out.Exists, out.Earliest, out.Latest = true, "2026-10-01T00:00:00Z", "2026-10-01T00:04:00Z"
		}
	default:
		return errors.New("unexpected method " + method)
	}
	return nil
}

func importChunk(fields []seriesformat.Field, rows int) runner.Chunk {
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	b := seriesformat.NewBatch(fields, rows)
	for i := 0; i < rows; i++ {
		b.Times = append(b.Times, t0.Add(time.Duration(i)*time.Minute))
		b.Tags = append(b.Tags, "")
	}
	b.Cols = make([]seriesformat.Column, len(fields))
	for fi, field := range fields {
		col := seriesformat.Column{Null: make([]bool, rows)}
		for i := 0; i < rows; i++ {
			switch field.Type {
			case seriesformat.TypeDouble:
				col.F = append(col.F, 60000.123456+float64(i))
			case seriesformat.TypeInt, seriesformat.TypeBool:
				col.I = append(col.I, int64(i))
			default:
				col.S = append(col.S, "x")
			}
		}
		b.Cols[fi] = col
	}
	return runner.Chunk{Space: "crypto", Dataset: "spot", Freq: "1m", Subject: "BTC", Fields: fields, Batch: b,
		From: t0, To: t0.Add(time.Duration(rows) * time.Minute), AllowMissing: true, RecordPending: true}
}

// 导入的因子机目标经网关调 EngineControl：一批对应一次 WriteSeries，列按类型放进 f、i、s，带 --recalc 时记待办（原因 import）；
// 字段表、标的范围与本地边界也都经网关读。
func TestFactorImportTargetWritesThroughTheGateway(t *testing.T) {
	gateway := &engineGateway{}
	engine := factorEngine{Gateway: gateway}
	ctx := context.Background()

	fields, err := engine.Fields(ctx, "crypto", "spot", "1m")
	require.NoError(t, err)
	require.Equal(t, []seriesformat.Field{{Name: "close", Type: seriesformat.TypeDouble}, {Name: "trades", Type: seriesformat.TypeInt}}, fields)
	in, err := engine.SubjectInScope(ctx, "crypto", "spot", "BTC")
	require.NoError(t, err)
	require.True(t, in)
	earliest, latest, ok, err := engine.LocalBounds("crypto", "spot", "1m")(ctx, "BTC")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), earliest)
	require.Equal(t, time.Date(2026, 10, 1, 0, 4, 0, 0, time.UTC), latest)
	_, _, ok, err = engine.LocalBounds("crypto", "spot", "1m")(ctx, "ETH")
	require.NoError(t, err)
	require.False(t, ok, "本地没有这个标的")

	target := newFactorTarget(engine)
	require.Equal(t, "factor", target.Name())
	require.NoError(t, target.Write(ctx, importChunk(fields, 3)))
	require.Len(t, gateway.writes, 1)
	req := gateway.writes[0]
	require.Equal(t, "BTC", req.GetSubjectId())
	require.Equal(t, "2026-10-01T00:00:00Z", req.GetFrom())
	require.Equal(t, "2026-10-01T00:03:00Z", req.GetTo())
	require.True(t, req.GetAllowMissing())
	require.True(t, req.GetRecordPending())
	require.Equal(t, "import", req.GetReason())
	require.Len(t, req.GetTimes(), 3)
	require.Len(t, req.GetCols()["close"].GetF(), 3)
	require.Len(t, req.GetCols()["trades"].GetI(), 3)
	require.Empty(t, req.GetCols()["trades"].GetF())

	gateway.reject = "缺少周期 2026-10-01T00:02:00Z"
	require.ErrorContains(t, target.Write(ctx, importChunk(fields, 3)), "因子引擎拒绝写入（INVALID_PARAM）：缺少周期")
}

// 默认一批一万行：40 个 double 列编码后仍在 EngineControl 的包体上限（默认 4 MiB）之内；超过上限时不发请求，提示减小 --batch-rows。
func TestFactorImportBatchFitsTheBodyLimit(t *testing.T) {
	var fields []seriesformat.Field
	for i := 0; i < 40; i++ {
		fields = append(fields, seriesformat.Field{Name: fmt.Sprintf("f%02d", i), Type: seriesformat.TypeDouble})
	}
	chunk := importChunk(fields, 10000)
	size := proto.Size(writeSeriesRequest(chunk))
	t.Logf("一万行、40 个 double 列编码后 %d 字节", size)
	require.Less(t, int64(size), servicecatalog.DefaultMaxBodyBytes, "一万行 40 列编码后 %d 字节", size)

	gateway := &engineGateway{}
	target := newFactorTarget(factorEngine{Gateway: gateway})
	require.Equal(t, servicecatalog.DefaultMaxBodyBytes, target.limit)
	target.limit = int64(size) - 1
	require.ErrorContains(t, target.Write(context.Background(), chunk), "--batch-rows")
	require.Empty(t, gateway.writes, "超过上限不发请求")
}
