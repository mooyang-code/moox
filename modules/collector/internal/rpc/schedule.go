package rpc

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"

	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"trpc.group/trpc-go/trpc-go/log"
)

type scheduleService interface {
	RecalculateAllTaskInstances(context.Context, *pb.RecalculateAllTaskInstancesReq) (*pb.RecalculateAllTaskInstancesRsp, error)
}

type scheduleParams struct {
	SpaceID string
}

var defaultSchedule = struct {
	sync.RWMutex
	service scheduleService
}{}
var defaultCoverage = struct {
	sync.RWMutex
	service interface {
		ReconcileMarketCoverage(context.Context, int) (int, error)
	}
}{}

// SetDefaultService registers the CollectMgr service used by the timer handler.
func SetDefaultService(service *Service) {
	setDefaultScheduleService(service)
	defaultCoverage.Lock()
	defaultCoverage.service = service
	defaultCoverage.Unlock()
}

func setDefaultScheduleService(service scheduleService) {
	defaultSchedule.Lock()
	defer defaultSchedule.Unlock()
	defaultSchedule.service = service
}

func HandleCoverageSchedule(ctx context.Context, _ string) error {
	defaultCoverage.RLock()
	service := defaultCoverage.service
	defaultCoverage.RUnlock()
	if service == nil {
		return nil
	}
	_, err := service.ReconcileMarketCoverage(ctx, 100)
	return err
}

func getDefaultScheduleService() scheduleService {
	defaultSchedule.RLock()
	defer defaultSchedule.RUnlock()
	return defaultSchedule.service
}

// HandleSchedule recalculates collector task instances for the configured space.
func HandleSchedule(ctx context.Context, rawParams string) error {
	params := parseScheduleParams(rawParams)
	if params.SpaceID == "" {
		return fmt.Errorf("space_id is required")
	}
	service := getDefaultScheduleService()
	if service == nil {
		log.WarnContextf(ctx, "[Collector] schedule skipped: CollectMgr service is not ready")
		return nil
	}
	rsp, err := service.RecalculateAllTaskInstances(ctx, &pb.RecalculateAllTaskInstancesReq{SpaceId: params.SpaceID})
	if err != nil {
		return err
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return fmt.Errorf("recalculate collector tasks: %s", rsp.GetRetInfo().GetMsg())
	}
	log.InfoContextf(ctx, "[Collector] schedule recalculated task instances space_id=%s", params.SpaceID)
	return nil
}

func parseScheduleParams(raw string) scheduleParams {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return scheduleParams{}
	}
	if !strings.Contains(raw, "=") {
		return scheduleParams{SpaceID: raw}
	}
	normalized := strings.NewReplacer(";", "&", ",", "&").Replace(raw)
	values, err := url.ParseQuery(normalized)
	if err != nil {
		return scheduleParams{}
	}
	return scheduleParams{SpaceID: strings.TrimSpace(values.Get("space_id"))}
}
