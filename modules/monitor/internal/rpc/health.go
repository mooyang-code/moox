package rpc

import (
	"context"
	"errors"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
)

func (s *Service) GetHealthOverview(ctx context.Context, req *monitorpb.GetHealthOverviewReq) (*monitorpb.GetHealthOverviewRsp, error) {
	if s.healthView == nil {
		return &monitorpb.GetHealthOverviewRsp{RetInfo: inner(errors.New("health overview is unavailable"))}, nil
	}
	overview, err := s.healthView.Build(ctx, req.GetSpaceId())
	if err != nil {
		return &monitorpb.GetHealthOverviewRsp{RetInfo: inner(err)}, nil
	}
	return &monitorpb.GetHealthOverviewRsp{RetInfo: success(), Overview: overview}, nil
}
