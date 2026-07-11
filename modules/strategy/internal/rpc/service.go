package rpc

import (
	"context"
	"errors"
	"fmt"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/registry"
	"github.com/mooyang-code/moox/modules/strategy/internal/repository"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/mooyang-code/moox/packages/commonpb"
)

type Service struct {
	Repo         *repository.Repository
	Registry     *registry.Service
	Workers      int
	ReadyWorkers int
	Engine       *engine.Engine
}

func (s *Service) CreateStrategy(ctx context.Context, req *strategypb.CreateStrategyReq) (*strategypb.CreateStrategyRsp, error) {
	if req == nil || req.GetStrategy() == nil {
		return &strategypb.CreateStrategyRsp{RetInfo: invalid(fmt.Errorf("strategy is required"))}, nil
	}
	p := req.GetStrategy()
	d, err := s.Registry.Publish(ctx, p.GetManifestYaml(), p.GetSourceCode())
	if err != nil {
		return &strategypb.CreateStrategyRsp{RetInfo: invalid(err)}, nil
	}
	return &strategypb.CreateStrategyRsp{RetInfo: success(), Strategy: &strategypb.StrategyDef{StrategyId: d.StrategyID, Version: d.Version, ApiVersion: d.API, ManifestYaml: d.ManifestYAML, SourceCode: d.SourceCode, SourceHash: d.SourceHash, Status: d.Status}}, nil
}
func (s *Service) RunOnce(context.Context, *strategypb.RunOnceReq) (*strategypb.RunOnceRsp, error) {
	return &strategypb.RunOnceRsp{RetInfo: invalid(errors.New("run-once requires a configured StrategyEngine"))}, nil
}
func (s *Service) GetEngineStatus(context.Context, *strategypb.GetEngineStatusReq) (*strategypb.GetEngineStatusRsp, error) {
	return &strategypb.GetEngineStatusRsp{RetInfo: success(), Workers: int32(s.Workers), ReadyWorkers: int32(s.ReadyWorkers)}, nil
}
func success() *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS, Msg: "success"}
}
func invalid(err error) *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM, Msg: err.Error()}
}

var _ = domain.ActionHold
