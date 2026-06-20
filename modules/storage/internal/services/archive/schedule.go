package archive

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"sync"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"trpc.group/trpc-go/trpc-go/log"
)

var defaultService struct {
	sync.RWMutex
	value *Service
}

func SetDefaultService(service *Service) {
	defaultService.Lock()
	defer defaultService.Unlock()
	defaultService.value = service
}

func HandleSchedule(ctx context.Context, params string) error {
	service := currentDefaultService()
	if service == nil {
		return errors.New("archive service is not initialized")
	}
	req, err := parseScheduleParams(params)
	if err != nil {
		log.ErrorContextf(ctx, "[Archive] schedule params invalid: %v", err)
		return err
	}
	if req.allDataSets {
		files, err := service.ArchiveDataSets(ctx, req.spaceID, req.partitionKey, req.timeRange)
		if err != nil {
			log.ErrorContextf(ctx, "[Archive] schedule all datasets failed: %v", err)
			return err
		}
		log.InfoContextf(ctx, "[Archive] schedule archived %d dataset(s) in %s", len(files), req.spaceID)
		return nil
	}
	file, err := service.ArchiveDataSet(ctx, req.spaceID, req.datasetID, req.partitionKey, req.timeRange)
	if err != nil {
		log.ErrorContextf(ctx, "[Archive] schedule failed: %v", err)
		return err
	}
	log.InfoContextf(ctx, "[Archive] schedule archived %s/%s to %s", req.spaceID, req.datasetID, file.GetFileUri())
	return nil
}

func currentDefaultService() *Service {
	defaultService.RLock()
	defer defaultService.RUnlock()
	return defaultService.value
}

type scheduleRequest struct {
	spaceID      string
	datasetID    string
	allDataSets  bool
	partitionKey string
	timeRange    *pb.TimeRange
}

func parseScheduleParams(params string) (scheduleRequest, error) {
	values, err := parseTimerParams(params)
	if err != nil {
		return scheduleRequest{}, err
	}
	req := scheduleRequest{
		spaceID:      strings.TrimSpace(values.Get("space_id")),
		datasetID:    strings.TrimSpace(values.Get("dataset_id")),
		partitionKey: strings.TrimSpace(values.Get("partition_key")),
	}
	req.allDataSets = req.datasetID == "*" || strings.EqualFold(req.datasetID, "all")
	if req.spaceID == "" || req.datasetID == "" {
		return scheduleRequest{}, errors.New("space_id and dataset_id are required")
	}
	req.timeRange, err = parseTimeRange(values)
	if err != nil {
		return scheduleRequest{}, err
	}
	return req, nil
}

func parseTimerParams(params string) (url.Values, error) {
	params = strings.TrimPrefix(strings.TrimSpace(params), "?")
	if strings.Contains(params, ";") {
		params = strings.ReplaceAll(params, ";", "&")
	}
	return url.ParseQuery(params)
}

func parseTimeRange(values url.Values) (*pb.TimeRange, error) {
	start := strings.TrimSpace(values.Get("start_time"))
	end := strings.TrimSpace(values.Get("end_time"))
	if start == "" && end == "" {
		return nil, nil
	}
	return &pb.TimeRange{
		StartTime: start,
		EndTime:   end,
	}, nil
}

func parseBoolDefault(value string, fallback bool) (bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, err
	}
	return parsed, nil
}
