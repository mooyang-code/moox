package rpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/preview"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/factorexpr"
	"github.com/stretchr/testify/require"
)

func TestValidateFactorSpecReturnsDerivedContract(t *testing.T) {
	svc := NewService(&catalogFake{}, &recalcFake{})
	rsp, err := svc.ValidateFactorSpec(context.Background(), &factorpb.ValidateFactorSpecReq{
		Spec: "params: {window: [5, 20]}\noutputs:\n  m_{window}: ts_mean(close, window)\n",
	})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.True(t, rsp.GetValid())
	require.Equal(t, domain.FactorTypeTimeSeries, rsp.GetFactorType())
	require.Equal(t, []string{"close"}, rsp.GetInputColumns())
	require.Equal(t, []string{"m_5", "m_20"}, rsp.GetOutputs())
	require.Equal(t, int32(20), rsp.GetLookbackPeriods())
	require.NotEmpty(t, rsp.GetCanonical())
	require.NotEmpty(t, rsp.GetDefinitionHash())
}

func TestValidateFactorSpecReportsDiagnosticWithPosition(t *testing.T) {
	svc := NewService(&catalogFake{}, &recalcFake{})
	rsp, err := svc.ValidateFactorSpec(context.Background(), &factorpb.ValidateFactorSpecReq{
		Spec: "outputs:\n  value: ts_mean(close, 5) + nope(open)\n",
	})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), "定义不合法不是接口错误")
	require.False(t, rsp.GetValid())
	require.Contains(t, rsp.GetDiagnostic().GetMessage(), "nope")
	require.Equal(t, "outputs.value", rsp.GetDiagnostic().GetPath())
	require.Positive(t, rsp.GetDiagnostic().GetColumn())
}

func TestListFactorFunctionsCoversRegistry(t *testing.T) {
	rsp, err := NewService(&catalogFake{}, &recalcFake{}).ListFactorFunctions(context.Background(), &factorpb.ListFactorFunctionsReq{})
	require.NoError(t, err)
	require.Equal(t, factorexpr.ExprVersion, rsp.GetExprVersion())
	require.Len(t, rsp.GetFunctions(), len(factorexpr.FunctionDocs()))
	names := map[string]*factorpb.FactorFunction{}
	for _, function := range rsp.GetFunctions() {
		names[function.GetName()] = function
	}
	require.Contains(t, names, "ts_mean")
	require.Equal(t, "timeseries", names["ts_mean"].GetCategory())
	require.NotEmpty(t, names["ts_mean"].GetArgs())
	require.NotEmpty(t, names["ts_mean"].GetDoc())
}

type previewerFake struct {
	got preview.Request
	err error
}

func (f *previewerFake) Preview(_ context.Context, req preview.Request) (preview.Result, error) {
	f.got = req
	if f.err != nil {
		return preview.Result{}, f.err
	}
	value := 1.5
	at := time.Date(2026, 10, 17, 1, 2, 0, 0, time.UTC)
	return preview.Result{Columns: []string{"m"}, Rows: []preview.Row{
		{Subject: "BTC", Tag: "", DataTime: at, Values: []*float64{&value}},
		{Subject: "BTC", Tag: "", DataTime: at.Add(time.Minute), Values: []*float64{nil}},
	}}, nil
}

func TestPreviewFactorCompilesAndConvertsNulls(t *testing.T) {
	set := domain.FactorSet{SetID: "fset_a", SpaceID: "crypto", SourceDatasetID: "dataset_a", Freq: "1m"}
	previewer := &previewerFake{}
	svc := NewService(&catalogFake{set: set}, &recalcFake{}, WithPreviewer(previewer))
	rsp, err := svc.PreviewFactor(context.Background(), &factorpb.PreviewFactorReq{
		SetId: "fset_a", Spec: "outputs:\n  m: ts_mean(close, 3)\n", Subjects: []string{"BTC"}, Periods: 2,
	})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.Equal(t, set, previewer.got.Set)
	require.Equal(t, []string{"BTC"}, previewer.got.Subjects)
	require.Equal(t, 2, previewer.got.Periods)
	require.NotNil(t, previewer.got.Factor.Compiled())
	require.Equal(t, []string{"m"}, rsp.GetColumns())
	require.Len(t, rsp.GetRows(), 2)
	require.Equal(t, []float64{1.5}, rsp.GetRows()[0].GetValues())
	require.Equal(t, []bool{false}, rsp.GetRows()[0].GetNulls())
	require.Equal(t, []bool{true}, rsp.GetRows()[1].GetNulls())
	require.Equal(t, "2026-10-17T01:02:00Z", rsp.GetRows()[0].GetDataTime())

	bad, err := svc.PreviewFactor(context.Background(), &factorpb.PreviewFactorReq{SetId: "fset_a", Spec: "outputs: {m: nope(close)}"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, bad.GetRetInfo().GetCode())
	previewer.err = errors.New("源数据集没有输入列 close")
	failed, err := svc.PreviewFactor(context.Background(), &factorpb.PreviewFactorReq{SetId: "fset_a", Spec: "outputs:\n  m: close\n"})
	require.NoError(t, err)
	require.Contains(t, failed.GetRetInfo().GetMsg(), "没有输入列")

	disabled, err := NewService(&catalogFake{set: set}, &recalcFake{}).PreviewFactor(context.Background(),
		&factorpb.PreviewFactorReq{SetId: "fset_a", Spec: "outputs:\n  m: close\n"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INNER_ERR, disabled.GetRetInfo().GetCode())
}
