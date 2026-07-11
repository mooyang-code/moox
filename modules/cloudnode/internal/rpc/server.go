package rpc

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobhistory"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobqueue"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobstate"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/projection"
	tencentscf "github.com/mooyang-code/moox/modules/cloudnode/internal/providers/tencent-scf"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/repository"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/storage"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"gorm.io/gorm"
)

// Service is the independent cloudnode service implementation.
type Service struct {
	pb.UnimplementedCloudNodeMgr
	dbm              *storage.Manager
	jobState         jobstate.Store
	history          *jobhistory.Store
	executionQueue   jobqueue.ExecutionQueue
	heartbeatSink    projection.HeartbeatSink
	catalog          *repository.CatalogRepository
	scfClientFactory func(repository.CloudAccount) scfProvisioner
}

type scfProvisioner interface {
	GetFunction(context.Context, tencentscf.FunctionRef) (*tencentscf.FunctionInfo, error)
	CreateFunction(context.Context, tencentscf.CreateFunctionRequest) (*tencentscf.CreateFunctionResponse, error)
	DeleteFunction(context.Context, tencentscf.FunctionRef) error
	UpdateFunctionCode(context.Context, tencentscf.UpdateFunctionCodeRequest) (*tencentscf.UpdateFunctionCodeResponse, error)
	UpdateFunctionConfiguration(context.Context, tencentscf.UpdateFunctionConfigurationRequest) (*tencentscf.UpdateFunctionConfigurationResponse, error)
}

type Option func(*Service)

func WithExecutionQueue(queue jobqueue.ExecutionQueue) Option {
	return func(s *Service) { s.executionQueue = queue }
}

func WithJobStateStore(store jobstate.Store) Option {
	return func(s *Service) { s.jobState = store }
}

func WithJobHistoryStore(store *jobhistory.Store) Option {
	return func(s *Service) { s.history = store }
}

func WithHeartbeatSink(sink projection.HeartbeatSink) Option {
	return func(s *Service) { s.heartbeatSink = sink }
}

// New creates a cloudnode RPC service.
func New(dbm *storage.Manager, opts ...Option) *Service {
	svc := &Service{
		dbm:              dbm,
		catalog:          repository.NewCatalogRepository(dbm.DB()),
		scfClientFactory: defaultSCFClientFactory,
	}
	for _, opt := range opts {
		opt(svc)
	}
	return svc
}

func defaultSCFClientFactory(account repository.CloudAccount) scfProvisioner {
	return tencentscf.New(account.SecretID, account.SecretKey)
}

func retOK() *pb.RetInfo {
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "ok"}
}

func retErr(code pb.ErrorCode, msg string) *pb.RetInfo {
	return &pb.RetInfo{Code: code, Msg: msg}
}

func retFromError(err error) *pb.RetInfo {
	switch {
	case errors.Is(err, repository.ErrPollingNodeNotFound):
		return retErr(pb.ErrorCode_NOT_FOUND, "cloud node not found")
	case errors.Is(err, jobstate.ErrConflict):
		return retErr(pb.ErrorCode_INVALID_PARAM, "conflict: job item state does not allow this operation")
	case errors.Is(err, jobstate.ErrStaleAttempt):
		return retErr(pb.ErrorCode_INVALID_PARAM, "conflict: job item attempt is stale")
	case errors.Is(err, jobstate.ErrInactive):
		return retErr(pb.ErrorCode_INVALID_PARAM, "conflict: job item is not running")
	case errors.Is(err, jobstate.ErrNotFound):
		return retErr(pb.ErrorCode_NOT_FOUND, "job item not found")
	case errors.Is(err, jobstate.ErrInvalid):
		return retErr(pb.ErrorCode_INVALID_PARAM, "invalid job item")
	case errors.Is(err, gorm.ErrRecordNotFound):
		return retErr(pb.ErrorCode_NOT_FOUND, "resource not found")
	default:
		return retErr(pb.ErrorCode_INNER_ERR, "internal error")
	}
}
