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
	ScheduleTasks(context.Context, *pb.ScheduleTasksReq) (*pb.ScheduleTasksRsp, error)
}

type scheduleParams struct {
	SpaceID string
}

var defaultSchedule = struct {
	sync.RWMutex
	service scheduleService
}{}

// SetDefaultService registers the CollectMgr service used by the timer handler.
func SetDefaultService(service *Service) {
	setDefaultScheduleService(service)
}

func setDefaultScheduleService(service scheduleService) {
	defaultSchedule.Lock()
	defer defaultSchedule.Unlock()
	defaultSchedule.service = service
}

func getDefaultScheduleService() scheduleService {
	defaultSchedule.RLock()
	defer defaultSchedule.RUnlock()
	return defaultSchedule.service
}

// HandleSchedule submits the next collector tasks for the configured space.
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
	rsp, err := service.ScheduleTasks(ctx, &pb.ScheduleTasksReq{SpaceId: params.SpaceID})
	if err != nil {
		return err
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return fmt.Errorf("schedule collector tasks: %s", rsp.GetRetInfo().GetMsg())
	}
	log.InfoContextf(ctx, "[Collector] scheduled tasks space_id=%s", params.SpaceID)
	return nil
}

func parseScheduleParams(raw string) scheduleParams {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return scheduleParams{SpaceID: "crypto"}
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
