package rpc

import (
	"reflect"
	"strings"
	"testing"

	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"
)

func strategyProtoMessages() []proto.Message {
	return []proto.Message{
		&strategypb.TargetWeight{},
		&strategypb.StrategyDef{StrategyId: "s1", Version: "v1", ApiVersion: "api", ManifestYaml: "m", SourceHash: "h", Status: "enabled"},
		&strategypb.StrategyBinding{BindingId: "b1", StrategyId: "s1", StrategyVersion: "v1", SpaceId: "space", Status: "enabled"},
		&strategypb.StrategyState{BindingId: "b1", Revision: 1, StateJson: "{}"},
		&strategypb.StrategyRun{RunId: "r1", BindingId: "b1", Status: "ok", Action: "hold"},
		&strategypb.CreateStrategyReq{},
		&strategypb.CreateStrategyRsp{},
		&strategypb.RunOnceReq{BindingId: "b1", TriggerBarTime: "t", DataJson: "[]", DataRevision: "rev"},
		&strategypb.RunOnceRsp{},
		&strategypb.GetEngineStatusReq{},
		&strategypb.GetEngineStatusRsp{Workers: 1, ReadyWorkers: 1},
		&strategypb.PageReq{Page: 1, PageSize: 20},
		&strategypb.TimeRange{From: "a", To: "b"},
		&strategypb.ListRunningStrategiesReq{SpaceId: "space"},
		&strategypb.StrategyHealth{Status: "ok", Mode: "observe"},
		&strategypb.RunningStrategySummary{BindingId: "b1"},
		&strategypb.ListRunningStrategiesRsp{Total: 1},
		&strategypb.GetStrategyOverviewReq{BindingId: "b1"},
		&strategypb.GetStrategyOverviewRsp{},
		&strategypb.ListStrategyRunsReq{BindingId: "b1"},
		&strategypb.ListStrategyRunsRsp{},
		&strategypb.GetStrategyRunReq{RunId: "r1"},
		&strategypb.GetStrategyRunRsp{},
		&strategypb.ListStrategyTargetsReq{RunId: "r1"},
		&strategypb.ListStrategyTargetsRsp{},
		&strategypb.GetStrategyStateSummaryReq{BindingId: "b1"},
		&strategypb.GetStrategyStateSummaryRsp{},
		&strategypb.GetStrategyHealthReq{BindingId: "b1"},
		&strategypb.GetStrategyHealthRsp{},
		&strategypb.PerformancePoint{PointTime: "t", Nav: "1"},
		&strategypb.PerformanceSummary{Status: "ok"},
		&strategypb.GetStrategyPerformanceReq{BindingId: "b1", PerformanceSource: "paper"},
		&strategypb.GetStrategyPerformanceRsp{},
		&strategypb.BindingOperationReq{BindingId: "b1", OperationId: "op", Reason: "r"},
		&strategypb.SetExecutionModeReq{BindingId: "b1", Mode: "paper", OperationId: "op"},
		&strategypb.BindingOperationRsp{OperationId: "op", Status: "ok"},
	}
}

func callProtoGetters(t *testing.T, msg proto.Message) {
	t.Helper()
	rv := reflect.ValueOf(msg)
	rt := rv.Type()
	for i := 0; i < rt.NumMethod(); i++ {
		method := rt.Method(i)
		if !strings.HasPrefix(method.Name, "Get") || method.Type.NumIn() != 1 || method.Type.NumOut() < 1 {
			continue
		}
		method.Func.Call([]reflect.Value{rv})
	}
	desc, idx := msg.(interface {
		Descriptor() ([]byte, []int)
	}).Descriptor()
	assert.NotEmpty(t, desc)
	assert.NotEmpty(t, idx)
	_ = msg.ProtoReflect()
}

func TestStrategyProtoMessages_ShouldExerciseGettersAndReflection(t *testing.T) {
	for _, msg := range strategyProtoMessages() {
		callProtoGetters(t, msg)
		if s, ok := msg.(interface{ String() string }); ok {
			_ = s.String()
		}
		if r, ok := msg.(interface{ Reset() }); ok {
			r.Reset()
		}
	}
}
