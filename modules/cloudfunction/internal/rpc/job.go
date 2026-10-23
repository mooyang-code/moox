package rpc

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/mooyang-code/moox/modules/cloudfunction/internal/spacecontext"
	"github.com/mooyang-code/moox/modules/cloudfunction/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudfunction/proto/cloudfunctiongen"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	functionJobOperationCreate        = "create_instances"
	functionJobOperationDeploy        = "deploy_instances"
	functionJobOperationDelete        = "delete_instances"
	functionJobOperationRuntimeConfig = "update_runtime_configs"
	maxFunctionJobItems               = 100
)

func (s *Service) SubmitCreateFunctionInstances(ctx context.Context, req *pb.SubmitCreateFunctionInstancesReq) (*pb.SubmitFunctionJobRsp, error) {
	spaceID, err := spacecontext.MustFromContext(ctx)
	if err != nil {
		return &pb.SubmitFunctionJobRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	items := req.GetInstances()
	if ret := validateFunctionJobSize(len(items), "instances"); ret != nil {
		return &pb.SubmitFunctionJobRsp{RetInfo: ret}, nil
	}

	jobID := "function-job-" + uuid.NewString()
	creates := make([]store.FunctionJobItemCreate, 0, len(items))
	seenInstances := make(map[string]struct{}, len(items))
	for index, item := range items {
		if item == nil {
			return &pb.SubmitFunctionJobRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "instances item is required")}, nil
		}
		// The control plane owns this nonce; callers cannot reuse another request's
		// reservation identity. It is persisted in the durable request for retries.
		item.CreateReservationId = uuid.NewString()
		instance, ret := s.preflightCreateInstance(ctx, spaceID, item, index)
		if ret != nil {
			return &pb.SubmitFunctionJobRsp{RetInfo: ret}, nil
		}
		if existing, err := s.catalog.GetInstance(ctx, spaceID, instance.InstanceID); err != nil {
			return &pb.SubmitFunctionJobRsp{RetInfo: retFromError(err)}, nil
		} else if existing != nil && !existing.IsDeleted {
			return &pb.SubmitFunctionJobRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "instance already exists: "+instance.InstanceID)}, nil
		}
		if _, exists := seenInstances[instance.InstanceID]; exists {
			return &pb.SubmitFunctionJobRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "instances contains duplicate instance_id")}, nil
		}
		seenInstances[instance.InstanceID] = struct{}{}
		raw, err := protojson.Marshal(item)
		if err != nil {
			return &pb.SubmitFunctionJobRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "invalid instance request")}, nil
		}
		creates = append(creates, store.FunctionJobItemCreate{
			ItemID:      fmt.Sprintf("%s-%03d", jobID, index),
			ItemIndex:   index,
			InstanceID:  instance.InstanceID,
			RequestJSON: string(raw),
		})
	}
	if err := s.catalog.CreateFunctionJob(ctx, store.FunctionJobCreate{
		SpaceID: spaceID, JobID: jobID, Operation: functionJobOperationCreate, Items: creates,
	}); err != nil {
		return &pb.SubmitFunctionJobRsp{RetInfo: retFromError(err)}, nil
	}
	return &pb.SubmitFunctionJobRsp{
		RetInfo: retOK(), JobId: jobID,
		Operation:  pb.FunctionJobOperation_FUNCTION_JOB_OPERATION_CREATE_INSTANCES,
		TotalCount: int32(len(creates)),
	}, nil
}

func (s *Service) SubmitDeployFunctionInstances(ctx context.Context, req *pb.SubmitDeployFunctionInstancesReq) (*pb.SubmitFunctionJobRsp, error) {
	spaceID, err := spacecontext.MustFromContext(ctx)
	if err != nil {
		return &pb.SubmitFunctionJobRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if req == nil {
		return &pb.SubmitFunctionJobRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "request is required")}, nil
	}
	items := req.GetDeployments()
	if ret := validateFunctionJobSize(len(items), "deployments"); ret != nil {
		return &pb.SubmitFunctionJobRsp{RetInfo: ret}, nil
	}
	instanceIDs := make([]string, 0, len(items))
	for _, item := range items {
		if item != nil {
			instanceIDs = append(instanceIDs, strings.TrimSpace(item.GetInstanceId()))
		}
	}
	unlockInstances := lockFunctionInstances(spaceID, instanceIDs)
	defer unlockInstances()

	jobID := "function-job-" + uuid.NewString()
	creates := make([]store.FunctionJobItemCreate, 0, len(items))
	seenInstances := make(map[string]struct{}, len(items))
	for index, item := range items {
		if ret := s.preflightDeployInstance(ctx, spaceID, item); ret != nil {
			return &pb.SubmitFunctionJobRsp{RetInfo: ret}, nil
		}
		instanceID := strings.TrimSpace(item.GetInstanceId())
		if _, exists := seenInstances[instanceID]; exists {
			return &pb.SubmitFunctionJobRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "deployments contains duplicate instance_id")}, nil
		}
		seenInstances[instanceID] = struct{}{}
		instance, err := s.catalog.GetInstance(ctx, spaceID, instanceID)
		if err != nil {
			return &pb.SubmitFunctionJobRsp{RetInfo: retFromError(err)}, nil
		}
		if instance == nil || instance.IsDeleted {
			return &pb.SubmitFunctionJobRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "instance not found: "+instanceID)}, nil
		}
		lifecycleID, ensureErr := s.catalog.InstanceLifecycleID(ctx, spaceID, instanceID)
		if ensureErr != nil {
			return &pb.SubmitFunctionJobRsp{RetInfo: retFromError(ensureErr)}, nil
		}
		if instance.LifecycleID != "" && instance.LifecycleID != lifecycleID {
			return &pb.SubmitFunctionJobRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "instance lifecycle changed while scheduling deploy: "+instanceID)}, nil
		}
		item.LifecycleId = lifecycleID
		item.OperationId = fmt.Sprintf("%s-%03d", jobID, index)
		raw, err := protojson.Marshal(item)
		if err != nil {
			return &pb.SubmitFunctionJobRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "invalid deployment request")}, nil
		}
		creates = append(creates, store.FunctionJobItemCreate{
			ItemID:      fmt.Sprintf("%s-%03d", jobID, index),
			ItemIndex:   index,
			InstanceID:  instanceID,
			RequestJSON: string(raw),
		})
	}
	if err := s.catalog.CreateFunctionJob(ctx, store.FunctionJobCreate{
		SpaceID: spaceID, JobID: jobID, Operation: functionJobOperationDeploy, Items: creates,
	}); err != nil {
		return &pb.SubmitFunctionJobRsp{RetInfo: retFromError(err)}, nil
	}
	return &pb.SubmitFunctionJobRsp{
		RetInfo: retOK(), JobId: jobID,
		Operation:  pb.FunctionJobOperation_FUNCTION_JOB_OPERATION_DEPLOY_INSTANCES,
		TotalCount: int32(len(creates)),
	}, nil
}

// SubmitDeleteFunctionInstances enqueues remote SCF deletion and catalog cleanup as a durable instance batch.
func (s *Service) SubmitDeleteFunctionInstances(ctx context.Context, req *pb.SubmitDeleteFunctionInstancesReq) (*pb.SubmitFunctionJobRsp, error) {
	spaceID, err := spacecontext.MustFromContext(ctx)
	if err != nil {
		return &pb.SubmitFunctionJobRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	if req == nil {
		return &pb.SubmitFunctionJobRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "request is required")}, nil
	}
	instanceIDs := compactStrings(req.GetInstanceIds())
	if ret := validateFunctionJobSize(len(instanceIDs), "instance_ids"); ret != nil {
		return &pb.SubmitFunctionJobRsp{RetInfo: ret}, nil
	}
	unlockInstances := lockFunctionInstances(spaceID, instanceIDs)
	defer unlockInstances()
	jobID := "function-job-" + uuid.NewString()
	items := make([]store.FunctionJobItemCreate, 0, len(instanceIDs))
	seen := make(map[string]struct{}, len(instanceIDs))
	for index, instanceID := range instanceIDs {
		if _, ok := seen[instanceID]; ok {
			return &pb.SubmitFunctionJobRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "instance_ids contains duplicate instance_id")}, nil
		}
		seen[instanceID] = struct{}{}
		instance, err := s.catalog.GetInstance(ctx, spaceID, instanceID)
		if err != nil {
			return &pb.SubmitFunctionJobRsp{RetInfo: retFromError(err)}, nil
		}
		if instance == nil || instance.IsDeleted {
			if !collectorPublishLeaseFieldsPresent(req.GetCollectorPublishLeaseId(), req.GetCollectorPublishFencingToken()) {
				return &pb.SubmitFunctionJobRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "instance not found: "+instanceID)}, nil
			}
			// Persist fenced no-op deletes too. A instance may have disappeared between
			// the caller's snapshot and scheduling; the worker still opens the
			// control-plane operation and then treats the missing generation as done.
			lifecycleID := ""
			if instance != nil {
				lifecycleID = instance.LifecycleID
			} else {
				deletedInstance, deletedErr := s.catalog.GetInstanceIncludingDeleted(ctx, spaceID, instanceID)
				if deletedErr != nil {
					return &pb.SubmitFunctionJobRsp{RetInfo: retFromError(deletedErr)}, nil
				}
				if deletedInstance != nil {
					lifecycleID = deletedInstance.LifecycleID
				}
			}
			if lifecycleID == "" {
				lifecycleID = uuid.NewString()
			}
			raw, err := protojson.Marshal(&pb.FunctionInstanceDeleteItem{
				InstanceId: instanceID, CollectorPublishLeaseId: req.GetCollectorPublishLeaseId(),
				CollectorPublishFencingToken: req.GetCollectorPublishFencingToken(),
				LifecycleId:                  lifecycleID, OperationId: fmt.Sprintf("%s-%03d", jobID, index),
			})
			if err != nil {
				return &pb.SubmitFunctionJobRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "invalid delete request")}, nil
			}
			items = append(items, store.FunctionJobItemCreate{ItemID: fmt.Sprintf("%s-%03d", jobID, index), ItemIndex: index, InstanceID: instanceID, RequestJSON: string(raw)})
			continue
		}
		lifecycleID, ensureErr := s.catalog.InstanceLifecycleID(ctx, spaceID, instanceID)
		if ensureErr != nil {
			return &pb.SubmitFunctionJobRsp{RetInfo: retFromError(ensureErr)}, nil
		}
		if instance.LifecycleID != "" && instance.LifecycleID != lifecycleID {
			return &pb.SubmitFunctionJobRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "instance lifecycle changed while scheduling delete: "+instanceID)}, nil
		}
		raw, err := protojson.Marshal(&pb.FunctionInstanceDeleteItem{
			InstanceId: instanceID, CollectorPublishLeaseId: req.GetCollectorPublishLeaseId(),
			CollectorPublishFencingToken: req.GetCollectorPublishFencingToken(),
			LifecycleId:                  lifecycleID,
			OperationId:                  fmt.Sprintf("%s-%03d", jobID, index),
		})
		if err != nil {
			return &pb.SubmitFunctionJobRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "invalid delete request")}, nil
		}
		items = append(items, store.FunctionJobItemCreate{ItemID: fmt.Sprintf("%s-%03d", jobID, index), ItemIndex: index, InstanceID: instanceID, RequestJSON: string(raw)})
	}
	if err := s.catalog.CreateFunctionJob(ctx, store.FunctionJobCreate{SpaceID: spaceID, JobID: jobID, Operation: functionJobOperationDelete, Items: items}); err != nil {
		return &pb.SubmitFunctionJobRsp{RetInfo: retFromError(err)}, nil
	}
	return &pb.SubmitFunctionJobRsp{RetInfo: retOK(), JobId: jobID, Operation: pb.FunctionJobOperation_FUNCTION_JOB_OPERATION_DELETE_INSTANCES, TotalCount: int32(len(items))}, nil
}

func (s *Service) GetFunctionJob(ctx context.Context, req *pb.GetFunctionJobReq) (*pb.GetFunctionJobRsp, error) {
	spaceID, err := spacecontext.MustFromContext(ctx)
	if err != nil {
		return &pb.GetFunctionJobRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, err.Error())}, nil
	}
	jobID := strings.TrimSpace(req.GetJobId())
	if jobID == "" {
		return &pb.GetFunctionJobRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "job_id is required")}, nil
	}
	aggregate, err := s.catalog.GetFunctionJob(ctx, spaceID, jobID)
	if err != nil {
		return &pb.GetFunctionJobRsp{RetInfo: retFromError(err)}, nil
	}
	if aggregate == nil {
		return &pb.GetFunctionJobRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "instance batch not found")}, nil
	}
	completed := aggregate.SuccessCount + aggregate.FailedCount
	progress := 0
	if aggregate.Job.TotalCount > 0 {
		progress = completed * 100 / aggregate.Job.TotalCount
	}
	rsp := &pb.GetFunctionJobRsp{
		RetInfo: retOK(),
		Job: &pb.FunctionJobSummary{
			JobId: jobID, Operation: functionJobOperationToPB(aggregate.Job.Operation),
			Status:     functionJobStatusToPB(aggregate.Job.Status),
			TotalCount: int32(aggregate.Job.TotalCount), PendingCount: int32(aggregate.PendingCount),
			RunningCount: int32(aggregate.RunningCount), SuccessCount: int32(aggregate.SuccessCount),
			FailedCount: int32(aggregate.FailedCount), ProgressPercent: int32(progress),
			CreatedAt: formatTime(aggregate.Job.CreateTime),
		},
		Items: make([]*pb.FunctionJobItemResult, 0, len(aggregate.Items)),
	}
	if aggregate.Job.CompletedAt != nil {
		rsp.Job.CompletedAt = formatTime(*aggregate.Job.CompletedAt)
	}
	for _, item := range aggregate.Items {
		out := &pb.FunctionJobItemResult{
			ItemId: item.ItemID, InstanceId: item.InstanceID,
			Status:        functionJobItemStatusToPB(item.Status),
			ResultSummary: item.ResultSummary, ErrorMessage: item.ErrorMessage,
		}
		if item.StartedAt != nil {
			out.StartedAt = formatTime(*item.StartedAt)
		}
		if item.CompletedAt != nil {
			out.CompletedAt = formatTime(*item.CompletedAt)
		}
		rsp.Items = append(rsp.Items, out)
	}
	return rsp, nil
}

func validateFunctionJobSize(count int, field string) *pb.RetInfo {
	if count < 1 || count > maxFunctionJobItems {
		return retErr(pb.ErrorCode_INVALID_PARAM, fmt.Sprintf("%s must contain 1..%d items", field, maxFunctionJobItems))
	}
	return nil
}

func (s *Service) preflightCreateInstance(
	ctx context.Context,
	spaceID string,
	item *pb.FunctionInstanceCreateItem,
	index int,
) (store.FunctionInstance, *pb.RetInfo) {
	if item == nil {
		return store.FunctionInstance{}, retErr(pb.ErrorCode_INVALID_PARAM, "instances item is required")
	}
	instance := functionInstanceFromCreateItem(spaceID, item, index)
	if err := validateTriggerType(instance.FunctionType, item.GetTriggerType()); err != nil {
		return store.FunctionInstance{}, retErr(pb.ErrorCode_INVALID_PARAM, err.Error())
	}
	if strings.TrimSpace(instance.CloudAccountID) == "" || strings.TrimSpace(instance.Region) == "" || strings.TrimSpace(instance.PackageID) == "" {
		return store.FunctionInstance{}, retErr(pb.ErrorCode_INVALID_PARAM, "instances.cloud_account_id, region and package_id are required")
	}
	account, err := s.catalog.GetAccount(ctx, instance.CloudAccountID)
	if err != nil {
		return store.FunctionInstance{}, retFromError(err)
	}
	if account == nil {
		return store.FunctionInstance{}, retErr(pb.ErrorCode_NOT_FOUND, "cloud account not found")
	}
	pkg, err := s.catalog.GetPackage(ctx, spaceID, instance.PackageID)
	if err != nil {
		return store.FunctionInstance{}, retFromError(err)
	}
	if pkg == nil {
		return store.FunctionInstance{}, retErr(pb.ErrorCode_NOT_FOUND, "package not found")
	}
	if pkg.Status != "available" {
		return store.FunctionInstance{}, retErr(pb.ErrorCode_INVALID_PARAM, "package is not available")
	}
	if ret := validateCollectorPublishLeaseAtSubmit(pkg, item.GetCollectorPublishLeaseId(), item.GetCollectorPublishFencingToken()); ret != nil {
		return store.FunctionInstance{}, ret
	}
	return instance, nil
}

func (s *Service) preflightDeployInstance(ctx context.Context, spaceID string, item *pb.FunctionInstanceDeployItem) *pb.RetInfo {
	if item == nil || strings.TrimSpace(item.GetInstanceId()) == "" || strings.TrimSpace(item.GetPackageId()) == "" {
		return retErr(pb.ErrorCode_INVALID_PARAM, "deployments.instance_id and package_id are required")
	}
	instance, err := s.catalog.GetInstance(ctx, spaceID, item.GetInstanceId())
	if err != nil {
		return retFromError(err)
	}
	if instance == nil {
		return retErr(pb.ErrorCode_NOT_FOUND, "instance not found")
	}
	pkg, err := s.catalog.GetPackage(ctx, spaceID, item.GetPackageId())
	if err != nil {
		return retFromError(err)
	}
	if pkg == nil {
		return retErr(pb.ErrorCode_NOT_FOUND, "package not found")
	}
	if pkg.Status != "available" {
		return retErr(pb.ErrorCode_INVALID_PARAM, "package is not available")
	}
	if validateCollectorPublishLeaseAtSubmit(pkg, item.GetCollectorPublishLeaseId(), item.GetCollectorPublishFencingToken()) != nil {
		return retErr(pb.ErrorCode_NO_PERMISSION, "collector market_fetcher deployment requires a publish lease and fencing token")
	}
	if currentPackage, packageErr := s.catalog.GetPackage(ctx, spaceID, instance.PackageID); packageErr != nil {
		return retFromError(packageErr)
	} else if ret := validateCollectorPublishLeaseAtSubmit(currentPackage, item.GetCollectorPublishLeaseId(), item.GetCollectorPublishFencingToken()); ret != nil {
		return ret
	}
	return nil
}

func functionJobOperationToPB(operation string) pb.FunctionJobOperation {
	switch operation {
	case functionJobOperationCreate:
		return pb.FunctionJobOperation_FUNCTION_JOB_OPERATION_CREATE_INSTANCES
	case functionJobOperationDeploy:
		return pb.FunctionJobOperation_FUNCTION_JOB_OPERATION_DEPLOY_INSTANCES
	case functionJobOperationDelete:
		return pb.FunctionJobOperation_FUNCTION_JOB_OPERATION_DELETE_INSTANCES
	case functionJobOperationRuntimeConfig:
		return pb.FunctionJobOperation_FUNCTION_JOB_OPERATION_UPDATE_RUNTIME_CONFIGS
	default:
		return pb.FunctionJobOperation_FUNCTION_JOB_OPERATION_UNSPECIFIED
	}
}

func functionJobStatusToPB(status string) pb.FunctionJobStatus {
	switch status {
	case store.FunctionJobPending:
		return pb.FunctionJobStatus_FUNCTION_JOB_STATUS_PENDING
	case store.FunctionJobRunning:
		return pb.FunctionJobStatus_FUNCTION_JOB_STATUS_RUNNING
	case store.FunctionJobSuccess:
		return pb.FunctionJobStatus_FUNCTION_JOB_STATUS_SUCCESS
	case store.FunctionJobFailed:
		return pb.FunctionJobStatus_FUNCTION_JOB_STATUS_FAILED
	case store.FunctionJobPartial:
		return pb.FunctionJobStatus_FUNCTION_JOB_STATUS_PARTIAL
	default:
		return pb.FunctionJobStatus_FUNCTION_JOB_STATUS_UNSPECIFIED
	}
}

func functionJobItemStatusToPB(status string) pb.FunctionJobItemStatus {
	switch status {
	case store.FunctionJobPending:
		return pb.FunctionJobItemStatus_FUNCTION_JOB_ITEM_STATUS_PENDING
	case store.FunctionJobRunning:
		return pb.FunctionJobItemStatus_FUNCTION_JOB_ITEM_STATUS_RUNNING
	case store.FunctionJobReconciliationRequired:
		return pb.FunctionJobItemStatus_FUNCTION_JOB_ITEM_STATUS_RUNNING
	case store.FunctionJobSuccess:
		return pb.FunctionJobItemStatus_FUNCTION_JOB_ITEM_STATUS_SUCCESS
	case store.FunctionJobFailed:
		return pb.FunctionJobItemStatus_FUNCTION_JOB_ITEM_STATUS_FAILED
	default:
		return pb.FunctionJobItemStatus_FUNCTION_JOB_ITEM_STATUS_UNSPECIFIED
	}
}
