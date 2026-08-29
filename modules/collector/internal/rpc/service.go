// Package rpc implements the independent Collector management RPC service.
package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/jobs"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	collectorresample "github.com/mooyang-code/moox/modules/collector/internal/resample"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/report"
	"google.golang.org/protobuf/types/known/structpb"
	"trpc.group/trpc-go/trpc-go/log"
)

// Dependencies contains external service endpoints used by CollectMgr.
type Dependencies struct {
	StorageRPCGatewayTarget string
	// PlannerStorageRPCGatewayTarget is the control-plane's local metadata target.
	// It overrides the runtime Storage target when both are available.
	PlannerStorageRPCGatewayTarget string
	RealtimeInventory              RealtimeInventory
	DefaultResampleSettleDelay     time.Duration
}

// RealtimeInventory reconciles the derived expected Dataset registry.
type RealtimeInventory interface {
	MarkDirty()
	Refresh(context.Context) error
}

// Service implements the independent CollectMgr RPC service.
type Service struct {
	pb.UnimplementedCollectMgr
	ruleRepo                   *store.TaskRuleRepository
	instanceRepo               *store.TaskInstanceRepository
	datasetSrc                 datasetSource
	inventory                  RealtimeInventory
	defaultResampleSettleDelay time.Duration
}

const defaultResampleSettleDelay = 10 * time.Second

type datasetSource interface {
	GetDataset(context.Context, string, string) (storagesource.DatasetInfo, error)
	ListSubjects(context.Context, string, string, string) ([]domain.DatasetSubject, error)
}

// New creates a collector management service.
func New(persistence *store.Store, deps Dependencies) *Service {
	plannerMetadataTarget := deps.PlannerStorageRPCGatewayTarget
	if strings.TrimSpace(plannerMetadataTarget) == "" {
		plannerMetadataTarget = deps.StorageRPCGatewayTarget
	}
	settleDelay := deps.DefaultResampleSettleDelay
	if settleDelay < 0 {
		settleDelay = defaultResampleSettleDelay
	}
	return &Service{
		ruleRepo:                   persistence.TaskRules(),
		instanceRepo:               persistence.TaskInstances(),
		datasetSrc:                 storagesource.NewDatasetSource(plannerMetadataTarget),
		inventory:                  deps.RealtimeInventory,
		defaultResampleSettleDelay: settleDelay,
	}
}

func retOK() *pb.RetInfo {
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "ok"}
}

func retErr(code pb.ErrorCode, msg string) *pb.RetInfo {
	return &pb.RetInfo{Code: code, Msg: msg}
}

func pageParams(page *pb.Page) (int, int) {
	if page == nil {
		return 1, 50
	}
	return normalizePageParams(int(page.GetPage()), int(page.GetSize()))
}

func normalizePageParams(page int, size int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = 50
	}
	if size > 1000 {
		size = 1000
	}
	return page, size
}

func pageResult(page int, size int, total int64) *pb.PageResult {
	return &pb.PageResult{
		Page:    uint32(page),
		Size:    uint32(size),
		Total:   uint32Total(total),
		HasMore: int64(page*size) < total,
	}
}

func uint32Total(total int64) uint32 {
	if total <= 0 {
		return 0
	}
	max := int64(^uint32(0))
	if total > max {
		return ^uint32(0)
	}
	return uint32(total)
}

// GetTaskRuleList returns rule data from the new collector DB.
func (s *Service) GetTaskRuleList(ctx context.Context, req *pb.GetTaskRuleListReq) (*pb.GetTaskRuleListRsp, error) {
	spaceID := strings.TrimSpace(req.GetSpaceId())
	if spaceID == "" {
		return &pb.GetTaskRuleListRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	page, size := pageParams(req.GetPage())
	rules, total, err := s.ruleRepo.List(ctx, store.TaskRuleFilter{
		SpaceID:    spaceID,
		DataType:   req.GetDataType(),
		Provider:   req.GetProvider(),
		MarketType: req.GetMarketType(),
		Enabled:    req.Enabled,
		RuleID:     req.GetRuleId(),
		Page:       page,
		PageSize:   size,
	})
	if err != nil {
		log.ErrorContextf(ctx, "[Collector] list task rules failed: %v", err)
		return &pb.GetTaskRuleListRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	out := make([]*pb.TaskRule, 0, len(rules))
	for _, rule := range rules {
		out = append(out, toPBRule(rule))
	}
	return &pb.GetTaskRuleListRsp{RetInfo: retOK(), Rules: out, Page: pageResult(page, size, total)}, nil
}

// GetTaskRuleDetail returns a single collector rule.
func (s *Service) GetTaskRuleDetail(ctx context.Context, req *pb.GetTaskRuleDetailReq) (*pb.GetTaskRuleDetailRsp, error) {
	if strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.GetTaskRuleDetailRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	if strings.TrimSpace(req.GetRuleId()) == "" {
		return &pb.GetTaskRuleDetailRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "rule_id is required")}, nil
	}
	rule, err := s.ruleRepo.GetByRuleID(ctx, req.GetSpaceId(), req.GetRuleId())
	if err != nil {
		return &pb.GetTaskRuleDetailRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, err.Error())}, nil
	}
	return &pb.GetTaskRuleDetailRsp{RetInfo: retOK(), Rule: toPBRule(*rule)}, nil
}

// CreateTaskRule creates a collector rule through the independent collector service.
func (s *Service) CreateTaskRule(ctx context.Context, req *pb.CreateTaskRuleReq) (*pb.CreateTaskRuleRsp, error) {
	rule := normalizeTaskRule(fromPBRule(req.GetRule()))
	if err := validateTaskRule(rule); err != nil {
		return &pb.CreateTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	var err error
	rule, err = canonicalizeTaskRule(rule)
	if err != nil {
		return &pb.CreateTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if err := s.validateTaskRuleDatasets(ctx, rule); err != nil {
		return &pb.CreateTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if err := s.ruleRepo.Create(ctx, rule); err != nil {
		log.ErrorContextf(ctx, "[Collector] create task rule failed: %v", err)
		return &pb.CreateTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	s.refreshRealtimeInventory(ctx)
	return &pb.CreateTaskRuleRsp{RetInfo: retOK(), RuleId: rule.RuleID}, nil
}

// UpdateTaskRule updates a collector rule through the independent collector service.
func (s *Service) UpdateTaskRule(ctx context.Context, req *pb.UpdateTaskRuleReq) (*pb.UpdateTaskRuleRsp, error) {
	spaceID := strings.TrimSpace(req.GetSpaceId())
	ruleID := strings.TrimSpace(req.GetRuleId())
	rule := normalizeTaskRule(fromPBRule(req.GetRule()))
	if spaceID == "" {
		spaceID = strings.TrimSpace(rule.SpaceID)
	}
	if spaceID == "" {
		return &pb.UpdateTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	if ruleID == "" {
		ruleID = strings.TrimSpace(rule.RuleID)
	}
	if ruleID == "" {
		return &pb.UpdateTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "rule_id is required")}, nil
	}
	if rule.RuleID == "" {
		rule.RuleID = ruleID
	}
	rule.SpaceID = spaceID
	if err := validateTaskRule(rule); err != nil {
		return &pb.UpdateTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	existing, err := s.ruleRepo.GetByRuleID(ctx, spaceID, ruleID)
	if err != nil {
		return &pb.UpdateTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, err.Error())}, nil
	}
	if err := validateTaskRuleUpdate(*existing, rule); err != nil {
		return &pb.UpdateTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	preserveTaskRuleCoverageStart(*existing, &rule)
	rule, err = canonicalizeTaskRule(rule)
	if err != nil {
		return &pb.UpdateTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if strings.EqualFold(existing.DataType, "kline_resample") {
		rule.Creator = existing.Creator
		// Re-running preparation on an enabled update also recovers a transient
		// Metadata/Storage error without requiring a second operator-only API.
		if rule.Enabled {
			rule.PrepareState = domain.PrepareStatePending
			rule.LastError = ""
		} else {
			rule.PrepareState = existing.PrepareState
			rule.LastError = existing.LastError
		}
	}
	if err := s.validateTaskRuleDatasets(ctx, rule); err != nil {
		return &pb.UpdateTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	updated, err := s.ruleRepo.UpdateByRuleID(ctx, spaceID, ruleID, rule)
	if err != nil {
		log.ErrorContextf(ctx, "[Collector] update task rule failed: %v", err)
		return &pb.UpdateTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	s.refreshRealtimeInventory(ctx)
	return &pb.UpdateTaskRuleRsp{RetInfo: retOK(), Rule: toPBRule(*updated)}, nil
}

// DisableTaskRule disables a collector rule without deleting runtime history.
func (s *Service) DisableTaskRule(ctx context.Context, req *pb.DisableTaskRuleReq) (*pb.DisableTaskRuleRsp, error) {
	if strings.TrimSpace(req.GetSpaceId()) == "" {
		return &pb.DisableTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	if strings.TrimSpace(req.GetRuleId()) == "" {
		return &pb.DisableTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "rule_id is required")}, nil
	}
	if err := s.ruleRepo.SetEnabled(ctx, req.GetSpaceId(), req.GetRuleId(), false); err != nil {
		log.ErrorContextf(ctx, "[Collector] disable task rule failed: %v", err)
		return &pb.DisableTaskRuleRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	s.refreshRealtimeInventory(ctx)
	return &pb.DisableTaskRuleRsp{RetInfo: retOK()}, nil
}

func (s *Service) refreshRealtimeInventory(ctx context.Context) {
	if s.inventory == nil {
		return
	}
	s.inventory.MarkDirty()
	if err := s.inventory.Refresh(ctx); err != nil {
		log.WarnContextf(ctx, "[Collector] refresh realtime dataset inventory failed: %v", err)
	}
}

// GetTaskInstanceList returns task instances from the new collector DB.
func (s *Service) GetTaskInstanceList(ctx context.Context, req *pb.GetTaskInstanceListReq) (*pb.GetTaskInstanceListRsp, error) {
	filter := req.GetFilter()
	if filter == nil || strings.TrimSpace(filter.GetSpaceId()) == "" {
		return &pb.GetTaskInstanceListRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id is required")}, nil
	}
	spaceID := strings.TrimSpace(filter.GetSpaceId())
	repoFilter := store.TaskInstanceFilter{Page: 1, PageSize: 50}
	page, size := 1, 50
	if filter != nil {
		page, size = pageParams(filter.GetPage())
		repoFilter.SpaceID = spaceID
		repoFilter.TaskID = filter.GetTaskId()
		repoFilter.RuleID = filter.GetRuleId()
		repoFilter.Provider = filter.GetProvider()
		repoFilter.MarketType = filter.GetMarketType()
		repoFilter.DataType = filter.GetDataType()
		repoFilter.DatasetID = filter.GetDatasetId()
		repoFilter.SubjectID = filter.GetSubjectId()
		repoFilter.Frequency = filter.GetFrequency()
		repoFilter.FunctionName = filter.GetFunctionName()
		repoFilter.LastExecNode = filter.GetLastExecNode()
		repoFilter.IncludeDeleted = filter.GetIncludeDeleted()
		repoFilter.Page = page
		repoFilter.PageSize = size
		if filter.GetLastExecStatus() != pb.TaskInstanceStatus_TASK_INSTANCE_STATUS_UNSPECIFIED {
			status := fromPBStatus(filter.GetLastExecStatus())
			repoFilter.LastExecStatus = &status
		}
	}
	instances, total, err := s.instanceRepo.List(ctx, repoFilter)
	if err != nil {
		log.ErrorContextf(ctx, "[Collector] list task instances failed: %v", err)
		return &pb.GetTaskInstanceListRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	out := make([]*pb.TaskInstance, 0, len(instances))
	for _, instance := range instances {
		out = append(out, toPBInstance(instance))
	}
	return &pb.GetTaskInstanceListRsp{RetInfo: retOK(), Instances: out, Page: pageResult(page, size, total)}, nil
}

// GetDataTypeConfigs returns the currently supported collector rule data types.
func (s *Service) GetDataTypeConfigs(ctx context.Context, req *pb.GetDataTypeConfigsReq) (*pb.GetDataTypeConfigsRsp, error) {
	jobDefinitions := jobs.ListJobDefinitions()
	configs := make([]*pb.DataTypeConfig, 0, len(jobDefinitions))
	for _, definition := range jobDefinitions {
		configs = append(configs, dataTypeConfigFromDefinition(definition))
	}
	return &pb.GetDataTypeConfigsRsp{
		RetInfo: retOK(),
		Configs: configs,
	}, nil
}

// GetDataTypeConfigWithFields returns field metadata for the rule form.
func (s *Service) GetDataTypeConfigWithFields(ctx context.Context, req *pb.GetDataTypeConfigWithFieldsReq) (*pb.GetDataTypeConfigWithFieldsRsp, error) {
	definition, ok := jobs.JobDefinitionByDataType(req.GetDataType())
	if !ok {
		return &pb.GetDataTypeConfigWithFieldsRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "unsupported data_type")}, nil
	}
	return &pb.GetDataTypeConfigWithFieldsRsp{
		RetInfo: retOK(),
		Detail: &pb.DataTypeConfigDetail{
			Config: dataTypeConfigFromDefinition(definition),
			Fields: dataTypeFieldsFromDefinition(definition),
		},
	}, nil
}

func (s *Service) StartKlineResampleBackfill(ctx context.Context, req *pb.StartKlineResampleBackfillReq) (*pb.StartKlineResampleBackfillRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetRuleId()) == "" || strings.TrimSpace(req.GetRequestId()) == "" {
		return &pb.StartKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id, rule_id and request_id are required")}, nil
	}
	start, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(req.GetStart()))
	if err != nil {
		return &pb.StartKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "start must be RFC3339")}, nil
	}
	end, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(req.GetEnd()))
	if err != nil {
		return &pb.StartKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "end must be RFC3339")}, nil
	}
	rule, err := s.ruleRepo.GetByRuleID(ctx, req.GetSpaceId(), req.GetRuleId())
	if err != nil {
		return &pb.StartKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, err.Error())}, nil
	}
	if !strings.EqualFold(rule.DataType, "kline_resample") {
		return &pb.StartKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "rule is not kline_resample")}, nil
	}
	if !rule.Enabled {
		return &pb.StartKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "rule is disabled")}, nil
	}
	if rule.PrepareState != domain.PrepareStateReady {
		return &pb.StartKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, fmt.Sprintf("rule is not ready (prepare_state=%s)", rule.PrepareState))}, nil
	}
	params, err := domain.ParseCollectParams(rule.CollectParams, rule.Provider, rule.MarketType, rule.DataType)
	if err != nil {
		return &pb.StartKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	target, err := collectorresample.ParseFixedFrequency(params.TargetFrequency)
	if err != nil {
		return &pb.StartKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	backfill := domain.ResampleBackfillRequest{RequestID: req.GetRequestId(), Start: start.UTC(), End: end.UTC()}
	sourceKeepDuration := ""
	if s.datasetSrc != nil {
		source, sourceErr := s.datasetSrc.GetDataset(ctx, req.GetSpaceId(), params.SourceDatasetID)
		if sourceErr != nil {
			return &pb.StartKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, sourceErr.Error())}, nil
		}
		sourceKeepDuration = source.KeepDuration
	}
	settleDelay := s.defaultResampleSettleDelay
	if settleDelay < 0 {
		settleDelay = defaultResampleSettleDelay
	}
	if err := validateResampleBackfillWindow(backfill, target.Duration, params.SettleDelayOr(settleDelay), sourceKeepDuration, time.Now().UTC()); err != nil {
		return &pb.StartKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if _, err := s.instanceRepo.StartResampleBackfill(ctx, req.GetSpaceId(), req.GetRuleId(), backfill); err != nil {
		return &pb.StartKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	return &pb.StartKlineResampleBackfillRsp{RetInfo: retOK()}, nil
}

func validateResampleBackfillWindow(request domain.ResampleBackfillRequest, target time.Duration, settleDelay time.Duration, sourceKeepDuration string, now time.Time) error {
	if err := request.ValidateForFrequency(target); err != nil {
		return err
	}
	now = now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if request.End.Add(settleDelay).After(now) {
		return fmt.Errorf("backfill end must be a closed bucket after settle delay")
	}
	if raw := strings.TrimSpace(sourceKeepDuration); raw != "" && raw != "0" {
		keep, err := time.ParseDuration(raw)
		if err != nil || keep <= 0 {
			return fmt.Errorf("source Dataset keep_duration %q is invalid", sourceKeepDuration)
		}
		if request.Start.Before(now.Add(-keep)) {
			return fmt.Errorf("backfill start is older than source Dataset retention %s", raw)
		}
	}
	return nil
}

func (s *Service) CancelKlineResampleBackfill(ctx context.Context, req *pb.CancelKlineResampleBackfillReq) (*pb.CancelKlineResampleBackfillRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetRuleId()) == "" || strings.TrimSpace(req.GetRequestId()) == "" {
		return &pb.CancelKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id, rule_id and request_id are required")}, nil
	}
	if _, err := s.instanceRepo.CancelResampleBackfill(ctx, req.GetSpaceId(), req.GetRuleId(), req.GetRequestId()); err != nil {
		return &pb.CancelKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	return &pb.CancelKlineResampleBackfillRsp{RetInfo: retOK()}, nil
}

// GetKlineResampleBackfill returns one durable, server-side aggregate instead
// of forcing callers to scan an unbounded TaskInstance history.
func (s *Service) GetKlineResampleBackfill(ctx context.Context, req *pb.GetKlineResampleBackfillReq) (*pb.GetKlineResampleBackfillRsp, error) {
	if req == nil || strings.TrimSpace(req.GetSpaceId()) == "" || strings.TrimSpace(req.GetRuleId()) == "" {
		return &pb.GetKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "space_id and rule_id are required")}, nil
	}
	instances := make([]domain.TaskInstance, 0)
	for page := 1; ; page++ {
		rows, total, err := s.instanceRepo.List(ctx, store.TaskInstanceFilter{SpaceID: req.GetSpaceId(), RuleID: req.GetRuleId(), DataType: "kline_resample", Page: page, PageSize: 1000})
		if err != nil {
			return &pb.GetKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
		}
		instances = append(instances, rows...)
		if len(rows) == 0 || int64(len(instances)) >= total {
			break
		}
	}
	requestID := strings.TrimSpace(req.GetRequestId())
	if requestID == "" {
		for _, instance := range instances {
			result, err := domain.ParseResampleTaskResult(instance.Result)
			if err == nil && result.Backfill != nil && result.Backfill.RequestID != "" {
				requestID = result.Backfill.RequestID
				break
			}
		}
	}
	if requestID == "" {
		return &pb.GetKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "backfill request not found")}, nil
	}
	response := &pb.GetKlineResampleBackfillRsp{RetInfo: retOK(), RequestId: requestID}
	for _, instance := range instances {
		result, err := domain.ParseResampleTaskResult(instance.Result)
		if err != nil || result.Backfill == nil || result.Backfill.RequestID != requestID {
			continue
		}
		backfill := result.Backfill
		response.Participants++
		if response.Start == "" {
			response.Start = backfill.Start.UTC().Format(time.RFC3339Nano)
			response.End = backfill.End.UTC().Format(time.RFC3339Nano)
		}
		next := backfill.NextBucket.UTC().Format(time.RFC3339Nano)
		if response.NextBucket == "" || next < response.NextBucket {
			response.NextBucket = next
		}
		switch backfill.State {
		case domain.ResampleBackfillRunning:
			response.Running++
		case domain.ResampleBackfillWaitingSource:
			response.WaitingSource++
		case domain.ResampleBackfillSyncing:
			response.Syncing++
		case domain.ResampleBackfillComplete:
			response.Complete++
		case domain.ResampleBackfillFailed:
			response.Failed++
		case domain.ResampleBackfillCanceled:
			response.Canceled++
		}
		if len(response.Errors) < 20 && strings.TrimSpace(result.LastError) != "" {
			response.Errors = append(response.Errors, result.LastError)
		}
	}
	if response.Participants == 0 {
		return &pb.GetKlineResampleBackfillRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "backfill request not found")}, nil
	}
	// An active participant must remain visible even when another participant
	// has already failed. This lets the operator cancel the remaining work and
	// retry the request instead of seeing a terminal state that is not actually
	// terminal for the whole request.
	if response.Syncing > 0 {
		response.State = string(domain.ResampleBackfillSyncing)
	} else if response.WaitingSource > 0 {
		response.State = string(domain.ResampleBackfillWaitingSource)
	} else if response.Running > 0 {
		response.State = string(domain.ResampleBackfillRunning)
	} else if response.Failed == response.Participants {
		response.State = string(domain.ResampleBackfillFailed)
	} else if response.Canceled == response.Participants {
		response.State = string(domain.ResampleBackfillCanceled)
	} else if response.Complete == response.Participants {
		response.State = string(domain.ResampleBackfillComplete)
	} else if response.Failed > 0 {
		response.State = string(domain.ResampleBackfillFailed)
	} else if response.Canceled > 0 {
		response.State = string(domain.ResampleBackfillCanceled)
	} else {
		response.State = string(domain.ResampleBackfillRunning)
	}
	return response, nil
}

func normalizeTaskRule(rule domain.TaskRule) domain.TaskRule {
	rule.RuleID = strings.TrimSpace(rule.RuleID)
	if rule.RuleID == "" {
		rule.RuleID = fmt.Sprintf("rule-%d", time.Now().UnixNano())
	}
	if strings.TrimSpace(rule.CollectParams) == "" {
		rule.CollectParams = "{}"
	}
	return rule
}

func validateTaskRule(rule domain.TaskRule) error {
	if strings.TrimSpace(rule.RuleID) == "" {
		return fmt.Errorf("rule_id is required")
	}
	if strings.TrimSpace(rule.SpaceID) == "" {
		return fmt.Errorf("space_id is required")
	}
	if strings.TrimSpace(rule.DataType) == "" {
		return fmt.Errorf("data_type is required")
	}
	if strings.TrimSpace(rule.Provider) == "" {
		return fmt.Errorf("provider is required")
	}
	params, err := domain.ParseCollectParams(rule.CollectParams, rule.Provider, rule.MarketType, rule.DataType)
	if err != nil {
		return fmt.Errorf("invalid collect_params: %w", err)
	}
	if err := params.Validate(); err != nil {
		return fmt.Errorf("invalid collect_params: %w", err)
	}
	if strings.EqualFold(strings.TrimSpace(params.Provider), "stock_cn_multi") && params.HistoryPolicy.Mode != domain.HistoryModeLiveOnly {
		return fmt.Errorf("stock_cn kline collector only supports live_only history mode")
	}
	definition, ok := jobs.JobDefinitionByDataType(params.Collector.DataType)
	if !ok || !definition.ExecutionMode.Valid() || !definition.Matches(params) {
		return fmt.Errorf(
			"unsupported collector: exchange=%s market=%s data_type=%s source_kind=%s",
			params.Collector.Exchange,
			params.Collector.Market,
			params.Collector.DataType,
			params.Source.Kind,
		)
	}
	if definition.ExecutionMode == jobs.ExecutionModeCloudInvoke {
		if _, routeOK := jobs.JobRouteFor(params.Collector.Exchange, params.Collector.DataType); !routeOK {
			return fmt.Errorf("cloud collector route not found: exchange=%s data_type=%s", params.Collector.Exchange, params.Collector.DataType)
		}
	}
	if !strings.EqualFold(rule.Provider, params.Provider) ||
		!strings.EqualFold(rule.MarketType, params.MarketType) ||
		!strings.EqualFold(rule.DataType, params.Collector.DataType) {
		return fmt.Errorf("rule identity does not match collect_params")
	}
	return nil
}

func preserveTaskRuleCoverageStart(existing domain.TaskRule, desired *domain.TaskRule) {
	if desired == nil {
		return
	}
	if existing.CoverageStartTime == nil || existing.CoverageStartTime.IsZero() {
		desired.CoverageStartTime = nil
		return
	}
	at := existing.CoverageStartTime.UTC()
	desired.CoverageStartTime = &at
}

func canonicalizeTaskRule(rule domain.TaskRule) (domain.TaskRule, error) {
	if !strings.EqualFold(strings.TrimSpace(rule.DataType), "kline_resample") {
		if rule.PrepareState == "" {
			rule.PrepareState = domain.PrepareStateReady
		}
		return rule, nil
	}
	params, err := domain.ParseCollectParams(rule.CollectParams, rule.Provider, rule.MarketType, rule.DataType)
	if err != nil {
		return rule, err
	}
	canonical, err := params.CanonicalJSON()
	if err != nil {
		return rule, err
	}
	rule.CollectParams = canonical
	if rule.PrepareState == "" || rule.PrepareState == domain.PrepareStateReady {
		rule.PrepareState = domain.PrepareStatePending
	}
	rule.LastError = ""
	return rule, nil
}

func validateTaskRuleUpdate(existing, desired domain.TaskRule) error {
	if !strings.EqualFold(strings.TrimSpace(existing.DataType), "kline_resample") && !strings.EqualFold(strings.TrimSpace(desired.DataType), "kline_resample") {
		return nil
	}
	if existing.SpaceID != desired.SpaceID || existing.RuleID != desired.RuleID || !strings.EqualFold(existing.DataType, desired.DataType) {
		return fmt.Errorf("resample rule identity cannot change; create a new rule and target Dataset")
	}
	existingParams, err := domain.ParseCollectParams(existing.CollectParams, existing.Provider, existing.MarketType, existing.DataType)
	if err != nil {
		return fmt.Errorf("parse existing resample rule: %w", err)
	}
	desiredParams, err := domain.ParseCollectParams(desired.CollectParams, desired.Provider, desired.MarketType, desired.DataType)
	if err != nil {
		return fmt.Errorf("parse desired resample rule: %w", err)
	}
	if err := domain.ValidateSameResampleIdentity(existingParams, desiredParams); err != nil {
		return fmt.Errorf("%w; create a new rule and target Dataset", err)
	}
	return nil
}

func (s *Service) validateTaskRuleDatasets(ctx context.Context, rule domain.TaskRule) error {
	params, err := domain.ParseCollectParams(rule.CollectParams, rule.Provider, rule.MarketType, rule.DataType)
	if err != nil {
		return err
	}
	switch params.Collector.DataType {
	case "symbol":
		return s.validateDataset(
			ctx,
			rule.SpaceID,
			params.Target.DatasetID,
			params.Collector.Exchange,
			storagepb.DataKind_DATA_KIND_RECORD,
			"target",
			false,
			params.MarketType,
			nil,
		)
	case "kline":
		if err := s.validateDataset(
			ctx,
			rule.SpaceID,
			params.Source.DatasetID,
			params.Collector.Exchange,
			storagepb.DataKind_DATA_KIND_RECORD,
			"source",
			false,
			params.Collector.Market,
			nil,
		); err != nil {
			return err
		}
		return s.validateDataset(
			ctx,
			rule.SpaceID,
			params.Target.DatasetID,
			params.Collector.Exchange,
			storagepb.DataKind_DATA_KIND_TIME_SERIES,
			"target",
			true,
			params.Collector.Market,
			params.Collector.Intervals,
		)
	case "kline_resample":
		return s.validateResampleSourceDataset(ctx, rule, params)
	default:
		return fmt.Errorf("unsupported collector data_type: %s", params.Collector.DataType)
	}
}

func (s *Service) validateResampleSourceDataset(ctx context.Context, rule domain.TaskRule, params *domain.CollectParams) error {
	info, err := s.datasetSrc.GetDataset(ctx, rule.SpaceID, params.SourceDatasetID)
	if err != nil {
		return fmt.Errorf("source Dataset %s is unavailable: %w", params.SourceDatasetID, err)
	}
	if info.Status != "active" {
		return fmt.Errorf("source Dataset %s must be active", params.SourceDatasetID)
	}
	if info.DataKind != storagepb.DataKind_DATA_KIND_TIME_SERIES {
		return fmt.Errorf("source Dataset %s must be time_series", params.SourceDatasetID)
	}
	if strings.EqualFold(strings.TrimSpace(info.Attributes["dataset_role"]), "kline_resample_result") {
		return fmt.Errorf("source Dataset %s cannot be another resample result", params.SourceDatasetID)
	}
	actualMarket := strings.ToLower(strings.TrimSpace(info.Attributes["market_type"]))
	if actualMarket == "" || actualMarket != strings.ToLower(strings.TrimSpace(rule.MarketType)) {
		return fmt.Errorf("source Dataset %s market_type=%s does not match rule market_type=%s", params.SourceDatasetID, actualMarket, rule.MarketType)
	}
	wantedFrequency, normalizeErr := report.NormalizeDatasetFrequency(strings.TrimSpace(params.SourceFrequency))
	if normalizeErr != nil {
		return fmt.Errorf("source Dataset %s frequency %q is invalid: %w", params.SourceDatasetID, params.SourceFrequency, normalizeErr)
	}
	for _, frequency := range info.Freqs {
		actualFrequency, frequencyErr := report.NormalizeDatasetFrequency(strings.TrimSpace(frequency))
		if frequencyErr == nil && actualFrequency == wantedFrequency {
			// Dataset metadata adapters that expose column discovery return a
			// non-nil ColumnTypes map. Validate both presence and logical type so
			// an empty or malformed schema cannot enter ready and spin forever.
			if info.ColumnTypes != nil {
				expected := map[string]storagepb.FieldValueType{
					"open": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "high": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
					"low": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "close": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
					"volume": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "quote_volume": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
					"trade_num": storagepb.FieldValueType_FIELD_VALUE_TYPE_INT,
				}
				missing := make([]string, 0)
				for column, expectedType := range expected {
					actualType, ok := info.ColumnTypes[column]
					if !ok {
						missing = append(missing, column)
						continue
					}
					if actualType != expectedType {
						return fmt.Errorf("source Dataset %s column %s has value_type=%s, want %s", params.SourceDatasetID, column, actualType.String(), expectedType.String())
					}
				}
				if len(missing) != 0 {
					sort.Strings(missing)
					return fmt.Errorf("source Dataset %s missing active K-line columns: %s", params.SourceDatasetID, strings.Join(missing, ","))
				}
			}
			return nil
		}
	}
	return fmt.Errorf("source Dataset %s does not enable frequency %q", params.SourceDatasetID, params.SourceFrequency)
}

func (s *Service) validateDataset(
	ctx context.Context,
	spaceID string,
	datasetID string,
	exchange string,
	expectedKind storagepb.DataKind,
	role string,
	allowSharedMarket bool,
	marketType string,
	requiredFreqs []string,
) error {
	info, err := s.datasetSrc.GetDataset(ctx, spaceID, datasetID)
	if err != nil {
		return fmt.Errorf("%s Dataset %s is unavailable: %w", role, datasetID, err)
	}
	if info.Status != "active" {
		return fmt.Errorf("%s Dataset %s must be active", role, datasetID)
	}
	sourceMatches := strings.EqualFold(info.DataSourceID, exchange) ||
		(allowSharedMarket && strings.EqualFold(info.DataSourceID, "crypto_market"))
	if !sourceMatches {
		return fmt.Errorf(
			"%s Dataset %s data_source_id=%s does not match collector exchange=%s",
			role,
			datasetID,
			info.DataSourceID,
			exchange,
		)
	}
	if info.DataKind != expectedKind {
		return fmt.Errorf(
			"%s Dataset %s data_kind=%s does not match collector data_type",
			role,
			datasetID,
			info.DataKind.String(),
		)
	}
	if marketType = strings.ToLower(strings.TrimSpace(marketType)); marketType != "" {
		actual := strings.ToLower(strings.TrimSpace(info.Attributes["market_type"]))
		if actual == "" {
			return fmt.Errorf("%s Dataset %s must declare attributes.market_type", role, datasetID)
		}
		if actual != marketType {
			return fmt.Errorf("%s Dataset %s market_type=%s does not match rule market_type=%s", role, datasetID, actual, marketType)
		}
	}
	if len(requiredFreqs) > 0 {
		available := make(map[string]struct{}, len(info.Freqs))
		for _, value := range info.Freqs {
			available[strings.ToLower(strings.TrimSpace(value))] = struct{}{}
		}
		for _, value := range requiredFreqs {
			if _, ok := available[strings.ToLower(strings.TrimSpace(value))]; !ok {
				return fmt.Errorf("%s Dataset %s does not enable frequency %q", role, datasetID, value)
			}
		}
	}
	return nil
}

func dataTypeConfigFromDefinition(definition jobs.JobDefinition) *pb.DataTypeConfig {
	return &pb.DataTypeConfig{
		Id:                definition.ID,
		DataType:          definition.DataType,
		TypeName:          definition.TypeName,
		TypeDesc:          definition.TypeDesc,
		DataSourceOptions: structFromAny(definition.DataSourceOptions),
		SortOrder:         definition.SortOrder,
		Version:           definition.Version,
	}
}

func dataTypeFieldsFromDefinition(definition jobs.JobDefinition) []*pb.DataTypeFieldConfig {
	fields := make([]*pb.DataTypeFieldConfig, 0, len(definition.Fields))
	for _, field := range definition.Fields {
		fields = append(fields, &pb.DataTypeFieldConfig{
			Id:                field.ID,
			DataType:          field.DataType,
			FieldKey:          field.FieldKey,
			FieldName:         field.FieldName,
			FieldType:         field.FieldType,
			IsRequired:        field.IsRequired,
			DefaultValue:      valueFromAny(field.DefaultValue),
			FieldOptions:      structFromJSONString(field.FieldOptionsJSON),
			DataSourceOptions: structFromAny(field.DataSourceOptions),
			SortOrder:         field.SortOrder,
		})
	}
	return fields
}

func structFromAny(value any) *structpb.Struct {
	raw, err := json.Marshal(value)
	if err != nil {
		return &structpb.Struct{Fields: map[string]*structpb.Value{}}
	}
	return structFromJSONString(string(raw))
}

func valueFromAny(value any) *structpb.Value {
	out, err := structpb.NewValue(value)
	if err != nil {
		return structpb.NewStringValue("")
	}
	return out
}
